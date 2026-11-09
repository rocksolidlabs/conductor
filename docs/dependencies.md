# Pinned versions


| Dependency | Version |
|---|---|
| Go | `go 1.26` (module) with `toolchain go1.26.10` pinned in `go.mod`, so every machine and CI run the patch the standard library's vulnerability fixes need (the go command fetches it on its own; CI's `~1.26.0` with check-latest lags a new patch by days) |
| honnef.co/go/tools (staticcheck, `make lint-static`) | v0.8.1 (2026.2.1) |
| golang.org/x/vuln (govulncheck, `make vuln`) | v1.8.0 |
| github.com/pion/webrtc/v4 | v4.2.21 |
| github.com/creack/pty | v1.1.24 |
| github.com/coder/websocket | v1.8.15 |
| github.com/pion/stun/v4 (direct for the reach lookup) | v4.0.1 |
| github.com/pion/ice/v4 (direct for the UDP mux type) | v4.4.4 |
| github.com/go-acme/lego/v5 (core, tls-alpn-01, http-01, dns-01 with cloudflare, exec, httpreq) | v5.5.2 |
| github.com/neovim/go-client (the editor's Neovim bridge: `nvim --embed` over msgpack-rpc) | v1.2.1 |
| github.com/go-git/go-git/v5 (the Files tab's Commits: the log and a commit's files; status and show stay with the git binary, which is faster for them) | v5.19.3 |
| modernc.org/sqlite (the managed runs' ledger, `internal/crew/ledger`, Dispatch's store, `internal/dispatch`, and the history's index, `internal/history` (HS1, a cache rebuilt from the stores), the three importers: SQLite translated to Go, so `CGO_ENABLED=0` stays; the owner chose it on 2026-10-22, #334; FTS5 and JSON1 are compiled in, checked 2026-10-28 for Dispatch's search) | v1.61.0 |
| Neovim (`nvim`, found on PATH at runtime; CI installs it for the bridge's tests) | v0.10.4 in CI; 0.10 or later on a machine |
| github.com/letsencrypt/pebble/v2 (test CA, `make test-pebble`; not a module dependency) | v2.10.1 |
| nuxt / @nuxt/ui / vue | 4.5.2 / 4.11.2 / 3.5.43 |
| reka-ui (declared at the version @nuxt/ui ships: the editor's tab menu closes itself through `injectContextMenuRootContext`, so Escape inside it never reaches the page's shortcuts, #330) | 2.10.4 |
| @xterm/xterm (+ fit, webgl, web-links) | 6.0.0 (0.11.0, 0.19.0, 0.12.0) |
| shiki | 4.4.3 |
| markdown-it (the editor's Markdown preview, #166: raw HTML parsed as text, scripted links refused) | 15.0.3 |
| mermaid (the preview's diagrams, run only in a sandboxed frame under a no-fetch Content Security Policy, loaded at the first diagram; `securityLevel: strict`) | 12.2.0 |
| vite (declared as a dev dependency at the version Nuxt ships: `nuxt.config.ts` calls its library build to bundle the Mermaid renderer as one classic script for the dev server) | 8.3.1 |
| @vue-flow/core (+ background, controls) | 1.48.2 (1.3.2, 1.1.3) |
| @iconify-json/lucide (icon client bundle) | 1.2.137 |
| monaco-editor (the Files editor, loaded with the first file) | 0.57.0 |
| @playwright/test (e2e; Chromium revision 1117, 125.0.6422.26) | 1.44.1 |
| electron / electron-builder (desktop) | 44.5.1 / 26.15.3 |
| @types/node (e2e type check) | 22.20.5 |

Upgrade deliberately and update this table.
