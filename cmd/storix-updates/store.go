// Storage for the Storix update service.
//
// There is one table and one row per instance. No column here can hold a
// network address, because the service never has one to write: see the notice
// at the top of main.go.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	// Pure Go SQLite driver, so this service needs no cgo and no external
	// database server. It is the driver the rest of Storix uses.
	_ "modernc.org/sqlite"
)

// schemaSQL creates the database. It is applied on every start and is safe to
// run again.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS instances (
  instance   TEXT PRIMARY KEY,
  version    TEXT NOT NULL,
  os         TEXT NOT NULL,
  arch       TEXT NOT NULL,
  channel    TEXT NOT NULL DEFAULT 'stable',
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  checks     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS instances_last_seen ON instances (last_seen);
CREATE INDEX IF NOT EXISTS instances_version ON instances (version);
`

// Validation failures. They are sentinels so the HTTP layer can answer with a
// message naming the field the caller got wrong.
var (
	// ErrInvalidInstance means the identifier was not 32 lower case hexadecimal characters.
	ErrInvalidInstance = errors.New("updates: instance must be 32 lower case hexadecimal characters")
	// ErrInvalidVersion means the reported version was empty or unreasonable.
	ErrInvalidVersion = errors.New("updates: version must be 1 to 32 characters of digits, letters, dots, dashes or underscores")
	// ErrInvalidPlatform means the reported os or arch was unreasonable.
	ErrInvalidPlatform = errors.New("updates: os and arch must be 1 to 16 lower case letters, digits or underscores")
	// ErrInvalidChannel means the reported release channel was unreasonable.
	ErrInvalidChannel = errors.New("updates: channel must be 1 to 16 lower case letters")
)

// Field limits. They keep a hostile caller from filling the database with long
// strings. They are not an opinion about version numbering.
const (
	instanceLen    = 32
	maxVersionLen  = 32
	maxPlatformLen = 16
)

// The release tracks this service publishes. The channel arrives from the
// caller and becomes both a cache key and a lookup against the release feed,
// so it has to come from a fixed set rather than merely look like a word.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// How many identifiers this service has never seen before may become rows.
//
// An anonymous endpoint cannot tell a real server from an invented one, so the
// question is not how to refuse forgeries but how to bound what they cost. New
// identifiers are given out of a bucket that refills slowly, and the table has
// a ceiling on top of that. A server already known to the service is never
// gated: it only updates the row it already has.
const (
	maxInstances     = 250_000
	newInstanceBurst = 500
	newInstancePerHr = 200
)

// CheckIn is one server reporting in. These five values are everything the
// service accepts and everything it keeps. There is deliberately no field for
// an address, a host name or an account.
type CheckIn struct {
	Instance string
	Version  string
	OS       string
	Arch     string
	Channel  string
}

// Validate reports whether a check in is well formed. The HTTP handler calls it
// before anything is recorded and Record calls it again, so a malformed
// identifier cannot reach the database by some other route.
func (c CheckIn) Validate() error {
	if !validInstance(c.Instance) {
		return ErrInvalidInstance
	}
	if !validVersion(c.Version) {
		return ErrInvalidVersion
	}
	if !validPlatform(c.OS) || !validPlatform(c.Arch) {
		return ErrInvalidPlatform
	}
	if !validChannel(c.Channel) {
		return ErrInvalidChannel
	}
	return nil
}

// validInstance accepts exactly 32 lower case hexadecimal characters, which is
// what the Storix side generates and stores as its instance identifier.
func validInstance(s string) bool {
	if len(s) != instanceLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validVersion accepts a short dotted version, with or without a prerelease
// suffix. Anything longer or stranger is refused rather than stored.
func validVersion(s string) bool {
	if s == "" || len(s) > maxVersionLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '.', c == '-', c == '+', c == '_':
		default:
			return false
		}
	}
	return true
}

// validPlatform accepts a Go GOOS or GOARCH value.
func validPlatform(s string) bool {
	if s == "" || len(s) > maxPlatformLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c == '_':
		default:
			return false
		}
	}
	return true
}

// validChannel accepts a release track name such as stable or beta.
func validChannel(s string) bool {
	return s == ChannelStable || s == ChannelBeta
}

// Stats is the summary served on /v1/stats. It counts servers, it never
// identifies one: no instance identifier appears anywhere in it.
type Stats struct {
	Total       int            `json:"total"`
	Active24h   int            `json:"active24h"`
	Active7d    int            `json:"active7d"`
	Active30d   int            `json:"active30d"`
	Checks      int64          `json:"checks"`
	Versions    map[string]int `json:"versions"`
	Platforms   map[string]int `json:"platforms"`
	RefusedNew  int64          `json:"refusedNew"`
	FirstSeen   *time.Time     `json:"firstSeen"`
	LastSeen    *time.Time     `json:"lastSeen"`
	GeneratedAt time.Time      `json:"generatedAt"`
}

// Store owns the SQLite connection pool.
type Store struct {
	db   *sql.DB
	path string

	// rows is how many instances the table holds. It is kept here rather than
	// counted per request because the only question asked of it, whether the
	// table has reached its ceiling, is asked on every unknown identifier.
	rows atomic.Int64

	// refused counts identifiers that were turned away by the budget. It is
	// reported in the statistics, because a number climbing there is how the
	// owner learns somebody is inventing servers.
	refused atomic.Int64

	gate   sync.Mutex
	tokens float64
	filled time.Time
}

// OpenStore connects to the database, creating it when needed.
func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("updates: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("updates: create %s: %w", dir, err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(15000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("updates: open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("updates: open database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("updates: create schema: %w", err)
	}
	// SQLite creates the file with whatever the umask allows, which on most
	// hosts is readable by everyone. This service usually shares a machine
	// with unrelated sites, so the file is narrowed to its own account.
	if err := os.Chmod(path, 0o640); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return nil, fmt.Errorf("updates: secure %s: %w", path, err)
	}
	st := &Store{db: db, path: path}
	var rows int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances`).Scan(&rows); err != nil {
		db.Close()
		return nil, fmt.Errorf("updates: count instances: %w", err)
	}
	st.rows.Store(rows)
	return st, nil
}

