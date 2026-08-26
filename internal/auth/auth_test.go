package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testParams keep Argon2 cheap enough to run in a unit test while still
// exercising the real code path.
func testParams() Params {
	return Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		password string
	}{
		{"ascii", "correct horse battery staple"},
		{"short", "abcdefgh"},
		{"symbols", "p@$$w0rd!#%&*()_+-=[]{}|;:,.<>?/"},
		{"unicode", "sarajevo-zima-2026-ćevapi"},
		{"spaces", "  leading and trailing  "},
		{"long", strings.Repeat("Storix", 40)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := HashPasswordWith(tc.password, testParams())
			if err != nil {
				t.Fatalf("HashPasswordWith: %v", err)
			}
			if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
				t.Fatalf("unexpected PHC prefix: %q", encoded)
			}
			fields := strings.Split(encoded, "$")
			if len(fields) != 6 {
				t.Fatalf("PHC string has %d fields, want 6: %q", len(fields), encoded)
			}
			// Only the salt and key fields are base64; the parameter fields
			// legitimately contain "=" signs.
			for _, f := range fields[4:] {
				if strings.Contains(f, "=") {
					t.Fatalf("base64 padding leaked into %q", encoded)
				}
			}
			if strings.Contains(encoded, tc.password) {
				t.Fatal("hash contains the plaintext password")
			}
			ok, err := VerifyPassword(encoded, tc.password)
			if err != nil {
				t.Fatalf("VerifyPassword: %v", err)
			}
			if !ok {
				t.Fatal("correct password was rejected")
			}
		})
	}
}

func TestHashPasswordSaltsEveryHash(t *testing.T) {
	t.Parallel()
	first, err := HashPasswordWith("same-password-twice", testParams())
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := HashPasswordWith("same-password-twice", testParams())
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical, salt is not random")
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	encoded, err := HashPasswordWith("the-real-password", testParams())
	if err != nil {
		t.Fatalf("HashPasswordWith: %v", err)
	}
	cases := []struct {
		name     string
		password string
	}{
		{"different", "the-fake-password"},
		{"empty", ""},
		{"prefix", "the-real-passwor"},
		{"suffix", "the-real-passwordd"},
		{"case", "The-Real-Password"},
		{"trailing space", "the-real-password "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassword(encoded, tc.password)
			if err != nil {
				t.Fatalf("VerifyPassword returned an error for a valid hash: %v", err)
			}
			if ok {
				t.Fatalf("password %q was accepted", tc.password)
			}
		})
	}
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	t.Parallel()
	good, err := HashPasswordWith("anchor-password", testParams())
	if err != nil {
		t.Fatalf("HashPasswordWith: %v", err)
	}
	parts := strings.Split(good, "$")

	cases := []struct {
		name    string
		encoded string
		want    error
	}{
		{"empty", "", ErrInvalidHash},
		{"plain text", "hunter2", ErrInvalidHash},
		{"too few fields", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA", ErrInvalidHash},
		{"too many fields", good + "$extra", ErrInvalidHash},
		{"no leading separator", strings.TrimPrefix(good, "$"), ErrInvalidHash},
		{"wrong algorithm", "$argon2i$" + strings.Join(parts[2:], "$"), ErrUnsupportedHash},
		{"bcrypt", "$2y$10$abcdefghijklmnopqrstuv", ErrInvalidHash},
		{"bad version field", "$argon2id$version=19$" + strings.Join(parts[3:], "$"), ErrInvalidHash},
		{"unknown version", "$argon2id$v=16$" + strings.Join(parts[3:], "$"), ErrUnsupportedHash},
		{"bad params", "$argon2id$v=19$m=lots,t=3,p=4$" + strings.Join(parts[4:], "$"), ErrInvalidHash},
		{"zero params", "$argon2id$v=19$m=0,t=0,p=0$" + strings.Join(parts[4:], "$"), ErrInvalidHash},
		{"bad salt base64", strings.Join(parts[:4], "$") + "$!!!!$" + parts[5], ErrInvalidHash},
		{"empty salt", strings.Join(parts[:4], "$") + "$$" + parts[5], ErrInvalidHash},
		{"bad key base64", strings.Join(parts[:5], "$") + "$!!!!", ErrInvalidHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassword(tc.encoded, "anchor-password")
			if ok {
				t.Fatal("a malformed hash verified successfully")
			}
			if err != tc.want {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if !NeedsRehash(tc.encoded, DefaultParams()) {
				t.Fatal("NeedsRehash should be true for a hash that cannot be parsed")
			}
		})
	}
}

