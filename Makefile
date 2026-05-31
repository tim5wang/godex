APP := godex
VERSION ?= v1.1.0
DIST_DIR ?= dist
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X github.com/tim5wang/godex/internal/version.Version=$(VERSION) -X github.com/tim5wang/godex/internal/version.Commit=$(COMMIT) -X github.com/tim5wang/godex/internal/version.Date=$(BUILD_DATE)

.PHONY: dev web build-linux release release-clean deploy-linux

web:
	pnpm --dir ui/web build

dev: web
	go build -ldflags "$(LDFLAGS)" -o $(APP) ./cmd/godex \
	&& ./godex service uninstall && ./godex service install && ./godex service start

build-linux: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP)-linux-amd64 ./cmd/godex

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
		cp README.md README.en.md "$$stage/"; \
		tar -cf - -C "$(DIST_DIR)/.stage" "$$pkg" | gzip -9 > "$(DIST_DIR)/$$pkg.tar.gz"; \
	}; \
	build win-x86-64 windows amd64 .exe; \
	build mac-x86-64 darwin amd64 ""; \
	build mac-apple darwin arm64 ""; \
	build linux-x86-64 linux amd64 ""; \
	rm -rf "$(DIST_DIR)/.stage"; \
	ls -lh "$(DIST_DIR)"/*.tar.gz

release-clean:
	rm -rf "$(DIST_DIR)"

deploy-linux: build-linux
	ssh mycloud "rm /opt/godex/godex" && scp godex-linux-amd64 mycloud:/opt/godex/godex \
	&& ssh mycloud "cd /opt/godex && ./godex service uninstall && ./godex service install --addr 127.0.0.1:3800 && ./godex service start" \
	&& ssh mycloud "systemctl stop godex.service && systemctl start godex.service" && rm godex-linux-amd64
