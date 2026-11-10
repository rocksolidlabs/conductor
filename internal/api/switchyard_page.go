package api

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// The switchyard's own pages. A switchyard serves no workbench: the agents,
// crews, wall and every terminal live in the Conductor on a person's own
// machine. What it serves is the landing page at /, a 404 page for every
// workbench path, the join page (the app, under /join and /paste, with its
// assets) and the API. Both pages are rendered here from Go templates with
// the brand's tokens inlined (web/public/brand/tokens.css, plus the dark
// values the design added), so they stand without the app being built. The
// copy is the design's, word for word (docs/design: "Switchyard Pages").

const (
	switchyardDocsURL   = "https://github.com/rocksolidlabs/conductor#readme"
	switchyardSourceURL = "https://github.com/rocksolidlabs/conductor"
)

// switchyardAppURL is the pages' Get the desktop app link: switchyard.appURL,
// or the repository's releases page. Validate refuses one out of form at
// load; a config that skipped it (one built in code) gets the default, and
// the log says so once, without the value.
func (s *Server) switchyardAppURL() string {
	link, ok := s.cfg.SwitchyardAppURL()
	if !ok {
		s.appURLSaid.Do(func() {
			s.log.Warn("switchyard.appURL is not an https URL with a host, within its bound: the pages link the default", "maxBytes", config.MaxSwitchyardAppURL, "default", link)
		})
	}
	return link
}

// relayMeter counts the bytes the switchyard relayed over the last hour, in
// one-minute buckets, for the operator's "Relay this hour" figure.
type relayMeter struct {
	now     func() time.Time
	mu      sync.Mutex
	buckets [60]int64
	minute  int64 // the unix minute buckets[minute%60] holds
}

func newRelayMeter(now func() time.Time) *relayMeter {
	if now == nil {
		now = time.Now
	}
	m := &relayMeter{now: now}
	m.minute = m.now().Unix() / 60
	return m
}

// advance moves the window to the current minute, clearing what fell out.
func (m *relayMeter) advance() int64 {
	cur := m.now().Unix() / 60
	if cur > m.minute {
		for i := m.minute + 1; i <= cur && i-m.minute <= 60; i++ {
			m.buckets[i%60] = 0
		}
		if cur-m.minute > 60 {
			m.buckets = [60]int64{}
		}
		m.minute = cur
	}
	return cur
}

func (m *relayMeter) add(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	cur := m.advance()
	m.buckets[cur%60] += int64(n)
	m.mu.Unlock()
}

// lastHour is the bytes relayed in the last sixty minutes.
func (m *relayMeter) lastHour() int64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.advance()
	var sum int64
	for _, b := range m.buckets {
		sum += b
	}
	return sum
}

// switchyardHost is one publishing machine as the operator's card lists it.
type switchyardHost struct {
	Name     string    `json:"name"`
	Since    time.Time `json:"since"`
	Sessions int       `json:"sessions"`
	Viewers  int       `json:"viewers"`
}

// switchyardCounts reads the hosted sessions that are live: the hosts they
// come from (earliest session first), how many sessions and viewers.
func (s *Server) switchyardCounts() (hosts []switchyardHost, sessions, viewers int) {
	byName := map[string]*switchyardHost{}
	for _, info := range s.registry.List() {
		if info.Kind != session.KindHosted || info.EndedAt != nil {
			continue
		}
		if info.Status != session.StatusRunning && info.Status != session.StatusStarting {
			continue
		}
		name := info.HostName
		if name == "" {
			name = "unnamed host"
		}
		h := byName[name]
		if h == nil {
			h = &switchyardHost{Name: name, Since: info.CreatedAt}
			byName[name] = h
		}
		if info.CreatedAt.Before(h.Since) {
			h.Since = info.CreatedAt
		}
		h.Sessions++
		h.Viewers += info.Viewers
		sessions++
		viewers += info.Viewers
	}
	hosts = make([]switchyardHost, 0, len(byName))
	for _, h := range byName {
		hosts = append(hosts, *h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Since.Equal(hosts[j].Since) {
			return hosts[i].Name < hosts[j].Name
		}
		return hosts[i].Since.Before(hosts[j].Since)
	})
	return hosts, sessions, viewers
}

