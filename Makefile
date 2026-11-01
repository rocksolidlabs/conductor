VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/rocksolidlabs/conductor/internal/version.Version=$(VERSION) \
           -X github.com/rocksolidlabs/conductor/internal/version.Commit=$(COMMIT)
GO_FILES := $(shell find . -name '*.go' -not -path './web/*')
GO_MIN   := $(shell awk '/^go /{print $$2}' go.mod)
NODE_MIN := 22
NODE_STAMP := web/node_modules/.package-lock.json

.PHONY: help deps check-tools build build-go check-version notices web-install web-build web-typecheck web-dev run dev test test-web test-e2e test-live test-pebble test-network test-recipes desktop-install desktop-dev desktop-test desktop-binaries desktop-dist desktop-e2e lint lint-static vuln fmt generate docker clean preflight

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

check-tools: ## verify go, node, npm and python3 are installed at supported versions
	@ok=1; \
	for t in go node npm python3; do \
	  command -v $$t >/dev/null 2>&1 || { echo "missing: $$t"; ok=0; }; \
	done; \
	[ $$ok = 1 ] || exit 1; \
	gov=$$(go env GOVERSION | sed 's/^go//'); \
	[ "$$(printf '%s\n%s\n' "$(GO_MIN)" "$$gov" | sort -V | head -n1)" = "$(GO_MIN)" ] \
	  || { echo "go $$gov is older than $(GO_MIN) (go.mod)"; exit 1; }; \
	nodev=$$(node -p 'process.versions.node'); \
	[ "$${nodev%%.*}" -ge $(NODE_MIN) ] \
	  || { echo "node $$nodev is older than $(NODE_MIN)"; exit 1; }; \
	echo "go $$gov, node $$nodev, npm $$(npm --version), python3 $$(python3 -c 'import platform; print(platform.python_version())')"

deps: check-tools web-install ## verify tools, then install Go modules and npm packages
	go mod download

build: web-build build-go ## build the single binary with the embedded UI

build-go: ## build the Go binary using whatever UI is in internal/web/dist
	@touch internal/web/dist/.gitkeep
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/conductor ./cmd/conductor

check-version: ## the built binary reports the version and commit make injected (a missed -X site would say dev (unknown))
	@got="$$(bin/conductor version | sed -n 1p)"; want="conductor $(VERSION) ($(COMMIT))"; \
	if [ "$$got" != "$$want" ]; then echo "check-version: got '$$got', want '$$want'"; exit 1; fi; echo "check-version: $$got"

web-install: ## reproducible npm install
	cd web && npm ci

$(NODE_STAMP): web/package.json web/package-lock.json
	cd web && npm ci

web-build: $(NODE_STAMP) ## generate the static SPA and copy it into internal/web/dist
	cd web && npm run generate
	find internal/web/dist -mindepth 1 -not -name .gitkeep -delete
	cp -r web/.output/public/. internal/web/dist/
	touch internal/web/dist/.gitkeep

web-typecheck: $(NODE_STAMP) ## vue-tsc type check
	cd web && npm run typecheck

web-dev: $(NODE_STAMP) ## nuxt dev server on :3000 proxying to :8080
	cd web && npm run dev

run: ## run the API server in dev mode against conductor.example.json
	go run ./cmd/conductor serve --config conductor.example.json --dev --log-level debug

dev: ## instructions for two-process development
	@echo "Terminal 1: make run      (Go API on :8080, --dev)"
	@echo "Terminal 2: make web-dev  (Nuxt on :3000, proxies /api and /ws)"

test: ## race-enabled Go tests (the bound catches a hang: docs/checks.md)
	go test -race -count=1 -timeout 30m ./...

preflight: ## every check CI runs, here, from the committed head, with a receipt and a log per step (scripts/preflight.py; --machines mac,windows for the other machines)
	python3 scripts/preflight.py

test-web: web-typecheck ## frontend checks

test-e2e: web-build build-go ## Playwright suite (web/e2e): the built server with stub agents, in Chromium 1117
	cd web && npm run typecheck:e2e && npm run test:e2e

test-live: build-go ## the live tier: the real Claude Code and Codex (CONDUCTOR_E2E_LIVE_REPO names a repository they trust; keys in ANTHROPIC_API_KEY / OPENAI_API_KEY or their logins)
	cd web && CONDUCTOR_E2E_LIVE=1 npx playwright test e2e/live.spec.ts

desktop-install: ## npm ci for the desktop shell (Electron)
	cd desktop && npm ci

desktop-dev: build-go ## run the desktop app against the checkout's bin/conductor
	cd desktop && npm run start

desktop-test: ## the desktop shell's type check and unit tests
	cd desktop && npm run typecheck && npm test

notices: web-build ## THIRD_PARTY_NOTICES from what the binary, the web bundle and the desktop app ship (npm ci in desktop/ first)
	python3 scripts/notices.py

desktop-e2e: build-go ## the desktop shell's smoke in Electron (needs a display: xvfb-run on Linux)
	cd desktop && npm run build && npm run test:e2e

# The server binaries the packages bundle, under the names electron-builder's ${os}-${arch} macros give.
desktop-binaries: ## cross-compile the server for the desktop packages (linux and mac, x64 and arm64)
	@touch internal/web/dist/.gitkeep
	@for t in linux/amd64/linux-x64 linux/arm64/linux-arm64 darwin/amd64/mac-x64 darwin/arm64/mac-arm64; do \
	  goos=$${t%%/*}; rest=$${t#*/}; goarch=$${rest%%/*}; dir=bin/desktop/$${rest#*/}; mkdir -p $$dir; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/conductor ./cmd/conductor && echo "built $$dir/conductor"; \
	done

desktop-dist: web-build desktop-binaries ## package the desktop app for this platform (electron-builder; unsigned without the secrets)
	cd desktop && npm run dist

PEBBLE_VERSION ?= v2.10.1
test-pebble: ## certificates from Let's Encrypt's Pebble (installs it with go install when missing)
	@command -v pebble >/dev/null 2>&1 || [ -n "$$CONDUCTOR_PEBBLE" ] || go install github.com/letsencrypt/pebble/v2/cmd/pebble@$(PEBBLE_VERSION)
	PATH="$$(go env GOPATH)/bin:$$PATH" go test -race -count=1 -tags pebble -run Pebble ./internal/certs/

STATICCHECK_VERSION ?= v0.8.1
GOVULNCHECK_VERSION ?= v1.8.0
lint-static: ## staticcheck (pinned)
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

vuln: ## govulncheck against the Go vulnerability database (needs the network)
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

test-network: ## the built-in agents' sites, on the network (nightly)
	go test -race -count=1 -tags network -run Sites ./internal/catalog/

test-recipes: ## the recipe flags against the CLIs on this machine (nightly; CONDUCTOR_RECIPES_REQUIRE names one that must be there)
	go test -count=1 -tags recipes -run Recipe -v ./internal/catalog/

lint: ## gofmt, go vet and the open-core rules (internal/opencore)
	@out="$$(gofmt -l $(GO_FILES))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	go test -count=1 -run '^TestModuleKeepsTheRules$$' ./internal/opencore

fmt: ## gofmt all Go files
	gofmt -w $(GO_FILES)

generate: ## go generate (regenerates the web bundle)
	go generate ./internal/web

docker: ## build the container image
	docker build -t conductor:$(VERSION) .

clean:
	rm -rf bin web/.nuxt web/.output
	find internal/web/dist -mindepth 1 -not -name .gitkeep -delete
