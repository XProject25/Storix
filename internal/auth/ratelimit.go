package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Header names consulted when the direct peer is a trusted proxy.
const (
	forwardedForHeader = "X-Forwarded-For"
	realIPHeader       = "X-Real-IP"
)

// janitorFloor keeps the sweep interval sane for very short windows.
const janitorFloor = 5 * time.Second

// Limiter is a sliding window rate limiter keyed by an arbitrary string,
// usually a client address or an address and username pair. It is safe for
// concurrent use and keeps only the timestamps still inside the window, so a
// key costs at most limit timestamps of memory.
//
// The window slides, so a burst does not reset at a fixed boundary the way it
// would with a fixed bucket, which is what makes it useful against a login
// flood.
type Limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time

	// now is swappable so tests can drive the clock.
	now func() time.Time

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewLimiter returns a limiter that allows limit events per key per window and
// starts a janitor goroutine that drops idle keys. Call Close when done. A
// limit below one is raised to one and a window at or below zero becomes one
// minute.
func NewLimiter(limit int, window time.Duration) *Limiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	l := &Limiter{
		limit:  limit,
		window: window,
		hits:   make(map[string][]time.Time),
		now:    time.Now,
		stop:   make(chan struct{}),
	}
	l.wg.Add(1)
	go l.janitor()
	return l
}

// janitor sweeps keys whose events have all aged out, so a scan of many
// distinct addresses cannot grow the map without bound.
func (l *Limiter) janitor() {
	defer l.wg.Done()
	interval := l.window
	if interval < janitorFloor {
		interval = janitorFloor
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.sweep()
		}
	}
}

// sweep prunes every key and removes the ones left empty.
func (l *Limiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.window)
	for key, times := range l.hits {
		kept := prune(times, cutoff)
		if len(kept) == 0 {
			delete(l.hits, key)
			continue
		}
		l.hits[key] = kept
	}
}

// prune drops the timestamps that have left the window. The slice is kept in
// ascending order, so this is a single scan from the front.
func prune(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return times
	}
	return times[i:]
}

// Allow records an event and reports whether it fits inside the limit. A
// rejected event is not recorded, so a client that backs off recovers as soon
// as the window slides.
func (l *Limiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	times := prune(l.hits[key], now.Add(-l.window))
	if len(times) >= l.limit {
		l.hits[key] = times
		return false
	}
	l.hits[key] = append(times, now)
	return true
}

// Observe records an event without deciding anything. Use it to count a
// failure that has already happened, for example a rejected password, so the
// next Allow sees it.
func (l *Limiter) Observe(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	times := prune(l.hits[key], now.Add(-l.window))
	// Cap the slice so repeated Observe calls on a blocked key cannot grow it.
	if len(times) >= l.limit {
		times = times[len(times)-l.limit+1:]
	}
	l.hits[key] = append(times, now)
}

// Remaining reports how many further events the key may spend in the current
// window.
func (l *Limiter) Remaining(key string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	times := prune(l.hits[key], l.now().Add(-l.window))
	l.hits[key] = times
	left := l.limit - len(times)
	if left < 0 {
		return 0
	}
	return left
}

// RetryAfter reports how long the key has to wait before Allow can succeed
// again. It is zero when the key is not currently blocked, which makes it
// safe to put straight into a Retry-After header.
func (l *Limiter) RetryAfter(key string) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	times := prune(l.hits[key], now.Add(-l.window))
	l.hits[key] = times
	if len(times) < l.limit {
		return 0
	}
	// The oldest event is the one whose departure frees a slot.
	wait := times[0].Add(l.window).Sub(now)
	if wait < 0 {
		return 0
	}
	return wait
}

// Reset forgets everything recorded for a key, which is what a successful
// sign in should do to the failed attempt counter.
func (l *Limiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// Close stops the janitor. It is safe to call more than once.
func (l *Limiter) Close() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		close(l.stop)
		l.wg.Wait()
	})
}

// ClientIP returns the address Storix should attribute a request to.
//
// X-Forwarded-For and X-Real-IP are attacker controlled on a direct
// connection, so they are read only when the peer that actually opened the
// socket falls inside one of the trusted proxy ranges. Inside a trusted chain
// the forwarded list is walked from the right, skipping entries that are
// themselves trusted proxies, and the first address that is not is the
// client. Anything unparsable falls back to the direct peer, which is the
// only value that cannot be forged.
//
// trustedProxies accepts CIDR blocks ("10.0.0.0/8", "fd00::/8") and bare
// addresses ("127.0.0.1"). Ports are always stripped from the result.
func ClientIP(r *http.Request, trustedProxies []string) string {
	if r == nil {
		return ""
	}
	peerHost := hostOnly(r.RemoteAddr)
	peer, err := netip.ParseAddr(peerHost)
	if err != nil {
		// Not an address at all, for example a unix socket peer. There is
		// nothing safer to report than what the server saw.
		return peerHost
	}
	peer = peer.Unmap()

	prefixes := parsePrefixes(trustedProxies)
	if !inPrefixes(peer, prefixes) {
		return peer.String()
	}

	for _, raw := range r.Header.Values(forwardedForHeader) {
		parts := strings.Split(raw, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			addr, ok := parseForwarded(parts[i])
			if !ok {
				continue
			}
			if inPrefixes(addr, prefixes) {
				// Another hop of our own proxy chain; keep walking left.
				continue
			}
			return addr.String()
		}
	}
	if v := strings.TrimSpace(r.Header.Get(realIPHeader)); v != "" {
		if addr, ok := parseForwarded(v); ok {
			return addr.String()
		}
	}
	return peer.String()
}

// parseForwarded reads one element of a forwarded header. The element may be
// a bare address, an address with a port, or an address wrapped in brackets.
func parseForwarded(raw string) (netip.Addr, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return netip.Addr{}, false
	}
	s = strings.Trim(s, `"`)
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	// Retry as host:port, which covers both "1.2.3.4:5678" and "[::1]:5678".
	if host, _, err := net.SplitHostPort(s); err == nil {
		if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			return addr.Unmap(), true
		}
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return addr.Unmap(), true
	}
	return netip.Addr{}, false
}

// parsePrefixes turns the configured trusted proxy list into prefixes,
// silently dropping entries that do not parse rather than failing a request
// over a typo in the configuration file.
func parsePrefixes(list []string) []netip.Prefix {
	if len(list) == 0 {
		return nil
	}
	out := make([]netip.Prefix, 0, len(list))
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()))
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// inPrefixes reports whether an address falls inside any trusted prefix.
func inPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// hostOnly strips the port from a "host:port" pair, leaving anything else
// untouched.
func hostOnly(remote string) string {
	s := strings.TrimSpace(remote)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(s, "[]")
}
