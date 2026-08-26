// Package web serves the compiled single page application that ships inside
// the Storix binary.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package web

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// ErrNotBuilt is returned when the binary was compiled without the frontend.
var ErrNotBuilt = errors.New("web: the interface has not been built into this binary")

// Assets returns the embedded file system rooted at the build output.
func Assets() (fs.FS, error) { return fs.Sub(dist, "dist") }

// Built reports whether a real build is embedded.
func Built() bool {
	sub, err := Assets()
	if err != nil {
		return false
	}
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return false
	}
	return !strings.Contains(string(data), "storix-placeholder")
}

// Handler serves the application. Hashed assets are cached forever, the
// document is never cached, and unknown paths fall back to index.html so
// client side routing works on a hard refresh.
//
// When dir is not empty the files are served from disk instead, which is what
// the development build uses.
func Handler(dir string) (http.Handler, error) {
	var files fs.FS
	if dir != "" {
		if _, err := os.Stat(path.Join(dir, "index.html")); err != nil {
			return nil, err
		}
		files = os.DirFS(dir)
	} else {
		sub, err := Assets()
		if err != nil {
			return nil, err
		}
		files = sub
	}

	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, ErrNotBuilt
	}
	fileServer := http.FileServerFS(files)
	start := time.Now()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}

		// The API namespace never reaches the file server.
		if strings.HasPrefix(upath, "api/") {
			http.NotFound(w, r)
			return
		}

		if f, err := files.Open(upath); err == nil {
			info, statErr := f.Stat()
			f.Close()
			if statErr == nil && !info.IsDir() {
				setCacheHeaders(w, upath)
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Anything else is a client route: hand back the document.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		http.ServeContent(w, r, "index.html", start, strings.NewReader(string(index)))
	}), nil
}

// setCacheHeaders marks fingerprinted assets as immutable.
func setCacheHeaders(w http.ResponseWriter, name string) {
	switch {
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasSuffix(name, ".html"), name == "manifest.webmanifest", name == "sw.js":
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	default:
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
}

// Placeholder renders a minimal page for a binary built without the frontend,
// so an operator still gets a clear message instead of a blank screen.
func Placeholder() http.Handler {
	const page = `<!doctype html><meta charset="utf-8"><title>Storix</title>
<style>body{background:#0B0F17;color:#9CA3AF;font:16px/1.6 system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0}
main{max-width:38rem;padding:2rem}h1{color:#fff;font-size:1.4rem;margin:0 0 .5rem}code{color:#00D4FF}</style>
<main><h1>Storix</h1><p>This binary was built without the web interface. Run <code>make build</code> from a full checkout to bundle it.</p>
<p>Developed by X Project.</p></main>`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, page)
	})
}