// handleSwitchyardStatus is GET /api/switchyard/status (workbench token): the
// operator's figures on the landing page. A plain server has no such route.
func (s *Server) handleSwitchyardStatus(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "no such route")
		return
	}
	hosts, sessions, viewers := s.switchyardCounts()
	writeJSON(w, http.StatusOK, map[string]any{
		"hosts":          len(hosts),
		"sessions":       sessions,
		"viewers":        viewers,
		"relayBytesHour": s.relayed.lastHour(),
		"hostList":       hosts,
	})
}

// switchyardPage is what the templates draw from.
type switchyardPage struct {
	Host          string // the public host name, for the invite forms
	Version       string
	Uptime        string
	TLS           string
	TLSClass      string
	CertWarn      string
	Relay         string
	RelayClass    string
	RelayOn       bool
	OpenHosts     bool
	PublicAddress string
	DocsURL       string
	SourceURL     string
	AppURL        string
	Path          string // the 404 page's path
}

func (s *Server) switchyardPageData() switchyardPage {
	p := switchyardPage{
		Version:   version.Version,
		Uptime:    uptimeWords(time.Since(s.started)),
		DocsURL:   switchyardDocsURL,
		SourceURL: switchyardSourceURL,
		AppURL:    s.switchyardAppURL(),
	}
	if u, err := url.Parse(s.cfg.PublicURL); err == nil && u.Host != "" {
		p.Host = u.Host
	}
	if p.Host == "" {
		p.Host = "this server"
	}
	p.PublicAddress = p.Host
	if s.reach != nil {
		if st := s.reach.Status(); st.ExternalIP.IsValid() {
			p.PublicAddress = st.ExternalIP.String()
		}
	}
	p.TLS, p.TLSClass, p.CertWarn = s.tlsRow()
	p.RelayOn = s.cfg.SwitchyardRelay()
	p.OpenHosts = s.cfg.Switchyard.OpenHosts
	switch {
	case !p.RelayOn:
		p.Relay, p.RelayClass = "Off · direct connections only", "muted"
	case s.cfg.Switchyard.RelayKBps > 0:
		p.Relay = "On · up to " + kbpsWords(s.cfg.Switchyard.RelayKBps) + " per host"
	default:
		p.Relay = "On · no bound per host"
	}
	return p
}

// tlsRow is the status card's TLS line, its class, and the amber line shown
// to everyone when a renewal failed (the renewal date is public anyway).
func (s *Server) tlsRow() (row, class, warn string) {
	if s.certs == nil {
		return "Off · plain http", "muted", ""
	}
	st := s.certs.Status()
	expires := func() string {
		if st.NotAfter.IsZero() {
			return ""
		}
		return st.NotAfter.UTC().Format("Jan 2, 15:04 UTC")
	}
	switch {
	case st.Ready && st.LastError == "":
		if st.RenewAt.IsZero() {
			return "On", "", ""
		}
		return "On · certificate renews " + st.RenewAt.UTC().Format("Jan 2, 2006"), "", ""
	case st.Ready:
		return "On · certificate expires " + expires(), "warn",
			"Certificate renewal failed. The current certificate expires " + expires() + "."
	case st.LastError != "":
		return "Waiting for a certificate", "warn", "The certificate order failed; the next try is " + nextTryWords(st.NextTry) + "."
	default:
		return "Waiting for the first certificate", "warn", ""
	}
}

func nextTryWords(t time.Time) string {
	if t.IsZero() {
		return "soon"
	}
	return "at " + t.UTC().Format("Jan 2, 15:04 UTC")
}

// uptimeWords: "12 days 4 hours", "4 hours 12 minutes", "3 minutes".
func uptimeWords(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, unit)
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return plural(days, "day") + " " + plural(hours, "hour")
	case hours > 0:
		return plural(hours, "hour") + " " + plural(minutes, "minute")
	default:
		return plural(minutes, "minute")
	}
}

// kbpsWords: "8 MB/s", "512 KB/s".
func kbpsWords(kbps int) string {
	if kbps >= 1024 && kbps%1024 == 0 {
		return fmt.Sprintf("%d MB/s", kbps/1024)
	}
	if kbps >= 1024 {
		return fmt.Sprintf("%.1f MB/s", float64(kbps)/1024)
	}
	return fmt.Sprintf("%d KB/s", kbps)
}

