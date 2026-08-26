// Package server owns the HTTP listeners, TLS and graceful shutdown.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/XProject25/Storix/internal/config"
)

// Options configure the server.
type Options struct {
	Config  *config.Config
	Handler http.Handler
	Logger  *slog.Logger
}

// Server runs the Storix listeners.
type Server struct {
	cfg     *config.Config
	handler http.Handler
	log     *slog.Logger

	primary  *http.Server
	redirect *http.Server
}

// New builds a server from options.
func New(o Options) *Server {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: o.Config, handler: o.Handler, log: log}
}

// Run starts listening and blocks until the context is cancelled or a
// listener fails. Uploads and downloads run without a write timeout on
// purpose: a multi hour transfer must not be cut off by the server.
func (s *Server) Run(ctx context.Context) error {
	switch s.cfg.Server.TLS.Mode {
	case config.TLSACME:
		return s.runACME(ctx)
	case config.TLSManual:
		return s.runManualTLS(ctx)
	default:
		return s.runPlain(ctx)
	}
}

func (s *Server) newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 20 * time.Second,
		ReadTimeout:       s.cfg.Server.ReadTimeout.D(),
		WriteTimeout:      s.cfg.Server.WriteTimeout.D(),
		IdleTimeout:       s.cfg.Server.IdleTimeout.D(),
		ErrorLog:          nil,
	}
}

func (s *Server) runPlain(ctx context.Context) error {
	addr := s.cfg.Addr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", addr, err)
	}
	s.primary = s.newHTTPServer(addr, s.handler)
	s.log.Info("storix is listening", "address", s.cfg.PublicURL())
	return s.serve(ctx, func() error { return s.primary.Serve(ln) })
}

func (s *Server) runManualTLS(ctx context.Context) error {
	cert, err := tls.LoadX509KeyPair(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile)
	if err != nil {
		return fmt.Errorf("server: load certificate: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.TLS.HTTPSPort)
	s.primary = s.newHTTPServer(addr, s.handler)
	s.primary.TLSConfig = baseTLSConfig()
	s.primary.TLSConfig.Certificates = []tls.Certificate{cert}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", addr, err)
	}
	s.startRedirect(ctx)
	s.log.Info("storix is listening with TLS", "address", s.cfg.PublicURL())
	return s.serve(ctx, func() error { return s.primary.ServeTLS(ln, "", "") })
}

func (s *Server) runACME(ctx context.Context) error {
	domain := strings.TrimSpace(s.cfg.Server.Domain)
	if domain == "" {
		return errors.New("server: acme requires a domain")
	}
	if err := os.MkdirAll(s.cfg.Server.TLS.CacheDir, 0o700); err != nil {
		return fmt.Errorf("server: acme cache: %w", err)
	}
	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(s.cfg.Server.TLS.CacheDir),
		HostPolicy: autocert.HostWhitelist(hostVariants(domain)...),
		Email:      s.cfg.Server.TLS.Email,
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.TLS.HTTPSPort)
	s.primary = s.newHTTPServer(addr, s.handler)
	s.primary.TLSConfig = baseTLSConfig()
	s.primary.TLSConfig.GetCertificate = manager.GetCertificate
	s.primary.TLSConfig.NextProtos = append([]string{"h2", "http/1.1"}, s.primary.TLSConfig.NextProtos...)

	// Port 80 answers the ACME challenge and redirects everything else.
	challenge := manager.HTTPHandler(s.redirectHandler())
	httpAddr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.TLS.HTTPPort)
	s.redirect = s.newHTTPServer(httpAddr, challenge)
	go func() {
		if err := s.redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("http listener stopped", "address", httpAddr, "err", err)
		}
	}()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", addr, err)
	}
	s.log.Info("storix is listening with automatic TLS", "address", s.cfg.PublicURL(), "domain", domain)
	return s.serve(ctx, func() error { return s.primary.ServeTLS(ln, "", "") })
}

// startRedirect serves the plain port with a permanent redirect to HTTPS.
func (s *Server) startRedirect(ctx context.Context) {
	if !s.cfg.Server.TLS.Redirect {
		return
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.TLS.HTTPPort)
	s.redirect = s.newHTTPServer(addr, s.redirectHandler())
	go func() {
		if err := s.redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("redirect listener stopped", "address", addr, "err", err)
		}
	}()
}

func (s *Server) redirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		target := "https://" + host
		if s.cfg.Server.TLS.HTTPSPort != 443 {
			target = fmt.Sprintf("%s:%d", target, s.cfg.Server.TLS.HTTPSPort)
		}
		target += r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// serve runs the listener and shuts it down when the context ends.
func (s *Server) serve(ctx context.Context, run func() error) error {
	errCh := make(chan error, 1)
	go func() {
		err := run()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down")
		grace := s.cfg.Server.ShutdownGrace.D()
		if grace <= 0 {
			grace = 20 * time.Second
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if s.redirect != nil {
			_ = s.redirect.Shutdown(shutdownCtx)
		}
		if s.primary != nil {
			if err := s.primary.Shutdown(shutdownCtx); err != nil {
				// Force the remaining connections closed so a stuck transfer
				// cannot block the restart forever.
				_ = s.primary.Close()
				return nil
			}
		}
		return nil
	}
}

// baseTLSConfig is a modern, conservative TLS profile.
func baseTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
		},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
		NextProtos: []string{"h2", "http/1.1"},
	}
}

// hostVariants allows both the bare domain and its www form.
func hostVariants(domain string) []string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	if strings.HasPrefix(domain, "www.") {
		return []string{domain, strings.TrimPrefix(domain, "www.")}
	}
	return []string{domain, "www." + domain}
}
