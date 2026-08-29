package main

// The page a person opens to see how many servers are running Storix.
//
// The JSON at /v1/stats is the same figure, but reading it means remembering a
// token and piping it through something that formats it. This is the version
// you bookmark.
//
// It is gated by its own key rather than the statistics token, so a link left
// in a browser history or pasted to somebody never carries the credential that
// reads the API. The key is compared in constant time, and the page carries no
// script, no font and no image from anywhere else: it is one document.
//
// Developed by X Project.

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"
)

// viewKeyEnv names the environment variable holding the key for the page.
const viewKeyEnv = "STORIX_UPDATES_VIEW_KEY"

// dashboardRow is one line in a breakdown.
type dashboardRow struct {
	Label   string
	Count   int
	Percent float64
}

// dashboardData is everything the page shows.
type dashboardData struct {
	Total     int
	Active24h int
	Active7d  int
	Active30d int
	Checks    int64
	Versions  []dashboardRow
	Platforms []dashboardRow
	FirstSeen string
	LastSeen  string
	Refused   int64
	Generated string
}

// handleDashboard answers GET /dashboard.
func (s *service) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint accepts GET")
		return
	}
	if s.viewKey == "" {
		writeError(w, http.StatusServiceUnavailable, "dashboard_disabled",
			"The page is not enabled on this service")
		return
	}
	presented := strings.TrimSpace(r.URL.Query().Get("k"))
	if subtle.ConstantTimeCompare([]byte(presented), []byte(s.viewKey)) != 1 {
		// Nothing here says whether the key was close or whether the page
		// exists at all.
		writeError(w, http.StatusNotFound, "not_found", "There is nothing at this address")
		return
	}

	stats, err := s.store.Stats(r.Context(), time.Now())
	if err != nil {
		s.logger.Error("could not read statistics", "err", err)
		writeError(w, http.StatusInternalServerError, "stats_failed", "The statistics could not be read")
		return
	}

	data := dashboardData{
		Total:     stats.Total,
		Active24h: stats.Active24h,
		Active7d:  stats.Active7d,
		Active30d: stats.Active30d,
		Checks:    stats.Checks,
		Versions:  breakdown(stats.Versions, stats.Total, true),
		Platforms: breakdown(stats.Platforms, stats.Total, false),
		FirstSeen: stamp(stats.FirstSeen),
		LastSeen:  stamp(stats.LastSeen),
		Refused:   stats.RefusedNew,
		Generated: stats.GeneratedAt.Format("2006-01-02 15:04 UTC"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// The figures are the operator's, not something to be indexed or framed.
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	if r.Method == http.MethodHead {
		return
	}
	if err := dashboardTemplate.Execute(w, data); err != nil {
		s.logger.Error("could not render the page", "err", err)
	}
}

// breakdown turns a tally into rows, largest first, with a share of the total.
// Versions read better newest first when the counts tie, which is why the sort
// differs.
func breakdown(counts map[string]int, total int, versions bool) []dashboardRow {
	rows := make([]dashboardRow, 0, len(counts))
	for label, n := range counts {
		share := 0.0
		if total > 0 {
			share = float64(n) * 100 / float64(total)
		}
		rows = append(rows, dashboardRow{Label: label, Count: n, Percent: share})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if versions {
			return rows[i].Label > rows[j].Label
		}
		return rows[i].Label < rows[j].Label
	})
	return rows
}

// stamp formats a time for the page, or says it never happened.
func stamp(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format("2006-01-02 15:04 UTC")
}