// Path reports the database file location.
func (s *Store) Path() string { return s.path }

// Close releases the pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	// A checkpoint keeps the WAL from growing without bound between runs.
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

// Record stores a check in. The first one creates the row, a later one from the
// same instance updates it in place, so a server checking in twice a day for a
// year stays one row and one counted server.
//
// The error it returns never carries the identifier, because errors are logged
// and nothing identifying belongs in a log.
func (s *Store) Record(ctx context.Context, in CheckIn, now time.Time) error {
	if err := in.Validate(); err != nil {
		return err
	}
	stamp := now.UTC().Unix()

	// A server this service already knows is never gated. It owns a row
	// already, so writing to it costs nothing and refusing it would lose a
	// real install from the count.
	res, err := s.db.ExecContext(ctx, `
        UPDATE instances SET
            version   = ?,
            os        = ?,
            arch      = ?,
            channel   = ?,
            last_seen = ?,
            checks    = checks + 1
        WHERE instance = ?`,
		in.Version, in.OS, in.Arch, in.Channel, stamp, in.Instance)
	if err != nil {
		return fmt.Errorf("updates: record check in: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}

	// An identifier nobody has seen before wants a new row, and that is the
	// only thing an anonymous caller can make this service spend. Refusing is
	// not an error: the caller still gets its answer, it simply is not
	// counted.
	if !s.allowNew(now) {
		s.refused.Add(1)
		return nil
	}
	res, err = s.db.ExecContext(ctx, `
        INSERT INTO instances (instance, version, os, arch, channel, first_seen, last_seen, checks)
        VALUES (?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(instance) DO NOTHING`,
		in.Instance, in.Version, in.OS, in.Arch, in.Channel, stamp, stamp)
	if err != nil {
		return fmt.Errorf("updates: record check in: %w", err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		// The row is written either way. Only the counter is uncertain, and
		// Prune reseeds it.
		return nil
	}
	if inserted > 0 {
		s.rows.Add(1)
		return nil
	}
	// Two check-ins from the same new server raced and the other one won the
	// insert. Apply this one as the update it turned out to be.
	if _, err := s.db.ExecContext(ctx, `
        UPDATE instances SET last_seen = ?, checks = checks + 1 WHERE instance = ?`,
		stamp, in.Instance); err != nil {
		return fmt.Errorf("updates: record check in: %w", err)
	}
	return nil
}

// allowNew reports whether an identifier the service has never seen may become
// a row. The bucket refills with time, so genuine growth is never blocked for
// long, while a caller inventing identifiers as fast as it can send them is
// held to a few thousand a day instead of as many as it likes.
func (s *Store) allowNew(now time.Time) bool {
	if s.rows.Load() >= maxInstances {
		return false
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	if s.filled.IsZero() {
		s.filled, s.tokens = now, newInstanceBurst
	}
	if elapsed := now.Sub(s.filled); elapsed > 0 {
		s.tokens += elapsed.Hours() * newInstancePerHr
		if s.tokens > newInstanceBurst {
			s.tokens = newInstanceBurst
		}
		s.filled = now
	}
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

// Stats summarises the table as it stands at now.
func (s *Store) Stats(ctx context.Context, now time.Time) (Stats, error) {
	out := Stats{
		Versions:    map[string]int{},
		Platforms:   map[string]int{},
		GeneratedAt: now.UTC().Truncate(time.Second),
	}
	day := now.Add(-24 * time.Hour).Unix()
	week := now.Add(-7 * 24 * time.Hour).Unix()
	month := now.Add(-30 * 24 * time.Hour).Unix()

	var first, last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
        SELECT COUNT(*),
               COALESCE(SUM(checks), 0),
               COALESCE(SUM(last_seen >= ?), 0),
               COALESCE(SUM(last_seen >= ?), 0),
               COALESCE(SUM(last_seen >= ?), 0),
               MIN(first_seen),
               MAX(last_seen)
        FROM instances`, day, week, month).
		Scan(&out.Total, &out.Checks, &out.Active24h, &out.Active7d, &out.Active30d, &first, &last)
	if err != nil {
		return out, fmt.Errorf("updates: read totals: %w", err)
	}
	if first.Valid {
		t := time.Unix(first.Int64, 0).UTC()
		out.FirstSeen = &t
	}
	if last.Valid {
		t := time.Unix(last.Int64, 0).UTC()
		out.LastSeen = &t
	}

	if err := s.tally(ctx, `SELECT version, COUNT(*) FROM instances GROUP BY version ORDER BY version`, out.Versions); err != nil {
		return out, err
	}
	const platformQuery = `SELECT os || '/' || arch, COUNT(*) FROM instances GROUP BY os, arch ORDER BY 1`
	if err := s.tally(ctx, platformQuery, out.Platforms); err != nil {
		return out, err
	}
	// Not a count of servers, but of identifiers the budget turned away since
	// this process started. Zero is the normal reading.
	out.RefusedNew = s.refused.Load()
	return out, nil
}

// tally runs a two column "label, count" query into a map.
func (s *Store) tally(ctx context.Context, query string, into map[string]int) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("updates: read breakdown: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var count int
		if err := rows.Scan(&label, &count); err != nil {
			return fmt.Errorf("updates: scan breakdown: %w", err)
		}
		into[label] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("updates: read breakdown: %w", err)
	}
	return nil
}

// Prune deletes instances not seen for the retention period, which is the
// deletion docs/UPDATES.md promises. It reports how many rows went.
func (s *Store) Prune(ctx context.Context, retention time.Duration, now time.Time) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-retention).Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("updates: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		n = 0
	}
	// Rows that were forgotten are room the ceiling may use again, so the
	// counter is read back from the table rather than adjusted by hand.
	var rows int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances`).Scan(&rows); err == nil {
		s.rows.Store(rows)
	}
	return n, nil
}