// switchyardFront routes a switchyard's GET requests: the landing page at /,
// the app under /join and /paste and for its assets (anything with a file
// extension, /_nuxt, /brand, /_fonts), and the 404 page for every other
// path, which is where the workbench's routes would be. The app handler is
// read at request time, so a server built without the UI still serves its
// own pages.
func (s *Server) switchyardFront() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		switch {
		case p == "/":
			s.serveSwitchyardPage(w, http.StatusOK, "landing", s.switchyardPageData())
		case p == "/paste" || strings.HasPrefix(p, "/join/") || strings.HasPrefix(p, "/_nuxt/") ||
			strings.HasPrefix(p, "/brand/") || strings.HasPrefix(p, "/_fonts/") || strings.Contains(path.Base(p), "."):
			if s.web == nil {
				http.Error(w, "web UI not built; run make web-build", http.StatusServiceUnavailable)
				return
			}
			s.web.ServeHTTP(w, r)
		default:
			d := s.switchyardPageData()
			d.Path = p
			if len(d.Path) > 120 {
				d.Path = d.Path[:120] + "…"
			}
			s.serveSwitchyardPage(w, http.StatusNotFound, "notfound", d)
		}
	})
}

func (s *Server) serveSwitchyardPage(w http.ResponseWriter, status int, name string, d switchyardPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := switchyardTemplates.ExecuteTemplate(w, name, d); err != nil {
		s.log.Warn("switchyard page", "page", name, "err", err)
	}
}

var switchyardTemplates = template.Must(template.New("switchyard").Parse(switchyardTemplateText))

