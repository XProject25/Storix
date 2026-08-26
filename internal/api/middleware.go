package api

import (
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/store"
)

// recoverer turns a panic into a 500 instead of killing the server.
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// A client that hangs up mid stream reaches us as
				// ErrAbortHandler; that is normal, let it through.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				a.Logger.Error("panic recovered",
					"path", r.URL.Path,
					"method", r.Method,
					"panic", rec,
					"stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": apiError(http.StatusInternalServerError, "internal", "Something went wrong on the server"),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies the baseline hardening headers. The policy is strict
// because the app ships its own assets and never loads third party code.
func (a *API) securityHeaders(next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'wasm-unsafe-eval'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"media-src 'self' blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// A browser ignores this header on a plain HTTP origin and logs a
		// console error for it. Most installs run on an address until a domain
		// is set, so only send it where it actually applies.
		if r.TLS != nil {
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
		}
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		// Raw file responses get their own policy in the download handler, so
		// a hosted HTML file can never execute in the app origin.
		if !strings.HasPrefix(r.URL.Path, "/api/v1/fs/raw") && !strings.HasPrefix(r.URL.Path, "/api/v1/fs/download") {
			h.Set("Content-Security-Policy", csp)
		}
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// statusWriter captures the response code for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach flushing and hijacking.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog records one line per request when enabled.
func (a *API) accessLog(next http.Handler) http.Handler {
	if !a.Config.Log.Access {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		a.Logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"ip", a.clientIP(r),
			"ms", time.Since(start).Milliseconds())
	})
}

// ipAllowlist refuses callers outside the configured networks.
func (a *API) ipAllowlist(next http.Handler) http.Handler {
	list := a.Config.Security.IPAllowlist
	if len(list) == 0 {
		return next
	}
	nets := make([]*net.IPNet, 0, len(list))
	var plain []net.IP
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, n)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			plain = append(plain, ip)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(a.clientIP(r))
		if ip != nil {
			for _, n := range nets {
				if n.Contains(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}
			for _, p := range plain {
				if p.Equal(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		a.Logger.Warn("blocked by ip allowlist", "ip", a.clientIP(r), "path", r.URL.Path)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": apiError(http.StatusForbidden, "ip_blocked", "This address is not allowed to reach Storix"),
		})
	})
}

// setupGate keeps the API closed until the first run wizard has completed.
func (a *API) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Store.SetupCompleted(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		allowed := p == "/api/v1/setup" ||
			p == "/api/v1/system/status" ||
			!strings.HasPrefix(p, "/api/")
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": apiError(http.StatusServiceUnavailable, "setup_required", "Storix has not been set up yet"),
		})
	})
}

// authenticate resolves the session cookie into an account. It never rejects;
// requireAuth does that, so public endpoints can still see who is calling.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := a.Session.Resolve(r.Context(), r)
		if err != nil || sess == nil {
			next.ServeHTTP(w, r)
			return
		}
		user, err := a.Store.GetUser(r.Context(), sess.UserID)
		if err != nil || user == nil || !user.Active {
			a.Session.Clear(w)
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, withUser(r, user, sess))
	})
}

// requireAuth refuses anonymous callers.
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			a.fail(w, r, errUnauthorized)
			return
		}
		next(w, r)
	}
}

// requirePerm refuses callers without a capability.
func (a *API) requirePerm(p store.Permission, next http.HandlerFunc) http.HandlerFunc {
	return a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).Can(p) {
			a.audit(r, "permission.denied", r.URL.Path, string(p), false)
			a.fail(w, r, errForbidden)
			return
		}
		next(w, r)
	})
}

// requireAdmin refuses everyone but administrators.
func (a *API) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			a.audit(r, "admin.denied", r.URL.Path, "", false)
			a.fail(w, r, errForbidden)
			return
		}
		next(w, r)
	})
}

// csrfGuard validates the double submit token on state changing requests.
func (a *API) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Public share endpoints are reachable without a session and are
		// protected by the unguessable token in the URL instead.
		if strings.HasPrefix(r.URL.Path, "/api/v1/public/") {
			next.ServeHTTP(w, r)
			return
		}
		sess := currentSession(r)
		if sess == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !a.Session.ValidateCSRF(r, sess) {
			a.fail(w, r, errCSRF)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noCache marks a response as never cacheable.
func noCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
}

// writeGuard applies a light rate limit to mutating API calls so a runaway
// client cannot hammer the disk.
func (a *API) writeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Chunked uploads legitimately issue a very high request rate.
		if strings.Contains(r.URL.Path, "/tus") {
			next.ServeHTTP(w, r)
			return
		}
		key := a.clientIP(r)
		if u := currentUser(r); u != nil {
			key = "u" + strings.TrimSpace(u.Username)
		}
		if !a.writeLimiter.Allow(key) {
			w.Header().Set("Retry-After", "5")
			a.fail(w, r, errRateLimited)
			return
		}
		next.ServeHTTP(w, r)
	})
}