func TestHashPasswordEmpty(t *testing.T) {
	t.Parallel()
	if _, err := HashPassword(""); err != ErrEmptyPassword {
		t.Fatalf("HashPassword(\"\") error = %v, want %v", err, ErrEmptyPassword)
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()
	weak := Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	strong := Params{Memory: 64 * 1024, Time: 3, Threads: 1, SaltLen: 16, KeyLen: 32}

	weakHash, err := HashPasswordWith("policy-check", weak)
	if err != nil {
		t.Fatalf("weak hash: %v", err)
	}
	strongHash, err := HashPasswordWith("policy-check", strong)
	if err != nil {
		t.Fatalf("strong hash: %v", err)
	}
	if !NeedsRehash(weakHash, strong) {
		t.Fatal("a weaker hash should need rehashing against stronger settings")
	}
	if NeedsRehash(strongHash, strong) {
		t.Fatal("a hash at the current settings should not need rehashing")
	}
	if NeedsRehash(strongHash, weak) {
		t.Fatal("a stronger hash should not be downgraded")
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		password string
		want     error
	}{
		{"good", "kifla-burek-2026", nil},
		{"exactly minimum", "abcd1234", nil},
		{"empty", "", ErrPasswordBlank},
		{"whitespace only", "        ", ErrPasswordBlank},
		{"tabs only", "\t\t\t\t\t\t\t\t\t", ErrPasswordBlank},
		{"too short", "abc123", ErrPasswordTooShort},
		{"common", "password", ErrPasswordCommon},
		{"common mixed case", "PassWord123", ErrPasswordCommon},
		{"common product name", "storix", ErrPasswordTooShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePassword(tc.password); err != tc.want {
				t.Fatalf("ValidatePassword(%q) = %v, want %v", tc.password, err, tc.want)
			}
		})
	}
}

func TestPasswordScore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		password string
		min      int
		max      int
	}{
		{"empty", "", 0, 0},
		{"common", "password", 0, 0},
		{"short", "ab1", 0, 1},
		{"repeated", "aaaaaaaaaaaaaaaaaaaa", 0, 1},
		{"lowercase only", "sarajevozima", 1, 2},
		{"mixed", "Sarajevo2026", 2, 4},
		{"strong", "Sarajevo-Zima-2026!", 4, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PasswordScore(tc.password)
			if got < 0 || got > 4 {
				t.Fatalf("score %d is outside the 0..4 range", got)
			}
			if got < tc.min || got > tc.max {
				t.Fatalf("PasswordScore(%q) = %d, want between %d and %d", tc.password, got, tc.min, tc.max)
			}
		})
	}
}

func TestTokenHelpers(t *testing.T) {
	t.Parallel()
	tok, err := GenerateToken(32)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token is not raw URL safe base64: %q", tok)
	}
	other, err := GenerateToken(32)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if tok == other {
		t.Fatal("two generated tokens are identical")
	}

	digest := HashToken(tok)
	if len(digest) != 64 {
		t.Fatalf("HashToken length = %d, want 64", len(digest))
	}
	if digest != HashToken(tok) {
		t.Fatal("HashToken is not deterministic")
	}
	if digest == HashToken(other) {
		t.Fatal("different tokens produced the same digest")
	}

	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		s := ShareToken()
		if len([]rune(s)) != ShareTokenLength {
			t.Fatalf("ShareToken length = %d, want %d", len([]rune(s)), ShareTokenLength)
		}
		if strings.ContainsAny(s, "0O1lI") {
			t.Fatalf("share token %q contains an ambiguous character", s)
		}
		seen[s] = struct{}{}
	}
	if len(seen) < 60 {
		t.Fatalf("only %d unique share tokens out of 64, entropy looks wrong", len(seen))
	}
}

