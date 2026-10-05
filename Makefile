APP := godex
VERSION ?= v1.5.0
DIST_DIR ?= dist
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X github.com/tim5wang/godex/internal/version.Version=$(VERSION) -X github.com/tim5wang/godex/internal/version.Commit=$(COMMIT) -X github.com/tim5wang/godex/internal/version.Date=$(BUILD_DATE)
PPROF_FLAG = $(if $(strip $(PPROF_ADDR)),--pprof-addr=$(PPROF_ADDR),)
GO_PACKAGES := ./cmd/... ./examples/... ./internal/...
GO_TEST_PARALLEL ?= 4
WEB_TEST_MAX_WORKERS ?= 4

.PHONY: dev dev-fast dev-voice dev-frontend web web-dev web-typecheck web-bundle-check web-clean docs-dev docs-build docs-check smoke verify build-linux build-minimal release release-clean deploy-linux

# ── Web UI build targets ───────────────────────────────────────────

## web:     Full production build (tsc type-check + vite bundle)
web:
	cd ui/web && corepack pnpm build

## web-dev: Quick build for development (skip tsc, vite bundle only)
web-dev:
	cd ui/web && corepack pnpm run dev:build

## web-typecheck: Run TypeScript type-checking only (CI gate)
web-typecheck:
	cd ui/web && corepack pnpm run typecheck

## web-bundle-check: Build the embedded Web UI and enforce cold-route gzip budgets
web-bundle-check:
	cd ui/web && corepack pnpm exec vite build --manifest
	node scripts/check_web_bundle.mjs

## web-clean: Remove all web build artifacts
web-clean:
	rm -rf ui/web/dist
	rm -rf internal/uiassets/embedded_dist/assets
	rm -f internal/uiassets/embedded_dist/index.html

## docs-dev: Run the VitePress documentation site locally
docs-dev:
	cd docs && corepack pnpm install --frozen-lockfile && corepack pnpm dev

## docs-build: Build the VitePress documentation site
docs-build:
	cd docs && corepack pnpm install --frozen-lockfile && corepack pnpm build

## docs-check: Validate documentation status headers, index coverage, and local Markdown links
docs-check:
	./scripts/check_docs.sh

## smoke: List smoke scenarios, or run one with `make smoke SCENARIO=self`
smoke:
	@bash scripts/smoke.sh $(if $(SCENARIO),$(SCENARIO),list)

## verify: Run the local release-quality test, lint, docs, and Web UI gates
verify:
	go test -p $(GO_TEST_PARALLEL) $(GO_PACKAGES) -count=1
	go vet $(GO_PACKAGES)
	$(MAKE) docs-check
	$(MAKE) docs-build
	cd ui/web && corepack pnpm run typecheck
	cd ui/web && corepack pnpm exec vitest run --maxWorkers=$(WEB_TEST_MAX_WORKERS)
	$(MAKE) web-bundle-check

# ── Development targets ────────────────────────────────────────────

## dev-frontend: Start Vite dev server with HMR (standalone, use with a separate Go backend)
dev-frontend:
	cd ui/web && corepack pnpm dev

## dev-fast: Quick rebuild + service restart (skip tsc type-check, uses web-dev)
# NOTE: use `mv` (atomic rename) to publish the binary, never `cp`/in-place
# write. On macOS a concurrent exec while the destination file is being
# written is killed by the kernel (OS_REASON_CODESIGNING / SIGKILL, exit
# 137), which surfaces as a mysteriously failing `service restart`.
dev-fast: web-dev
	go build -ldflags "$(LDFLAGS)" -o $(APP).new ./cmd/godex \
		&& mv $(APP).new $(APP) \
		&& (./$(APP) service restart $(PPROF_FLAG) 2>/dev/null || (./$(APP) service install && ./$(APP) service start $(PPROF_FLAG)))

## dev: Full rebuild + service restart (with tsc type-check, legacy)
dev: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP).new ./cmd/godex \
		&& mv $(APP).new $(APP) \
		&& (./$(APP) service restart $(PPROF_FLAG) 2>/dev/null || (./$(APP) service install && ./$(APP) service start $(PPROF_FLAG)))

