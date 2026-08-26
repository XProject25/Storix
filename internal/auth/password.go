// Package auth implements Storix authentication.
//
// It holds four independent pieces that the API layer composes: Argon2id
// password hashing in the PHC string format, cookie backed sessions with CSRF
// protection, RFC 6238 TOTP codes for two factor sign in, and an in memory
// sliding window rate limiter used to slow brute force attempts.
//
// Secrets never sit in the database in a reversible form. Passwords are
// Argon2id hashes, a session cookie carries a random token whose SHA-256
// digest is what the sessions table stores, and share links follow the same
// rule.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Errors reported by the password and token helpers.
var (
	// ErrEmptyPassword is returned when hashing is asked to run on nothing.
	ErrEmptyPassword = errors.New("auth: empty password")
	// ErrInvalidHash means the encoded password is not a Storix PHC string.
	ErrInvalidHash = errors.New("auth: malformed password hash")
	// ErrUnsupportedHash means the PHC string names an algorithm or version
	// this build cannot verify.
	ErrUnsupportedHash = errors.New("auth: unsupported password hash")
	// ErrPasswordTooShort is returned by ValidatePassword.
	ErrPasswordTooShort = errors.New("auth: password must be at least 8 characters")
	// ErrPasswordBlank is returned when a password is only whitespace.
	ErrPasswordBlank = errors.New("auth: password cannot be blank")
	// ErrPasswordCommon is returned when a password appears in the list of
	// passwords attackers try first.
	ErrPasswordCommon = errors.New("auth: password is too common")
)

// MinPasswordLength is the shortest password Storix accepts.
const MinPasswordLength = 8

// Params are the Argon2id cost settings baked into every hash.
type Params struct {
	// Memory is the amount of memory in KiB the hash occupies.
	Memory uint32
	// Time is the number of passes over that memory.
	Time uint32
	// Threads is the degree of parallelism.
	Threads uint8
	// SaltLen is the salt length in bytes.
	SaltLen uint32
	// KeyLen is the derived key length in bytes.
	KeyLen uint32
}

// DefaultParams returns the cost settings Storix hashes with. They target
// roughly a tenth of a second on a small server while staying inside the
// memory budget of a modest VPS.
func DefaultParams() Params {
	threads := runtime.NumCPU()
	if threads > 4 {
		threads = 4
	}
	if threads < 1 {
		threads = 1
	}
	return Params{
		Memory:  64 * 1024,
		Time:    3,
		Threads: uint8(threads),
		SaltLen: 16,
		KeyLen:  32,
	}
}

// normalize replaces zero or nonsensical fields with their defaults so a
// partially filled Params can never produce a weak hash.
func (p Params) normalize() Params {
	d := DefaultParams()
	if p.Memory == 0 {
		p.Memory = d.Memory
	}
	if p.Time == 0 {
		p.Time = d.Time
	}
	if p.Threads == 0 {
		p.Threads = d.Threads
	}
	if p.SaltLen < 8 {
		p.SaltLen = d.SaltLen
	}
	if p.KeyLen < 16 {
		p.KeyLen = d.KeyLen
	}
	return p
}

// HashPassword hashes a password with the default cost settings and returns
// the PHC encoded result.
func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, DefaultParams())
}

// HashPasswordWith hashes a password with explicit cost settings. The result
// is a PHC string of the form $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>,
// where both binary fields use unpadded standard base64.
func HashPasswordWith(password string, p Params) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	p = p.normalize()
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// decodedHash is a parsed PHC string.
type decodedHash struct {
	params Params
	salt   []byte
	key    []byte
}

// decodeHash parses a Storix PHC string. It rejects anything it does not
// fully understand rather than falling back to a weaker interpretation.
func decodeHash(encoded string) (decodedHash, error) {
	var out decodedHash
	parts := strings.Split(encoded, "$")
	// A well formed string opens with the separator, so the first field is
	// empty and exactly five more follow it.
	if len(parts) != 6 || parts[0] != "" {
		return out, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return out, ErrUnsupportedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return out, ErrInvalidHash
	}
	if version != argon2.Version {
		return out, ErrUnsupportedHash
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return out, ErrInvalidHash
	}
	if memory == 0 || passes == 0 || threads == 0 {
		return out, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return out, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return out, ErrInvalidHash
	}
	out.params = Params{
		Memory:  memory,
		Time:    passes,
		Threads: threads,
		SaltLen: uint32(len(salt)),
		KeyLen:  uint32(len(key)),
	}
	out.salt = salt
	out.key = key
	return out, nil
}

// VerifyPassword reports whether a password matches an encoded hash. A
// mismatch is (false, nil); a hash Storix cannot parse is an error, so the
// caller can tell a wrong password apart from a corrupt record.
func VerifyPassword(encoded, password string) (bool, error) {
	d, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(password), d.salt,
		d.params.Time, d.params.Memory, d.params.Threads, d.params.KeyLen)
	return subtle.ConstantTimeCompare(candidate, d.key) == 1, nil
}