// TestTOTPRFC6238 checks the SHA1 vectors from appendix B of RFC 6238. The
// published values are eight digits; Storix emits six, so the expectation is
// the low six digits of each vector.
func TestTOTPRFC6238(t *testing.T) {
	t.Parallel()
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tc := range cases {
		at := time.Unix(tc.unix, 0).UTC()
		t.Run(at.Format(time.RFC3339), func(t *testing.T) {
			t.Parallel()
			got, err := Code(secret, at)
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Code at %d = %s, want %s", tc.unix, got, tc.want)
			}
			if !Verify(secret, got, at, 0) {
				t.Fatal("Verify rejected the code it just generated")
			}
		})
	}
}

func TestTOTPVerify(t *testing.T) {
	t.Parallel()
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d, want 32 base32 characters", len(secret))
	}
	if strings.Contains(secret, "=") {
		t.Fatalf("secret carries base32 padding: %q", secret)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := Code(secret, now)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}

	cases := []struct {
		name  string
		code  string
		at    time.Time
		skew  int
		valid bool
	}{
		{"current step", code, now, 1, true},
		{"one step early", code, now.Add(-TOTPPeriod * time.Second), 1, true},
		{"one step late", code, now.Add(TOTPPeriod * time.Second), 1, true},
		{"outside skew", code, now.Add(3 * TOTPPeriod * time.Second), 1, false},
		{"no skew allowed", code, now.Add(TOTPPeriod * time.Second), 0, false},
		{"wrong code", "000000", now, 1, false},
		{"empty code", "", now, 1, false},
		{"short code", "12345", now, 1, false},
		{"long code", code + "0", now, 1, false},
		{"negative skew is absolute", code, now.Add(-TOTPPeriod * time.Second), -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Verify(secret, tc.code, tc.at, tc.skew); got != tc.valid {
				t.Fatalf("Verify(%q) = %v, want %v", tc.code, got, tc.valid)
			}
		})
	}

	if Verify("not base32 !!", code, now, 1) {
		t.Fatal("Verify accepted a code against an unparsable secret")
	}
	if _, err := Code("not base32 !!", now); err != ErrInvalidSecret {
		t.Fatalf("Code with a bad secret returned %v, want %v", err, ErrInvalidSecret)
	}
	// Users paste secrets in the grouped, lower case form the setup screen
	// shows, so that form has to work too.
	grouped := strings.ToLower(secret[:4] + " " + secret[4:8] + "-" + secret[8:])
	if !Verify(grouped, code, now, 1) {
		t.Fatal("Verify rejected a grouped lower case form of the same secret")
	}
}

func TestProvisioningURI(t *testing.T) {
	t.Parallel()
	uri := ProvisioningURI("JBSWY3DPEHPK3PXP", "amir@example.com", "Storix")
	want := "otpauth://totp/Storix:amir@example.com" +
		"?secret=JBSWY3DPEHPK3PXP&issuer=Storix&algorithm=SHA1&digits=6&period=30"
	if uri != want {
		t.Fatalf("ProvisioningURI = %q, want %q", uri, want)
	}

	spaced := ProvisioningURI("JBSWY3DPEHPK3PXP", "file admin", "X Project")
	if strings.Contains(spaced, " ") {
		t.Fatalf("provisioning URI was not escaped: %q", spaced)
	}
	if !strings.Contains(spaced, "issuer=X+Project") {
		t.Fatalf("issuer parameter missing from %q", spaced)
	}

	noIssuer := ProvisioningURI("JBSWY3DPEHPK3PXP", "amir", "")
	if strings.Contains(noIssuer, "issuer=") {
		t.Fatalf("issuer parameter should be absent: %q", noIssuer)
	}
	if want := "otpauth://totp/amir?secret=JBSWY3DPEHPK3PXP&algorithm=SHA1&digits=6&period=30"; noIssuer != want {
		t.Fatalf("ProvisioningURI without an issuer = %q, want %q", noIssuer, want)
	}
}