## dev-voice: Start local voice-engine, rebuild/restart Godex, and supervise the engine until Ctrl-C
VOICE_ENGINE_DIR ?= ../voice-engine
VOICE_ENGINE_ADDR ?= 127.0.0.1:17021
VOICE_ASR_MODEL ?= asr/zipformer-small-zh-en
dev-voice:
	VOICE_ENGINE_DIR="$(VOICE_ENGINE_DIR)" VOICE_ENGINE_ADDR="$(VOICE_ENGINE_ADDR)" VOICE_ASR_MODEL="$(VOICE_ASR_MODEL)" PPROF_ADDR="$(PPROF_ADDR)" bash scripts/dev_voice.sh

# ── Release targets ────────────────────────────────────────────────

## build-linux: Full build for Linux amd64
build-linux: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP)-linux-amd64 ./cmd/godex

## build-minimal: Build the smallest possible binary (Linux: UPX --best --lzma; macOS: UPX incompatible, stripped only)
build-minimal:
	@echo "[minimal] building stripped binary..."
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP).new ./cmd/godex
	mv $(APP).new $(APP)
	@echo "[minimal] binary size before compression: $$(wc -c < $(APP) | tr -d ' ') bytes"
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "[minimal] UPX is incompatible with macOS code-signing (binary would be killed by kernel)"; \
		echo "[minimal] skipping compression — cross-compile on Linux for best results"; \
	elif command -v upx >/dev/null 2>&1; then \
		echo "[minimal] UPX compressing with --best --lzma..."; \
		upx --best --lzma $(APP); \
	else \
		echo "[minimal] UPX not installed — skipping compression (apt install upx-ucl)"; \
	fi
	@echo "[minimal] final binary: $$(ls -lh $(APP) | awk '{print $$5}')"

release: release-clean web
	mkdir -p "$(DIST_DIR)"
	set -eu; \
	build() { \
		platform="$$1"; goos="$$2"; goarch="$$3"; ext="$$4"; \
		pkg="$(APP)-$(VERSION)-$$platform"; \
		stage="$(DIST_DIR)/.stage/$$pkg"; \
		mkdir -p "$$stage"; \
		echo "[release] build $$platform ($$goos/$$goarch)"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$stage/$(APP)$$ext" ./cmd/godex; \
		cp README.md "$$stage/"; \
		node scripts/release_manifest.mjs package \
			--stage-dir "$$stage" \
			--app "$(APP)" \
			--version "$(VERSION)" \
			--commit "$(COMMIT)" \
			--build-date "$(BUILD_DATE)" \
			--platform "$$platform" \
			--binary "$(APP)$$ext"; \
		tar -cf - -C "$(DIST_DIR)/.stage" "$$pkg" | gzip -9 > "$(DIST_DIR)/$$pkg.tar.gz"; \
	}; \
	build win-x86-64 windows amd64 .exe; \
	build mac-x86-64 darwin amd64 ""; \
	build mac-apple darwin arm64 ""; \
	build linux-x86-64 linux amd64 ""; \
	rm -rf "$(DIST_DIR)/.stage"; \
	node scripts/release_manifest.mjs index \
		--dist-dir "$(DIST_DIR)" \
		--app "$(APP)" \
		--version "$(VERSION)" \
		--commit "$(COMMIT)" \
		--build-date "$(BUILD_DATE)"; \
	ls -lh "$(DIST_DIR)"/*.tar.gz

release-clean:
	rm -rf "$(DIST_DIR)"

deploy-linux: build-linux
	@echo "[deploy] compressing godex-linux-amd64 (gzip)..."
	@gzip -c godex-linux-amd64 > godex-linux-amd64.gz
	@echo "[deploy] binary: $$(ls -lh godex-linux-amd64 | awk '{print $$5}') -> gz: $$(ls -lh godex-linux-amd64.gz | awk '{print $$5}')"
	# Only the systemd unit (system scope, Caddy -> :3801) must run. Installing the
	# user-scope service too would start a second instance that shares
	# /root/.godex/control/nodes.json and overwrites the relay status written by
	# the system instance, causing intermittent 503 "node offline" errors.
	scp godex-linux-amd64.gz mycloud:/tmp/godex-linux-amd64.gz \
	&& ssh mycloud "gunzip -f /tmp/godex-linux-amd64.gz && mv /tmp/godex-linux-amd64 /opt/godex/godex && chmod +x /opt/godex/godex && cd /opt/godex && ./godex service uninstall && systemctl stop godex.service && systemctl start godex.service" \
	&& rm -f godex-linux-amd64.gz godex-linux-amd64