// dashboardTemplate is the whole page. Every value passing through it is
// escaped by html/template, which matters because the version and platform
// labels arrive from callers on the internet.
var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="refresh" content="120">
<title>Storix servers</title>
<style>
  :root {
    --bg: #0B0F17;
    --surface: #111827;
    --line: #1F2937;
    --ink: #E5E7EB;
    --muted: #9CA3AF;
    --faint: #6B7280;
    --primary: #00D4FF;
    --secondary: #0077FF;
    --accent: #7C3AED;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--ink);
    font: 15px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Arial, sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  .wrap { max-width: 900px; margin: 0 auto; padding: 48px 24px 64px; }
  header { display: flex; align-items: center; gap: 12px; margin-bottom: 40px; }
  header svg { flex: none; }
  h1 { font-size: 20px; font-weight: 600; margin: 0; letter-spacing: -0.01em; }
  .sub { color: var(--faint); font-size: 13px; margin-top: 2px; }
  .headline {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 14px;
    padding: 32px;
    margin-bottom: 20px;
  }
  .big {
    font-size: 72px;
    line-height: 1;
    font-weight: 650;
    letter-spacing: -0.03em;
    background: linear-gradient(120deg, var(--primary), var(--secondary) 55%, var(--accent));
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }
  .big-label { color: var(--muted); font-size: 14px; margin-top: 10px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 14px; margin-bottom: 28px; }
  .tile {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 12px;
    padding: 18px;
  }
  .tile .n { font-size: 26px; font-weight: 600; letter-spacing: -0.02em; }
  .tile .l { color: var(--faint); font-size: 12px; margin-top: 4px; text-transform: uppercase; letter-spacing: 0.06em; }
  section { margin-bottom: 28px; }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--faint);
    font-weight: 600;
    margin: 0 0 12px;
  }
  .rows { background: var(--surface); border: 1px solid var(--line); border-radius: 12px; overflow: hidden; }
  .row { display: flex; align-items: center; gap: 14px; padding: 13px 18px; border-top: 1px solid var(--line); }
  .row:first-child { border-top: none; }
  .row .name { width: 150px; flex: none; font-variant-numeric: tabular-nums; }
  .bar { flex: 1; height: 6px; background: #0B1220; border-radius: 99px; overflow: hidden; }
  .bar span { display: block; height: 100%; background: linear-gradient(90deg, var(--primary), var(--secondary)); border-radius: 99px; }
  .row .n { width: 56px; text-align: right; flex: none; color: var(--muted); font-variant-numeric: tabular-nums; }
  .meta { border-top: 1px solid var(--line); padding-top: 20px; color: var(--faint); font-size: 13px; }
  .meta div { display: flex; justify-content: space-between; padding: 4px 0; }
  .meta .k { color: var(--faint); }
  .meta .v { color: var(--muted); font-variant-numeric: tabular-nums; }
  .warn { color: #F59E0B; }
  footer { margin-top: 36px; color: var(--faint); font-size: 12px; text-align: center; }
  .empty { padding: 22px 18px; color: var(--faint); }
  @media (max-width: 560px) {
    .wrap { padding: 28px 16px 48px; }
    .big { font-size: 56px; }
    .row .name { width: 110px; }
  }
</style>
</head>
<body>
<div class="wrap">

  <header>
    <svg width="30" height="30" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <defs>
        <linearGradient id="g" x1="0" y1="0" x2="24" y2="24" gradientUnits="userSpaceOnUse">
          <stop stop-color="#00D4FF"/><stop offset="1" stop-color="#7C3AED"/>
        </linearGradient>
      </defs>
      <rect x="2.5" y="4" width="19" height="6" rx="2" stroke="url(#g)" stroke-width="1.7"/>
      <rect x="2.5" y="14" width="19" height="6" rx="2" stroke="url(#g)" stroke-width="1.7"/>
      <circle cx="6.5" cy="7" r="1.1" fill="#00D4FF"/>
      <circle cx="6.5" cy="17" r="1.1" fill="#7C3AED"/>
    </svg>
    <div>
      <h1>Storix servers</h1>
      <div class="sub">Counted from the update check</div>
    </div>
  </header>

  <div class="headline">
    <div class="big">{{.Total}}</div>
    <div class="big-label">servers running Storix</div>
  </div>

  <div class="grid">
    <div class="tile"><div class="n">{{.Active24h}}</div><div class="l">seen today</div></div>
    <div class="tile"><div class="n">{{.Active7d}}</div><div class="l">this week</div></div>
    <div class="tile"><div class="n">{{.Active30d}}</div><div class="l">this month</div></div>
    <div class="tile"><div class="n">{{.Checks}}</div><div class="l">check ins</div></div>
  </div>

  <section>
    <h2>Versions</h2>
    <div class="rows">
      {{- if .Versions}}
      {{- range .Versions}}
      <div class="row">
        <div class="name">{{.Label}}</div>
        <div class="bar"><span style="width:{{printf "%.1f" .Percent}}%"></span></div>
        <div class="n">{{.Count}}</div>
      </div>
      {{- end}}
      {{- else}}
      <div class="empty">Nothing has checked in yet.</div>
      {{- end}}
    </div>
  </section>

  <section>
    <h2>Platforms</h2>
    <div class="rows">
      {{- if .Platforms}}
      {{- range .Platforms}}
      <div class="row">
        <div class="name">{{.Label}}</div>
        <div class="bar"><span style="width:{{printf "%.1f" .Percent}}%"></span></div>
        <div class="n">{{.Count}}</div>
      </div>
      {{- end}}
      {{- else}}
      <div class="empty">Nothing has checked in yet.</div>
      {{- end}}
    </div>
  </section>

  <div class="meta">
    <div><span class="k">First counted</span><span class="v">{{.FirstSeen}}</span></div>
    <div><span class="k">Last check in</span><span class="v">{{.LastSeen}}</span></div>
    {{- if .Refused}}
    <div><span class="k warn">Refused as invented</span><span class="v warn">{{.Refused}}</span></div>
    {{- end}}
    <div><span class="k">Read at</span><span class="v">{{.Generated}}</span></div>
  </div>

  <footer>Developed by X Project</footer>

</div>
</body>
</html>
`))