func TestRecoveryCodes(t *testing.T) {
	t.Parallel()
	codes, err := RecoveryCodes(10)
	if err != nil {
		t.Fatalf("RecoveryCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("got %d codes, want 10", len(codes))
	}
	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		groups := strings.Split(c, "-")
		if len(groups) != RecoveryCodeGroups {
			t.Fatalf("code %q has %d groups, want %d", c, len(groups), RecoveryCodeGroups)
		}
		for _, g := range groups {
			if len(g) != 5 {
				t.Fatalf("group %q in %q is %d characters, want 5", g, c, len(g))
			}
			if strings.ContainsAny(g, "0O1lI") {
				t.Fatalf("code %q contains an ambiguous character", c)
			}
		}
		if _, dup := seen[c]; dup {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = struct{}{}
	}
	if _, err := RecoveryCodes(0); err == nil {
		t.Fatal("RecoveryCodes(0) should fail")
	}
}

// newTestLimiter builds a limiter driven by a caller supplied clock. The
// clock is installed under the mutex because the janitor reads it there.
func newTestLimiter(limit int, window time.Duration, clock func() time.Time) *Limiter {
	l := NewLimiter(limit, window)
	l.mu.Lock()
	l.now = clock
	l.mu.Unlock()
	return l
}

func TestLimiterAllowAndExpiry(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0).UTC()
	now := base
	l := newTestLimiter(3, time.Minute, func() time.Time { return now })
	defer l.Close()

	const key = "203.0.113.10"
	for i := 0; i < 3; i++ {
		if !l.Allow(key) {
			t.Fatalf("attempt %d was blocked while inside the limit", i+1)
		}
		if got, want := l.Remaining(key), 2-i; got != want {
			t.Fatalf("Remaining after attempt %d = %d, want %d", i+1, got, want)
		}
	}
	if l.Allow(key) {
		t.Fatal("the fourth attempt inside the window should have been blocked")
	}
	if got := l.Remaining(key); got != 0 {
		t.Fatalf("Remaining = %d, want 0", got)
	}
	if got, want := l.RetryAfter(key), time.Minute; got != want {
		t.Fatalf("RetryAfter = %v, want %v", got, want)
	}

	// A different key has its own budget.
	if !l.Allow("198.51.100.4") {
		t.Fatal("an unrelated key was blocked")
	}

	// Halfway through the window nothing has aged out yet.
	now = base.Add(30 * time.Second)
	if l.Allow(key) {
		t.Fatal("the key should still be blocked halfway through the window")
	}
	if got, want := l.RetryAfter(key), 30*time.Second; got != want {
		t.Fatalf("RetryAfter halfway = %v, want %v", got, want)
	}

	// Past the window the oldest events are gone and the key is usable again.
	now = base.Add(time.Minute + time.Second)
	if got := l.Remaining(key); got != 3 {
		t.Fatalf("Remaining after the window slid = %d, want 3", got)
	}
	if got := l.RetryAfter(key); got != 0 {
		t.Fatalf("RetryAfter after the window slid = %v, want 0", got)
	}
	if !l.Allow(key) {
		t.Fatal("the key should be usable again after the window slid")
	}
}

func TestLimiterObserveAndReset(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	l := newTestLimiter(2, time.Minute, func() time.Time { return now })
	defer l.Close()

	const key = "user:amir"
	l.Observe(key)
	l.Observe(key)
	if l.Allow(key) {
		t.Fatal("observed events should count against the limit")
	}
	// Observing while blocked must not grow the record without bound.
	for i := 0; i < 50; i++ {
		l.Observe(key)
	}
	if got := l.Remaining(key); got != 0 {
		t.Fatalf("Remaining = %d, want 0", got)
	}

	l.Reset(key)
	if got := l.Remaining(key); got != 2 {
		t.Fatalf("Remaining after Reset = %d, want 2", got)
	}
	if !l.Allow(key) {
		t.Fatal("the key should be usable after Reset")
	}
}

func TestLimiterConcurrent(t *testing.T) {
	t.Parallel()
	l := NewLimiter(100, time.Minute)
	defer l.Close()

	const workers = 16
	const each = 25
	done := make(chan int, workers)
	for w := 0; w < workers; w++ {
		go func() {
			allowed := 0
			for i := 0; i < each; i++ {
				if l.Allow("shared") {
					allowed++
				}
				l.Remaining("shared")
				l.RetryAfter("shared")
			}
			done <- allowed
		}()
	}
	total := 0
	for w := 0; w < workers; w++ {
		total += <-done
	}
	if total != 100 {
		t.Fatalf("allowed %d events, want exactly the limit of 100", total)
	}
}

func TestLimiterCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	l := NewLimiter(1, time.Millisecond)
	l.Close()
	l.Close()
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	trusted := []string{"10.0.0.0/8", "127.0.0.1", "2001:db8::/32"}

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		proxies []string
		want    string
	}{
		{
			name:    "no proxy configured ignores forwarded headers",
			remote:  "203.0.113.9:44321",
			headers: map[string]string{forwardedForHeader: "1.2.3.4"},
			want:    "203.0.113.9",
		},
		{
			name:    "untrusted peer cannot spoof x-real-ip",
			remote:  "203.0.113.9:44321",
			headers: map[string]string{realIPHeader: "10.0.0.1"},
			proxies: trusted,
			want:    "203.0.113.9",
		},
		{
			name:    "peer outside the trusted range is used as is",
			remote:  "198.51.100.20:1234",
			headers: map[string]string{forwardedForHeader: "203.0.113.7"},
			proxies: trusted,
			want:    "198.51.100.20",
		},
		{
			name:    "trusted proxy forwarded header is honoured",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7"},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "trusted chain walks back to the first untrusted hop",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7, 10.1.2.9, 10.1.2.8"},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "spoofed prefix ahead of the real client is ignored",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "1.1.1.1, 203.0.113.7, 10.1.2.9"},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "entirely trusted chain falls back to the peer",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "10.0.0.5, 10.0.0.6"},
			proxies: trusted,
			want:    "10.1.2.3",
		},
		{
			name:    "x-real-ip used when there is no forwarded header",
			remote:  "127.0.0.1:8080",
			headers: map[string]string{realIPHeader: "198.51.100.4"},
			proxies: trusted,
			want:    "198.51.100.4",
		},
		{
			name:   "forwarded header wins over x-real-ip",
			remote: "127.0.0.1:8080",
			headers: map[string]string{
				forwardedForHeader: "203.0.113.7",
				realIPHeader:       "198.51.100.4",
			},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "forwarded entries carrying ports are accepted",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7:19999"},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "unparsable forwarded entries are skipped",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7, unknown, "},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "empty forwarded header falls back to the peer",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "   "},
			proxies: trusted,
			want:    "10.1.2.3",
		},
		{
			name:    "ipv6 peer inside a trusted prefix",
			remote:  "[2001:db8::1]:443",
			headers: map[string]string{forwardedForHeader: "198.51.100.5"},
			proxies: trusted,
			want:    "198.51.100.5",
		},
		{
			name:    "ipv6 client behind a trusted proxy",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "2001:db9::99"},
			proxies: trusted,
			want:    "2001:db9::99",
		},
		{
			name:    "ipv4 mapped peer matches its ipv4 prefix",
			remote:  "[::ffff:10.1.2.3]:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7"},
			proxies: trusted,
			want:    "203.0.113.7",
		},
		{
			name:    "bare trusted address entry",
			remote:  "127.0.0.1:9000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7"},
			proxies: []string{"127.0.0.1"},
			want:    "203.0.113.7",
		},
		{
			name:    "malformed proxy entries are ignored not fatal",
			remote:  "10.1.2.3:5000",
			headers: map[string]string{forwardedForHeader: "203.0.113.7"},
			proxies: []string{"", "nonsense", "10.0.0.0/8"},
			want:    "203.0.113.7",
		},
		{
			name:   "remote address without a port",
			remote: "203.0.113.9",
			want:   "203.0.113.9",
		},
		{
			name:   "unparsable remote address is returned untouched",
			remote: "@",
			want:   "@",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := ClientIP(r, tc.proxies); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPNilRequest(t *testing.T) {
	t.Parallel()
	if got := ClientIP(nil, nil); got != "" {
		t.Fatalf("ClientIP(nil) = %q, want empty", got)
	}
}

func TestClientIPRepeatedForwardedHeaders(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r.RemoteAddr = "10.1.2.3:5000"
	r.Header.Add(forwardedForHeader, "203.0.113.7")
	r.Header.Add(forwardedForHeader, "10.1.2.9")
	if got := ClientIP(r, []string{"10.0.0.0/8"}); got != "203.0.113.7" {
		t.Fatalf("ClientIP = %q, want 203.0.113.7", got)
	}
}
