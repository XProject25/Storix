// Programmatic access, from the outside: what a token can and cannot do, and
// what a drive mounted with one can and cannot do. Every case here is a request
// against a running server, because the point of these two features is what
// they let a caller reach, and that is only true at the edge.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// tokPassword is the administrator password every harness is set up with.
const tokPassword = "StorixIntegration1"

// mintToken creates a token as the signed in administrator and returns the
// secret, which the server shows exactly once.
func (h *harness) mintToken(name, scope, expiresIn string) (secret string, id int64) {
	h.t.Helper()
	var out struct {
		Token struct {
			ID    int64  `json:"id"`
			Scope string `json:"scope"`
		} `json:"token"`
		Secret string `json:"secret"`
	}
	h.decode(h.do(http.MethodPost, "/api/v1/auth/tokens", map[string]any{
		"name": name, "scope": scope, "expiresIn": expiresIn,
	}), http.StatusCreated, &out)
	if out.Secret == "" {
		h.t.Fatal("creating a token must return the secret once")
	}
	return out.Secret, out.Token.ID
}

// bearer issues a request carrying a token and no cookie at all, which is how a
// script calls Storix.
func (h *harness) bearer(method, path, token string, body io.Reader) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

// tokUser creates an account with an exact permission set and one mount on the
// files directory, so a test can say precisely what it may do.
func (h *harness) tokUser(name string, perms []store.Permission) {
	h.t.Helper()
	hash, err := auth.HashPassword(tokPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	user := &store.User{
		Username:     name,
		PasswordHash: hash,
		Role:         store.RoleCustom,
		Permissions:  perms,
		Active:       true,
		Mounts:       []store.Mount{{Path: vfs.Clean(h.filesDir), Label: "Files"}},
	}
	if _, err := h.store.CreateUser(h.t.Context(), user); err != nil {
		h.t.Fatal(err)
	}
}

// davSlugs lists every collection name the drive exposes to one account.
func (h *harness) davSlugs(user, pass string) []string {
	h.t.Helper()
	resp := h.davRequest("PROPFIND", "/dav/", user, pass,
		strings.NewReader(davPropfindAll), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		h.t.Fatalf("PROPFIND on the root gave %d: %s", resp.StatusCode, truncate(payload))
	}
	out := make([]string, 0, 4)
	for _, marker := range []string{"<D:href>", "<d:href>"} {
		for _, part := range strings.Split(string(payload), marker)[1:] {
			href := part
			if idx := strings.IndexByte(href, '<'); idx >= 0 {
				href = href[:idx]
			}
			if name := strings.Trim(strings.TrimPrefix(href, "/dav"), "/"); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// A read scoped token narrows the account to browsing and downloading, and the
// secret is handed over once and never again.
func TestTokenScopeNarrowsTheAccount(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	secret, _ := h.mintToken("reader", "read", "never")
	if !strings.HasPrefix(secret, "sxp_") {
		t.Errorf("secret = %q, want the sxp_ tag so a leaked token is recognisable", secret)
	}

	list := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, secret, nil)
	list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("a read token must be able to list, got %d", list.StatusCode)
	}

	write := h.bearer(http.MethodPost, "/api/v1/fs/mkdir", secret,
		strings.NewReader(`{"path":"`+root+`","name":"from-a-read-token"}`))
	write.Body.Close()
	if write.StatusCode != http.StatusForbidden {
		t.Errorf("a read token creating a folder gave %d, want 403", write.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "from-a-read-token")); err == nil {
		t.Error("a read token created a folder")
	}

	// The listing screen shows the prefix and never the secret or its digest.
	payload, err := io.ReadAll(h.do(http.MethodGet, "/api/v1/auth/tokens", nil).Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	if strings.Contains(body, secret) || strings.Contains(body, strings.TrimPrefix(secret, "sxp_")) {
		t.Error("the token listing carries the secret")
	}
	if strings.Contains(body, auth.HashToken(secret)) || strings.Contains(body, `"hash"`) {
		t.Error("the token listing carries the stored digest")
	}
}

// Nothing in an Authorization header may authenticate anything or bring the
// server down, however it is shaped.
func TestTokenRefusesMalformedCredentials(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)
	secret, _ := h.mintToken("shapes", "write", "never")

	headers := []string{
		"",
		"Basic",
		"Basic ",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("no-colon-at-all")),
		"Basic " + base64.StdEncoding.EncodeToString([]byte(":")),
		"Basic !!!! not base64 !!!!",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("ad:min:"+secret)),
		"Bearer",
		"Bearer ",
		"bearer sxp_",
		"BEARER sxp__",
		"sxp_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 32),
		"Bearer sxp_" + strings.Repeat("a", 8) + "x" + strings.Repeat("b", 32),
		"Bearer " + strings.Repeat("z", 200000),
		// A prefix of eight characters that are not eight bytes.
		"Bearer sxp_" + strings.Repeat("é", 8) + "_" + strings.Repeat("b", 32),
		// The right prefix with the wrong secret, which is the case the
		// comparison has to answer without leaking which character differed.
		"Bearer sxp_" + secret[4:12] + "_" + strings.Repeat("b", 32),
	}
	for _, header := range headers {
		req, err := http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/fs/list?path="+root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatalf("header %.40q: %v", header, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("header %.40q gave %d, want 401", header, resp.StatusCode)
		}
	}

	// The server is still standing and the real token still works.
	ok := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, secret, nil)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("the valid token gave %d after the malformed ones, want 200", ok.StatusCode)
	}
}

