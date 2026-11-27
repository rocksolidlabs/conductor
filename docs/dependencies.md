# Pinned versions


| Dependency | Version |
|---|---|
| Go | `go 1.26` (module) with `toolchain go1.26.11` pinned in `go.mod`, so every machine and CI run the patch the standard library's vulnerability fixes need (the go command fetches it on its own; CI's `~1.26.0` with check-latest lags a new patch by days) |
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
| @uxuiprinciples/mcp-server (the design principles' MCP server, a tool a person may add to their own agent, with the pinned command in `docs/design/tools.md`; not checked in, not a module or package dependency, never shipped, so not in `THIRD_PARTY_NOTICES`; its own dependencies come by range) | 1.3.0 |
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
| @capacitor/core / @capacitor/ios / @capacitor/cli (the phone shell's native runtime and its tool, `mobile/`: 8.6.0, the newest release at least two weeks old; the iOS app's Capacitor and Cordova frameworks come from capacitor-swift-pm at the same version, through the Swift Package Manager, so the build needs no Ruby; its revision is pinned by the committed `Package.resolved`, which every build reads with automatic resolution off, and its two binaries by the manifest's checksums) | 8.6.0 / 8.6.0 / 8.6.0 |
| @capacitor/push-notifications / @capacitor/app / @capacitor/browser (the shell's push token and notification taps; opened links; the in-app browser for Open a server and a provider's sign-in page) | 8.1.3 / 8.1.2 / 8.0.5 |
| typescript (`mobile/`, dev: the runtime's tool reads `capacitor.config.ts` with it; the version `desktop/` pins) | 5.9.3 |
| @capacitor/android (the phone shell's Android app, `mobile/android`, OSS-3: the native runtime's Android library at the version `@capacitor/core` and `@capacitor/ios` pin; built by Gradle from the package, with Apache Cordova's Android framework) | 8.6.0 |
| The Android app's build (`mobile/android`, as `@capacitor/cli` 8.6.0's template makes it): Gradle (the wrapper's distribution pinned by its checksum) / the Android Gradle plugin / compileSdk and targetSdk / minSdk (the template's own, Android 7.0; the plan names none) / the Google services plugin (applied only when a packaging adds its `google-services.json`) | 8.14.3 / 8.13.0 / 36 / 24 / 4.4.4 |
| Kotlin (the Android shell's own code, as the plan's decision on the second platform says; its Gradle plugin and the standard library it adds to the app) | 2.2.20 |
| androidx.biometric (the person's unlock before the key signs: BiometricPrompt with the device's PIN, pattern or password allowed) / androidx.webkit (the web message listener the bridge gate takes over, and the document-start scripts the runtime needs; the version the runtime pins) | 1.1.0 / 1.14.0 |
| org.bouncycastle:bcprov-jdk18on (Ed25519 for a key sealed by a keystore AES-GCM key, where the phone's keystore has no Ed25519 of its own: before Android 13, or a phone whose secure hardware lacks it; the platform offers none there; its lightweight API alone) | 1.82 |
| com.google.firebase:firebase-messaging / androidx.browser (pinned in `variables.gradle` at the versions `@capacitor/push-notifications` 8.1.3 and `@capacitor/browser` 8.0.5 name: the push token and taps; Custom Tabs for the in-app browser) | 25.0.1 / 1.9.0 |
| The Android app's tests: JUnit / org.json (the JVM tests parse with it as the phone's own does; the version the runtime's own tests use) / androidx.test ext-junit and Espresso (the boot check on an emulator; the template's) | 4.13.2 / 20250517 / 1.3.0 and 3.7.0 |
| CI's Android tools: actions/setup-java (Temurin 21) / gradle/actions/setup-gradle (Gradle's cache and the wrapper's validation) / reactivecircus/android-emulator-runner (the emulator, API 34, Google APIs, x86_64) | v4 / v4 / v2 |

Upgrade deliberately and update this table.
