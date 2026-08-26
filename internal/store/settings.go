package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Well known setting keys. Anything else may be stored freely, these are the
// ones Storix itself reads.
const (
	// SettingSetupCompleted flips to true once the first run wizard finished.
	SettingSetupCompleted = "setup.completed"
	// SettingBranding holds the JSON encoded Branding record.
	SettingBranding = "branding"
	// SettingInstanceID is the stable random identifier of this install.
	SettingInstanceID = "instance.id"
	// SettingUpdateChannel selects the release track the updater follows.
	SettingUpdateChannel = "update.channel"
	// SettingFeatures holds the JSON encoded feature toggle map.
	SettingFeatures = "features"
)

// GetSetting reads one value. A missing key yields an empty string and a nil
// error, so callers can treat absence as "use the default".
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("store: read setting %q: %w", key, err)
	}
	return value, nil
}

// SetSetting writes one value, creating or replacing the row.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("store: empty setting key")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("store: write setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a key. Removing a key that was never set is not an
// error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %q: %w", key, err)
	}
	return nil
}

// AllSettings returns every stored key and value. The map is never nil.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("store: list settings: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store: scan setting: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list settings: %w", err)
	}
	return out, nil
}

// GetJSON decodes a JSON encoded setting into dst. It reports false with a nil
// error when the key was never set, leaving dst untouched.
func (s *Store) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, err := s.GetSetting(ctx, key)
	if err != nil {
		return false, err
	}
	if raw == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("store: decode setting %q: %w", key, err)
	}
	return true, nil
}

// SetJSON stores a value in its JSON encoding.
func (s *Store) SetJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: encode setting %q: %w", key, err)
	}
	return s.SetSetting(ctx, key, string(raw))
}

// GetBool reads a boolean setting, falling back to def when the key is absent,
// unreadable or holds something that is not a boolean. It is meant for the
// many call sites where a storage hiccup should not break rendering.
func (s *Store) GetBool(ctx context.Context, key string, def bool) bool {
	raw, err := s.GetSetting(ctx, key)
	if err != nil || raw == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	}
	return def
}

// SetBool stores a boolean setting in the canonical "1" or "0" form.
func (s *Store) SetBool(ctx context.Context, key string, v bool) error {
	if v {
		return s.SetSetting(ctx, key, "1")
	}
	return s.SetSetting(ctx, key, "0")
}

// SetupCompleted reports whether the first run wizard has been completed. A
// database problem reads as "not completed", which keeps the wizard reachable
// instead of locking the operator out.
func (s *Store) SetupCompleted(ctx context.Context) bool {
	return s.GetBool(ctx, SettingSetupCompleted, false)
}

// MarkSetupCompleted records that the first run wizard finished.
func (s *Store) MarkSetupCompleted(ctx context.Context) error {
	return s.SetBool(ctx, SettingSetupCompleted, true)
}

// InstanceID returns the stable identifier of this install, 32 hexadecimal
// characters. It is generated and persisted on the first call.
func (s *Store) InstanceID(ctx context.Context) (string, error) {
	id, err := s.GetSetting(ctx, SettingInstanceID)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: generate instance id: %w", err)
	}
	id = hex.EncodeToString(buf)

	// INSERT OR IGNORE rather than an upsert: if two callers race on a fresh
	// database, the first written identifier is the one everybody keeps.
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO settings (key, value, updated_at) VALUES (?, ?, ?)`,
		SettingInstanceID, id, ts(time.Now())); err != nil {
		return "", fmt.Errorf("store: store instance id: %w", err)
	}
	stored, err := s.GetSetting(ctx, SettingInstanceID)
	if err != nil {
		return "", err
	}
	if stored == "" {
		return id, nil
	}
	return stored, nil
}