// A token whose day has passed is refused even though its secret is right.
func TestTokenRefusesAnExpiredToken(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	admin, err := h.store.GetUserByName(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 32)
	past := time.Now().Add(-time.Hour).UTC()
	if _, err := h.store.CreateToken(t.Context(), &store.APIToken{
		UserID: admin.ID, Name: "yesterday", Prefix: "expired1",
		Hash: auth.HashToken(secret), Scope: store.ScopeWrite, ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	dead := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, "sxp_expired1_"+secret, nil)
	dead.Body.Close()
	if dead.StatusCode != http.StatusUnauthorized {
		t.Errorf("an expired token gave %d, want 401", dead.StatusCode)
	}

	// The same token with a future expiry proves it was the date that refused
	// it and not the secret.
	future := time.Now().Add(time.Hour).UTC()
	if _, err := h.store.CreateToken(t.Context(), &store.APIToken{
		UserID: admin.ID, Name: "tomorrow", Prefix: "current1",
		Hash: auth.HashToken(secret), Scope: store.ScopeWrite, ExpiresAt: &future,
	}); err != nil {
		t.Fatal(err)
	}
	live := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, "sxp_current1_"+secret, nil)
	live.Body.Close()
	if live.StatusCode != http.StatusOK {
		t.Errorf("a token expiring in an hour gave %d, want 200", live.StatusCode)
	}
}