// NeedsRehash reports whether a stored hash was produced with settings weaker
// than the ones now in force, which is the signal to hash the password again
// on the next successful sign in. A hash that cannot be parsed always needs
// replacing.
func NeedsRehash(encoded string, p Params) bool {
	d, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	p = p.normalize()
	switch {
	case d.params.Memory < p.Memory,
		d.params.Time < p.Time,
		d.params.Threads < p.Threads,
		d.params.SaltLen < p.SaltLen,
		d.params.KeyLen < p.KeyLen:
		return true
	}
	return false
}

// commonPasswords holds the handful of passwords that lead every credential
// stuffing list. The published lists run to millions of entries and do not
// belong in a binary; the opening entries catch most of the real risk.
var commonPasswords = map[string]struct{}{
	"password":      {},
	"password1":     {},
	"password123":   {},
	"passw0rd":      {},
	"123456":        {},
	"1234567":       {},
	"12345678":      {},
	"123456789":     {},
	"1234567890":    {},
	"111111":        {},
	"000000":        {},
	"654321":        {},
	"qwerty":        {},
	"qwerty123":     {},
	"qwertyuiop":    {},
	"1q2w3e4r":      {},
	"zaq12wsx":      {},
	"abc123":        {},
	"admin":         {},
	"admin123":      {},
	"administrator": {},
	"root":          {},
	"toor":          {},
	"guest":         {},
	"letmein":       {},
	"welcome":       {},
	"welcome1":      {},
	"changeme":      {},
	"iloveyou":      {},
	"monkey":        {},
	"dragon":        {},
	"sunshine":      {},
	"princess":      {},
	"football":      {},
	"baseball":      {},
	"superman":      {},
	"starwars":      {},
	"trustno1":      {},
	"login":         {},
	"test1234":      {},
	"storix":        {},
}

// ValidatePassword applies the minimum policy every Storix password has to
// meet. It stays deliberately short: a length floor plus a rejection of the
// obvious choices, with the strength meter guiding users beyond that.
func ValidatePassword(password string) error {
	if strings.TrimSpace(password) == "" {
		return ErrPasswordBlank
	}
	if len([]rune(password)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if _, bad := commonPasswords[strings.ToLower(password)]; bad {
		return ErrPasswordCommon
	}
	return nil
}

// PasswordScore rates a password from 0 (unusable) to 4 (strong) to drive the
// strength meter in the interface. It is a hint for the person choosing the
// password, never a gate: ValidatePassword decides what is accepted.
func PasswordScore(password string) int {
	runes := []rune(password)
	n := len(runes)
	if n == 0 {
		return 0
	}
	if _, bad := commonPasswords[strings.ToLower(password)]; bad {
		return 0
	}

	var lower, upper, digit, symbol bool
	unique := make(map[rune]struct{}, n)
	for _, r := range runes {
		unique[r] = struct{}{}
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			classes++
		}
	}

	score := 0
	if n >= MinPasswordLength {
		score++
	}
	if n >= 12 {
		score++
	}
	if n >= 16 {
		score++
	}
	if classes >= 3 {
		score++
	}
	if classes == 4 {
		score++
	}

	// A long password built from two or three distinct characters is not
	// really long, so cap it whatever the length bonuses said.
	if len(unique) <= 3 {
		score = 1
	}
	if n < MinPasswordLength && score > 1 {
		score = 1
	}
	if score > 4 {
		score = 4
	}
	return score
}

// GenerateToken returns a URL safe random token carrying the requested number
// of bytes of entropy. A request for fewer than 16 bytes is raised to 16.
func GenerateToken(bytes int) (string, error) {
	if bytes < 16 {
		bytes = 16
	}
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// MustToken is GenerateToken for call sites that have no sensible way to
// report a failure, such as package level initialisation. It panics only when
// the operating system entropy source is unavailable, a condition the Go
// runtime already treats as fatal. Request paths should use GenerateToken.
func MustToken(bytes int) string {
	tok, err := GenerateToken(bytes)
	if err != nil {
		panic(err)
	}
	return tok
}

// HashToken returns the lowercase hex SHA-256 of a token. Session identifiers
// and share tokens are stored in this form, so a leaked database cannot be
// replayed against the running server. SHA-256 is the right tool here rather
// than Argon2id because the input is already high entropy random data.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// shareAlphabet omits the characters people confuse when reading a link aloud
// or copying it by hand: zero, capital O, one, capital I and lower L.
const shareAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ShareTokenLength is the character count of a public share token.
const ShareTokenLength = 10

// ShareToken returns a short, URL safe, unambiguous token for a public link.
func ShareToken() string {
	return randomString(ShareTokenLength, shareAlphabet)
}

// randomString draws characters uniformly from an alphabet using rejection
// sampling, so no character comes up more often than another.
func randomString(n int, alphabet string) string {
	if n <= 0 || alphabet == "" {
		return ""
	}
	size := len(alphabet)
	// The largest multiple of size that fits in a byte. Values at or above it
	// are discarded, which is what keeps the distribution flat.
	limit := 256 - (256 % size)
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/2+8)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand does not fail on any platform Storix supports. A
			// failure here means the entropy source is gone, which the
			// runtime already treats as unrecoverable.
			panic(fmt.Errorf("auth: read random: %w", err))
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%size])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}