// The pages. Classes over inline styles; dark always (the workbench's zinc
// surfaces with forest-300 as the action colour, as the design has it), whatever
// the system prefers, no toggle. No JavaScript but the operator form.
const switchyardTemplateText = `
{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta name="color-scheme" content="dark">
<title>{{template "title" .}}</title>
<link rel="icon" href="/brand/conductor-favicon.svg" type="image/svg+xml">
<style>
:root{--conductor-surface:#18181B;--conductor-panel:#18181B;--conductor-text:#FFFFFF;--conductor-body:#E4E4E7;--conductor-muted:#A1A1AA;--conductor-border:#27272A;--conductor-action:#9BB3A3;--conductor-on-action:#18181B;--conductor-accent-text:#DF8259;--conductor-warning:#DBA63E;--conductor-warning-bg:rgba(219,166,62,.12);--conductor-success:#6FAE83;--conductor-idle:#71717A;--conductor-mark:#EEF1E9;--sy-code:#27272A;--conductor-font-ui:Inter,ui-sans-serif,system-ui,sans-serif;--conductor-font-code:"JetBrains Mono",ui-monospace,SFMono-Regular,Consolas,monospace}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;flex-direction:column;background:var(--conductor-surface);color:var(--conductor-body);font-family:var(--conductor-font-ui);-webkit-font-smoothing:antialiased}
a{color:var(--conductor-accent-text)}a:hover{color:var(--conductor-text)}
code,.code,.mono{font-family:var(--conductor-font-code)}
header.sy{display:flex;align-items:center;gap:8px;padding:16px 32px;border-bottom:1px solid var(--conductor-border)}
header.sy svg{flex:none}
header.sy .word{font-size:16px;font-weight:700;letter-spacing:-.02em;color:var(--conductor-text)}
header.sy .div{width:1px;height:20px;background:var(--conductor-border);margin:0 4px}
header.sy .for{font-size:13px;color:var(--conductor-muted)}
header.sy .lockup{display:flex;align-items:center;gap:6px;font-size:14px;font-weight:600;color:var(--conductor-text)}
header.sy .host{margin-left:auto;font:400 13px var(--conductor-font-code);color:var(--conductor-muted)}
main.sy{flex:1;width:100%;max-width:1040px;margin:0 auto;padding:64px 32px;display:flex;flex-direction:column;gap:32px}
main.sy.narrow{max-width:720px;gap:24px}
.hero{display:flex;flex-direction:column;gap:12px;max-width:720px}
h1{margin:0;font-size:28px;line-height:1.25;font-weight:600;letter-spacing:-.01em;color:var(--conductor-text)}
h2{margin:0;font-size:15px;line-height:22px;font-weight:600;color:var(--conductor-text)}
p{margin:0;font-size:14px;line-height:22px}
p.muted,.muted{color:var(--conductor-muted)}
.small{font-size:13px;line-height:20px}
.cols{display:flex;flex-wrap:wrap;gap:32px;align-items:flex-start}
.col{flex:1 1 520px;min-width:0;display:flex;flex-direction:column;gap:16px}
.aside{flex:1 1 320px;max-width:400px;min-width:0;display:flex;flex-direction:column;gap:16px}
.card{background:var(--conductor-panel);border:1px solid var(--conductor-border);border-radius:6px;padding:20px 24px;display:flex;flex-direction:column;gap:8px}
.card.status{gap:12px}
.code{display:flex;flex-direction:column;gap:4px;background:var(--sy-code);border-radius:6px;padding:8px 12px;font-size:13px;line-height:20px;word-break:break-all;white-space:pre-wrap}
.btns{display:flex;flex-wrap:wrap;gap:8px;margin-top:4px}
.btn{height:32px;display:inline-flex;align-items:center;padding:0 10px;border-radius:6px;background:var(--conductor-action);color:var(--conductor-on-action);font-size:14px;font-weight:500;text-decoration:none;border:0;cursor:pointer;font-family:inherit}
.btn:hover{color:var(--conductor-on-action);opacity:.92}
.btn.ghost{background:transparent;color:var(--conductor-text);border:1px solid var(--conductor-border)}
.btn.ghost:hover{color:var(--conductor-text)}
.title{display:flex;align-items:center;gap:8px}
.dot{width:8px;height:8px;border-radius:50%;background:var(--conductor-success);flex:none}
.dot.idle{background:var(--conductor-idle)}
dl{margin:0;display:flex;flex-direction:column;gap:8px}
.row{display:grid;grid-template-columns:108px minmax(0,1fr);gap:12px;font-size:13px;line-height:20px}
.row dt{color:var(--conductor-muted)}
.row dd{margin:0;word-break:break-word}
.row dd.warn{color:var(--conductor-warning)}
.row dd.muted{color:var(--conductor-muted)}
.warnline{background:var(--conductor-warning-bg);color:var(--conductor-warning);border-radius:6px;padding:8px 12px;font-size:13px;line-height:20px}
.op-head{display:flex;align-items:baseline}
.op-head a{margin-left:auto;font-size:13px}
.form{display:flex;gap:8px}
.form input{flex:1;min-width:0;height:32px;border:1px solid var(--conductor-border);border-radius:6px;background:var(--conductor-surface);color:var(--conductor-text);padding:0 12px;font:400 13px var(--conductor-font-code)}
.form input:focus{outline:3px solid var(--conductor-accent-text);outline-offset:1px}
.counts{display:grid;grid-template-columns:1fr 1fr;gap:8px 16px}
.counts div{display:flex;flex-direction:column}
.counts dt{font-size:13px;line-height:20px;color:var(--conductor-muted)}
.counts dd{margin:0;font-size:20px;line-height:28px;font-weight:600;color:var(--conductor-text)}
.hosts{display:flex;flex-direction:column;border-top:1px solid var(--conductor-border)}
.host{display:flex;flex-direction:column;gap:2px;padding:8px 0;border-bottom:1px solid var(--conductor-border)}
.host .line{display:flex;align-items:center;gap:8px}
.host .name{font:500 13px var(--conductor-font-code);color:var(--conductor-text)}
.host .since{margin-left:auto;font-size:13px;color:var(--conductor-muted)}
.host .detail{font-size:13px;line-height:20px;color:var(--conductor-muted);padding-left:16px}
.nohosts{display:flex;flex-direction:column;gap:4px;border-top:1px solid var(--conductor-border);padding-top:12px}
.nohosts b{font-size:14px;font-weight:600;color:var(--conductor-text)}
.hidden{display:none}
.rows{background:var(--conductor-panel);border:1px solid var(--conductor-border);border-radius:6px;display:flex;flex-direction:column}
.rows>div{padding:16px 24px;display:flex;flex-direction:column;gap:4px;border-bottom:1px solid var(--conductor-border)}
.rows>div:last-child{border-bottom:0}
.rows b{font-size:15px;font-weight:600;color:var(--conductor-text)}
.back{font-size:14px}
footer.sy{display:flex;flex-wrap:wrap;gap:8px 16px;padding:16px 32px;border-top:1px solid var(--conductor-border);font-size:13px;line-height:20px;color:var(--conductor-muted)}
footer.sy .right{margin-left:auto}
footer.sy .note{flex-basis:100%}
.badge{display:inline-flex;align-items:center;gap:10px;height:36px;padding:0 12px;border:1px solid #333;border-radius:6px;background:#000;color:#999;text-decoration:none}.badge:hover{border-color:var(--conductor-muted);color:var(--conductor-body)}.badge span{font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.04em}.badge img{height:18px;width:auto;display:block}
@media (max-width:640px){header.sy,footer.sy{padding-left:16px;padding-right:16px}header.sy .host{display:none}main.sy{padding:32px 16px}h1{font-size:24px}footer.sy .right{margin-left:0}}
</style>
</head>
<body>
<header class="sy">
<svg viewBox="0 0 256 256" width="28" height="28" aria-hidden="true"><g style="fill:var(--conductor-mark)"><path d="M170 41H98A62 62 0 0 0 98 165H138A12 12 0 0 0 138 141H98A38 38 0 0 1 98 65H146Z"></path><path d="M98 91H138A62 62 0 0 1 138 215H78A12 12 0 0 1 78 191H138A38 38 0 0 0 138 115H98A12 12 0 0 1 98 91Z"></path></g><path fill="#D26B3F" d="M184 41H224L198 67H158Z"></path></svg>
<span class="word">Switchyard</span>
<span class="div"></span>
<span class="for">for</span>
<span class="lockup"><svg viewBox="0 0 256 256" width="20" height="20" aria-hidden="true"><g style="fill:var(--conductor-mark)"><path d="M198 39.5H124A88.5 88.5 0 0 0 124 216.5H204A8.5 8.5 0 0 0 204 199.5H124A71.5 71.5 0 0 1 124 56.5H181Z"></path><path d="M199 63.5L176 86.5Q170 92.5 167 92.5H124A35.5 35.5 0 0 0 124 163.5H204A8.5 8.5 0 0 1 204 180.5H124A52.5 52.5 0 0 1 124 75.5H164.5L176.5 63.5Z"></path></g><path fill="#D26B3F" d="M204 39.5H236L216 59.5H184Z"></path></svg>Conductor</span>
<span class="host">{{.Host}}</span>
</header>
{{end}}

{{define "foot"}}
<footer class="sy">
<span>Conductor switchyard <span class="mono">{{.Version}}</span> · Apache-2.0 · <a href="{{.DocsURL}}">Docs</a> · <a href="{{.SourceURL}}">Source</a> · <a href="/third-party-notices.txt" data-notices>Third-party notices</a></span>
<a class="right badge" href="https://rocksolidlabs.io" data-credit><span>Sponsored by</span><img src="/sponsor/rocksolidlabs-logo-reversed.png" alt="RockSolid Labs" width="82" height="18"></a>
<span class="note">No cookies. An operator token you paste stays in this browser.</span>
</footer>
</body>
</html>
{{end}}

{{define "landing"}}{{template "head" .}}
<main class="sy">
<div class="hero">
<h1>This is a Conductor switchyard.</h1>
<p class="muted">It introduces viewers to Conductor sessions running on their owners' own machines. No agent runs here and no output is kept. It mints and checks the links that open those sessions. {{if .RelayOn}}Once a viewer joins, the session goes over WebRTC between the owner's machine and the viewer's browser, and through this server's relay, which can read it, when the network allows no direct path.{{else}}Once a viewer joins, the session goes over WebRTC between the owner's machine and the viewer's browser. This server's relay is off, so a viewer whose network allows no direct path cannot join.{{end}}</p>
</div>
<div class="cols">
<div class="col">
<section class="card">
<h2>Were you sent a link?</h2>
<p>Open it exactly as you received it. Links from this switchyard look like this:</p>
<div class="code"><span>https://{{.Host}}/join/…</span><span>conductor://{{.Host}}/join/…</span></div>
<p class="small muted">The second kind opens in the desktop app. This page lists no sessions; without a link there is nothing to join.</p>
</section>
<section class="card">
<h2>Looking for the workbench?</h2>
<p>Conductor runs on your own computer: the agents, crews, yard and every terminal stay there. Install it, then share from it through a switchyard like this one.</p>
<div class="btns"><a class="btn" href="{{.AppURL}}">Get the desktop app</a><a class="btn ghost" href="{{.DocsURL}}">Read the docs</a></div>
</section>
<section class="card">
<h2>Sharing from your machine?</h2>
{{if .OpenHosts}}<p>Nothing to configure: the desktop app and <span class="mono">conductor serve</span> publish here by default. To name this switchyard yourself:</p>
<div class="code">"rendezvous": {
  "server": "https://{{.Host}}"
}</div>
<p class="small muted">In the desktop app it is Settings → Switchyard. A host token is optional and marks a trusted machine, outside the per-address limits. Your sessions run on your machine. This server is told each one's name, agent, folder name and your machine's name, mints and checks its links and decides who joins: publish here only if you trust its operator.</p>{{else}}<p>Ask the operator of this switchyard for a host token, then add both to your Conductor config:</p>
<div class="code">"rendezvous": {
  "server": "https://{{.Host}}",
  "token": "&lt;host token&gt;"
}</div>
<p class="small muted">In the desktop app it is Settings → Switchyard. Your sessions run on your machine. This server is told each one's name, agent, folder name and your machine's name, mints and checks its links and decides who joins: publish here only if you trust its operator.</p>{{end}}
</section>
</div>
<aside class="aside">
<section class="card status">
<div class="title"><span class="dot"></span><h2>This server is up</h2></div>
{{if .CertWarn}}<p class="warnline">{{.CertWarn}}</p>{{end}}
<dl>
<div class="row"><dt>Version</dt><dd class="mono">{{.Version}}</dd></div>
<div class="row"><dt>Up</dt><dd>{{.Uptime}}</dd></div>
<div class="row"><dt>TLS</dt><dd class="{{.TLSClass}}">{{.TLS}}</dd></div>
<div class="row"><dt>Relay</dt><dd class="{{.RelayClass}}">{{.Relay}}</dd></div>
<div class="row"><dt>Public address</dt><dd class="mono">{{.PublicAddress}}</dd></div>
<div class="row"><dt>Invites</dt><dd class="mono">conductor://{{.Host}}/join/&lt;token&gt;</dd></div>
</dl>
</section>
<section class="card status" id="operator">
<div class="op-head"><h2>Operator</h2><a href="#" id="op-forget" class="hidden">Forget token</a></div>
<div id="op-anon">
<p class="small muted">Paste the workbench token to see connected hosts and traffic. It is kept in this browser only.</p>
<form class="form" id="op-form"><input type="password" id="op-token" placeholder="workbench token" autocomplete="off" aria-label="workbench token"><button class="btn ghost" type="submit">Show</button></form>
<p class="small muted hidden" id="op-refused">That token was refused.</p>
<noscript><p class="small muted">The operator view needs JavaScript.</p></noscript>
</div>
<div id="op-view" class="hidden">
<dl class="counts"><div><dt>Hosts connected</dt><dd id="c-hosts">0</dd></div><div><dt>Sessions published</dt><dd id="c-sessions">0</dd></div><div><dt>Viewers</dt><dd id="c-viewers">0</dd></div><div><dt>Relay this hour</dt><dd id="c-relay">0 B</dd></div></dl>
<div class="hosts" id="op-hosts"></div>
<div class="nohosts hidden" id="op-nohosts"><div class="title"><span class="dot idle"></span><b>No hosts connected yet</b></div><p class="small muted">A home machine appears here a few seconds after it connects. What it needs, if anything, is under "Sharing from your machine?"</p></div>
</div>
</section>
</aside>
</div>
</main>
<script>
(function(){
var KEY='conductor.switchyard.token',timer=null;
var $=function(id){return document.getElementById(id)};
function bytes(n){if(n<1024)return n+' B';if(n<1048576)return (n/1024).toFixed(1)+' KB';if(n<1073741824)return (n/1048576).toFixed(1)+' MB';return (n/1073741824).toFixed(1)+' GB'}
function since(iso){var d=new Date(iso),now=new Date(),p=function(x){return (x<10?'0':'')+x};
if(d.toDateString()===now.toDateString())return 'since '+p(d.getHours())+':'+p(d.getMinutes());
return 'since '+d.toLocaleDateString(undefined,{month:'short',day:'numeric'})}
function show(view){$('op-anon').classList.toggle('hidden',view);$('op-view').classList.toggle('hidden',!view);$('op-forget').classList.toggle('hidden',!view)}
function render(d){$('c-hosts').textContent=d.hosts;$('c-sessions').textContent=d.sessions;$('c-viewers').textContent=d.viewers;$('c-relay').textContent=bytes(d.relayBytesHour||0);
var list=$('op-hosts');list.textContent='';var hs=d.hostList||[];
$('op-nohosts').classList.toggle('hidden',hs.length>0);
hs.forEach(function(h){var row=document.createElement('div');row.className='host';
var line=document.createElement('div');line.className='line';var dot=document.createElement('span');dot.className='dot';var name=document.createElement('span');name.className='name';name.textContent=h.name;var when=document.createElement('span');when.className='since';when.textContent=since(h.since);line.appendChild(dot);line.appendChild(name);line.appendChild(when);
var det=document.createElement('span');det.className='detail';det.textContent=h.sessions+(h.sessions===1?' session':' sessions')+' · '+h.viewers+(h.viewers===1?' viewer':' viewers');
row.appendChild(line);row.appendChild(det);list.appendChild(row)})}
function fetchStatus(token,onRefused){fetch('/api/switchyard/status',{headers:{Authorization:'Bearer '+token},cache:'no-store'}).then(function(r){if(r.status===401){onRefused();return null}return r.ok?r.json():null}).then(function(d){if(d){render(d);show(true)}}).catch(function(){})}
function schedule(){clearInterval(timer);timer=setInterval(function(){var t=load();if(t&&document.visibilityState==='visible')fetchStatus(t,forget)},30000)}
function load(){try{return localStorage.getItem(KEY)||''}catch(e){return ''}}
function forget(){try{localStorage.removeItem(KEY)}catch(e){}clearInterval(timer);show(false)}
$('op-form').addEventListener('submit',function(ev){ev.preventDefault();var t=$('op-token').value.trim();if(!t)return;$('op-refused').classList.add('hidden');
fetchStatus(t,function(){$('op-refused').classList.remove('hidden');$('op-token').focus()});
fetch('/api/switchyard/status',{headers:{Authorization:'Bearer '+t},cache:'no-store'}).then(function(r){if(r.ok){try{localStorage.setItem(KEY,t)}catch(e){}$('op-token').value='';schedule()}}).catch(function(){})});
$('op-forget').addEventListener('click',function(ev){ev.preventDefault();forget()});
var saved=load();if(saved){fetchStatus(saved,forget);schedule()}
})();
</script>
{{template "foot" .}}{{end}}

{{define "notfound"}}{{template "head" .}}
<main class="sy narrow">
<div class="hero">
<span class="mono small muted">404 · {{.Path}}</span>
<h1>The workbench isn't here.</h1>
<p class="muted">This address is a Conductor switchyard. It introduces viewers to sessions on people's own machines and has no sessions, crews, yard, agents or settings of its own. Those live in Conductor on your computer.</p>
</div>
<div class="rows">
<div><b>Open Conductor on your computer</b><span class="mono small">http://localhost:8080</span><span class="small muted">The default address of a Conductor you run yourself. The desktop app opens its own window.</span></div>
<div><b>Don't have it yet?</b><div class="btns"><a class="btn" href="{{.AppURL}}">Get the desktop app</a><a class="btn ghost" href="{{.DocsURL}}">Read the docs</a></div></div>
<div><b>Were you sent a link?</b><span class="small muted">Open it exactly as you received it. Share links on this server start with <span class="mono">/join/</span>.</span></div>
</div>
<a class="back" href="/">← {{.Host}}</a>
</main>
{{template "foot" .}}{{end}}

{{define "title"}}{{if .Path}}Not here · Conductor switchyard{{else}}Conductor switchyard{{end}}{{end}}
`