// The last used stamp is written back once and then left alone, and one server
// throttling its tokens must not silence another one's.
func TestTokenRecordsUseOncePerServer(t *testing.T) {
	root := ""
	for _, run := range []string{"first", "second"} {
		h := newHarness(t)
		h.setup()
		root = vfs.Clean(h.filesDir)
		secret, id := h.mintToken("worker", "read", "never")

		for i := 0; i < 3; i++ {
			resp := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, secret, nil)
			resp.Body.Close()
		}
		// The write happens off the request goroutine.
		deadline := time.Now().Add(5 * time.Second)
		var used *store.APIToken
		for time.Now().Before(deadline) {
			tokens, err := h.store.ListTokens(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, tok := range tokens {
				if tok.ID == id && tok.LastUsedAt != nil {
					used = tok
				}
			}
			if used != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if used == nil {
			t.Fatalf("%s server: the token was used three times and never recorded it", run)
		}
		if used.LastUsedIP == "" {
			t.Errorf("%s server: the address the token was used from was not recorded", run)
		}
		first := *used.LastUsedAt

		// Three more calls inside the minute must not write again.
		for i := 0; i < 3; i++ {
			resp := h.bearer(http.MethodGet, "/api/v1/fs/list?path="+root, secret, nil)
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
		tokens, err := h.store.ListTokens(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range tokens {
			if tok.ID == id && tok.LastUsedAt != nil && !tok.LastUsedAt.Equal(first) {
				t.Errorf("%s server: the stamp moved to %v inside the throttle window", run, tok.LastUsedAt)
			}
		}
	}
}

// A read scoped token mounts read only, whatever the account may otherwise do,
// and that has to hold for every method a drive can write with.
func TestWebDAVReadTokenCannotWrite(t *testing.T) {
	h := newHarness(t)
	h.setup()
	secret, _ := h.mintToken("drive", "read", "never")
	slugs := h.davSlugs("admin", secret)
	if len(slugs) == 0 {
		t.Fatal("the drive listed no collections for a read token")
	}
	slug := slugs[0]

	lockBody := `<?xml version="1.0" encoding="utf-8" ?><D:lockinfo xmlns:D="DAV:">` +
		`<D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`

	cases := []struct {
		method  string
		target  string
		body    string
		headers map[string]string
		file    string
	}{
		{method: http.MethodPut, target: "/dav/" + slug + "/put.txt", body: "no", file: "put.txt"},
		{method: "MKCOL", target: "/dav/" + slug + "/made", file: "made"},
		{method: "LOCK", target: "/dav/" + slug + "/locked.txt", body: lockBody,
			headers: map[string]string{"Content-Type": "application/xml"}, file: "locked.txt"},
		{method: http.MethodDelete, target: "/dav/" + slug + "/projects"},
		{method: "MOVE", target: "/dav/" + slug + "/projects",
			headers: map[string]string{"Destination": h.server.URL + "/dav/" + slug + "/gone"}},
		{method: "COPY", target: "/dav/" + slug + "/projects",
			headers: map[string]string{"Destination": h.server.URL + "/dav/" + slug + "/clone"}, file: "clone"},
		// The collection above the mounts is not a folder anyone can change.
		{method: "MKCOL", target: "/dav/invented"},
		{method: http.MethodPut, target: "/dav/loose.txt", body: "no"},
	}
	for _, c := range cases {
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		resp := h.davRequest(c.method, c.target, "admin", secret, body, c.headers)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a read token gave %d, want 403", c.method, c.target, resp.StatusCode)
		}
		if c.file != "" {
			if _, err := os.Stat(filepath.Join(h.filesDir, c.file)); err == nil {
				t.Errorf("%s %s created %s on disk", c.method, c.target, c.file)
			}
		}
	}
	// Nothing above was allowed to remove the folder the harness created.
	if _, err := os.Stat(filepath.Join(h.filesDir, "projects")); err != nil {
		t.Errorf("a read token removed a folder: %v", err)
	}
}

// The drive is a second door onto the same files. An account that may not
// delete in the browser may not delete through the drive either.
func TestWebDAVHonoursAccountPermissions(t *testing.T) {
	h := newHarness(t)
	h.setup()
	// May add files, may never delete, rename, move or copy.
	h.tokUser("uploader", []store.Permission{store.PermView, store.PermDownload, store.PermUpload, store.PermCreate})

	for _, name := range []string{"keep.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(h.filesDir, name), []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	slugs := h.davSlugs("uploader", tokPassword)
	if len(slugs) == 0 {
		t.Fatal("the drive listed no collections for the account")
	}
	slug := slugs[0]

	// What the account does hold still works, so the test is about the
	// permission and not about the drive being broken.
	put := h.davRequest(http.MethodPut, "/dav/"+slug+"/added.txt", "uploader", tokPassword,
		strings.NewReader("added over the drive"), nil)
	put.Body.Close()
	if put.StatusCode >= 400 {
		t.Fatalf("an account holding upload could not PUT: %d", put.StatusCode)
	}
	mkcol := h.davRequest("MKCOL", "/dav/"+slug+"/added-folder", "uploader", tokPassword, nil, nil)
	mkcol.Body.Close()
	if mkcol.StatusCode >= 400 {
		t.Fatalf("an account holding create could not MKCOL: %d", mkcol.StatusCode)
	}

	del := h.davRequest(http.MethodDelete, "/dav/"+slug+"/keep.txt", "uploader", tokPassword, nil, nil)
	del.Body.Close()
	if del.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE without the delete permission gave %d, want 403", del.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "keep.txt")); err != nil {
		t.Errorf("the file was deleted by an account that may not delete: %v", err)
	}

	move := h.davRequest("MOVE", "/dav/"+slug+"/keep.txt", "uploader", tokPassword, nil, map[string]string{
		"Destination": h.server.URL + "/dav/" + slug + "/elsewhere/keep.txt", "Overwrite": "F",
	})
	move.Body.Close()
	if move.StatusCode != http.StatusForbidden {
		t.Errorf("MOVE without the move permission gave %d, want 403", move.StatusCode)
	}

	rename := h.davRequest("MOVE", "/dav/"+slug+"/keep.txt", "uploader", tokPassword, nil, map[string]string{
		"Destination": h.server.URL + "/dav/" + slug + "/renamed.txt", "Overwrite": "F",
	})
	rename.Body.Close()
	if rename.StatusCode != http.StatusForbidden {
		t.Errorf("MOVE as a rename without the rename permission gave %d, want 403", rename.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "renamed.txt")); err == nil {
		t.Error("the file was renamed by an account that may not rename")
	}

	copied := h.davRequest("COPY", "/dav/"+slug+"/second.txt", "uploader", tokPassword, nil, map[string]string{
		"Destination": h.server.URL + "/dav/" + slug + "/second-copy.txt", "Overwrite": "F",
	})
	copied.Body.Close()
	if copied.StatusCode != http.StatusForbidden {
		t.Errorf("COPY without the copy permission gave %d, want 403", copied.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "second-copy.txt")); err == nil {
		t.Error("the file was copied by an account that may not copy")
	}
}

// A drive write counts against the storage allowance the same way a browser
// upload does, and a transfer the allowance stops leaves nothing behind.
func TestWebDAVKeepsTheStorageAllowance(t *testing.T) {
	h := newHarness(t)
	h.setup()

	admin, err := h.store.GetUserByName(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.Quota = 32 << 10
	if err := h.store.UpdateUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	slugs := h.davSlugs("admin", tokPassword)
	if len(slugs) == 0 {
		t.Fatal("the drive listed no collections")
	}
	slug := slugs[0]

	small := strings.Repeat("s", 1024)
	fits := h.davRequest(http.MethodPut, "/dav/"+slug+"/fits.txt", "admin", tokPassword,
		strings.NewReader(small), nil)
	fits.Body.Close()
	if fits.StatusCode >= 400 {
		t.Fatalf("a write inside the allowance gave %d", fits.StatusCode)
	}
	if info, err := os.Stat(filepath.Join(h.filesDir, "fits.txt")); err != nil || info.Size() != int64(len(small)) {
		t.Fatalf("the small file did not land whole: %v", err)
	}

	over := h.davRequest(http.MethodPut, "/dav/"+slug+"/over.bin", "admin", tokPassword,
		strings.NewReader(strings.Repeat("o", 512<<10)), nil)
	over.Body.Close()
	if over.StatusCode < 400 {
		t.Errorf("a write far past the allowance gave %d, want a refusal", over.StatusCode)
	}
	if info, err := os.Stat(filepath.Join(h.filesDir, "over.bin")); err == nil {
		t.Errorf("a refused transfer left %d bytes behind", info.Size())
	}
}

// Two mounts whose labels reduce to the same name are told apart, and both stay
// reachable: a mount that is listed and cannot be opened is worse than one that
// was never listed.
func TestWebDAVNamesEveryMountReachably(t *testing.T) {
	h := newHarness(t)
	h.setup()

	first := filepath.Join(h.filesDir, "first")
	second := filepath.Join(h.filesDir, "second")
	third := filepath.Join(h.filesDir, "third")
	contents := map[string]string{
		first:  "the first mount",
		second: "the second mount",
		third:  "the third mount",
	}
	for dir, body := range contents {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "which.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hash, err := auth.HashPassword(tokPassword)
	if err != nil {
		t.Fatal(err)
	}
	// The two labels are different strings that fold onto one another, which is
	// what a case insensitive lookup treats as the same name.
	user := &store.User{
		Username: "twomounts", PasswordHash: hash, Role: store.RoleCustom,
		Permissions: []store.Permission{store.PermView, store.PermDownload},
		Active:      true,
		Mounts: []store.Mount{
			{Path: vfs.Clean(first), Label: "ſecret"},
			{Path: vfs.Clean(second), Label: "secret"},
			// A label that is longer than the name the drive hands out, in a
			// script whose characters are more than one byte each.
			{Path: vfs.Clean(third), Label: strings.Repeat("あ", 30)},
		},
	}
	if _, err := h.store.CreateUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}

	slugs := h.davSlugs("twomounts", tokPassword)
	if len(slugs) != len(contents) {
		t.Fatalf("the drive listed %v, want one collection per mount", slugs)
	}
	seen := map[string]bool{}
	for _, slug := range slugs {
		resp := h.davRequest(http.MethodGet, "/dav/"+slug+"/which.txt", "twomounts", tokPassword, nil, nil)
		payload, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the listed collection %q could not be opened: %d", slug, resp.StatusCode)
		}
		seen[string(payload)] = true
	}
	for _, want := range contents {
		if !seen[want] {
			t.Errorf("no listed collection reached %q, a mount is listed and cannot be opened: %v", want, seen)
		}
	}
}

// A mounted drive opens several connections at once and puts its credentials on
// every one of them. Those are not sign in attempts, and a working mount must
// never throttle itself.
func TestWebDAVLetsAWorkingMountWorkInParallel(t *testing.T) {
	h := newHarness(t)
	h.setup()

	const parallel = 16
	codes := make(chan int, parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			resp := h.davRequest("PROPFIND", "/dav/", "admin", tokPassword,
				strings.NewReader(davPropfindAll),
				map[string]string{"Depth": "0", "Content-Type": "application/xml"})
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	for i := 0; i < parallel; i++ {
		if code := <-codes; code != http.StatusMultiStatus {
			t.Errorf("a correctly authenticated request in a burst of %d gave %d, want 207",
				parallel, code)
		}
	}
}

// Guessing is still slowed down, which is the reason the limiter is there.
func TestWebDAVSlowsDownGuessing(t *testing.T) {
	h := newHarness(t)
	h.setup()

	blocked := false
	for i := 0; i < 20; i++ {
		resp := h.davRequest(http.MethodGet, "/dav/", "admin", "not-the-password", nil, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			blocked = true
			break
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a wrong password gave %d, want 401", resp.StatusCode)
		}
	}
	if !blocked {
		t.Error("twenty wrong passwords in a row were never slowed down")
	}
}
