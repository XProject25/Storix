package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. Storix follows the profile every authenticator app
// implements: HMAC-SHA1, six digits, a thirty second step. Changing any of
// them would lock out apps that ignore the otpauth parameters.
const (
	// TOTPDigits is the number of digits in a generated code.
	TOTPDigits = 6
	// TOTPPeriod is the code lifetime in seconds.
	TOTPPeriod = 30
	// TOTPSecretBytes is the size of a freshly generated shared secret.
	TOTPSecretBytes = 20
)

// TOTP errors.
var (
	// ErrInvalidSecret means the shared secret is not valid base32.
	ErrInvalidSecret = errors.New("auth: invalid TOTP secret")
)

// b32 is the unpadded base32 alphabet authenticator apps expect.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh base32 encoded TOTP shared secret.
func NewSecret() (string, error) {
	buf := make([]byte, TOTPSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read TOTP secret: %w", err)
	}
	return b32.EncodeToString(buf), nil
}

// decodeSecret accepts a secret as a user might paste it: lower case, padded,
// or broken into space separated groups.
func decodeSecret(secret string) ([]byte, error) {
	cleaned := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' || r == '\n' || r == '\r' || r == '=' {
			return -1
		}
		return r
	}, secret))
	if cleaned == "" {
		return nil, ErrInvalidSecret
	}
	key, err := b32.DecodeString(cleaned)
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// Code returns the TOTP value for a secret at a point in time.
func Code(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeAtCounter(key, uint64(t.Unix()/TOTPPeriod)), nil
}

// codeAtCounter is the HOTP construction of RFC 4226 with the dynamic
// truncation step, which the TOTP counter feeds.
func codeAtCounter(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(sha1.New, key)
	// hash.Hash never reports a write error, so the value is discarded here
	// on purpose rather than pretending there is a failure path.
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < TOTPDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", TOTPDigits, value%mod)
}

// Verify reports whether a code matches the secret at the given time, also
// accepting the skewSteps windows either side so a clock a little out of sync
// still works. Every candidate is compared in constant time and the loop does
// not stop early, so a wrong code costs the same as a right one.
func Verify(secret, code string, t time.Time, skewSteps int) bool {
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != TOTPDigits {
		return false
	}
	if skewSteps < 0 {
		skewSteps = -skewSteps
	}
	counter := t.Unix() / TOTPPeriod
	match := 0
	for step := -skewSteps; step <= skewSteps; step++ {
		c := counter + int64(step)
		if c < 0 {
			continue
		}
		candidate := codeAtCounter(key, uint64(c))
		match |= subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
	}
	return match == 1
}

// ProvisioningURI builds the otpauth URI an authenticator app scans. Both the
// label and the issuer parameter carry the issuer, which is what the apps
// expect and what keeps two Storix installs apart in the same app.
func ProvisioningURI(secret, account, issuer string) string {
	label := url.PathEscape(account)
	if issuer != "" {
		label = url.PathEscape(issuer) + ":" + label
	}
	// The query is assembled in the order apps document rather than through
	// url.Values, which would sort the keys.
	q := "secret=" + url.QueryEscape(strings.ToUpper(strings.TrimSpace(secret)))
	if issuer != "" {
		q += "&issuer=" + url.QueryEscape(issuer)
	}
	q += fmt.Sprintf("&algorithm=SHA1&digits=%d&period=%d", TOTPDigits, TOTPPeriod)
	return "otpauth://totp/" + label + "?" + q
}

// RecoveryCodeGroups is the number of five character groups in a recovery
// code.
const RecoveryCodeGroups = 2

// RecoveryCodes returns n single use recovery codes, each two groups of five
// characters drawn from the unambiguous share alphabet, for example
// "K7RTM-9PXHD". Store the hash of each code, not the code itself.
func RecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		return nil, errors.New("auth: recovery code count must be positive")
	}
	codes := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for len(codes) < n {
		groups := make([]string, 0, RecoveryCodeGroups)
		for i := 0; i < RecoveryCodeGroups; i++ {
			groups = append(groups, randomString(5, shareAlphabet))
		}
		code := strings.Join(groups, "-")
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes, nil
}
