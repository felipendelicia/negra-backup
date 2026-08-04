.PHONY: build build-server build-agent build-agent-windows build-ui build-ui-ci embed-ui build-full \
        test test-short test-race cover lint fmt fmt-check vet migrate-up dev-up dev-down clean

SERVER_BIN=bin/nat-backup-server
AGENT_BIN=bin/nat-backup-agent
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# `./...` also matches web/node_modules, which vendors Go sources (the `flatted`
# package). Every Go tool invocation must use these scoped patterns instead.
GO_PKGS := ./cmd/... ./internal/...
GO_DIRS := ./cmd ./internal ./migrations

# ── Build ─────────────────────────────────────────────────────────────────────

build: build-server build-agent

build-server:
	go build $(LDFLAGS) -o $(SERVER_BIN) ./cmd/server

build-agent:
	go build $(LDFLAGS) -o $(AGENT_BIN) ./cmd/agent

build-agent-windows:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/nat-backup-agent.exe ./cmd/agent

build-ui:
	cd web && npm install && npm run build
	$(MAKE) embed-ui

# Same as build-ui but with a reproducible install; used by CI/release.
build-ui-ci:
	cd web && npm ci && npm run build
	$(MAKE) embed-ui

# Copies the Vite output where cmd/server picks it up via `go:embed all:dist`
# (internal/api/static/embed.go).
embed-ui:
	rm -rf internal/api/static/dist
	cp -r web/dist internal/api/static/dist

# Recursive $(MAKE) calls instead of prerequisites so the UI is always embedded
# before the server binary is compiled, even under `make -j`.
build-full:
	$(MAKE) build-ui
	$(MAKE) build-server
	$(MAKE) build-agent

# ── Test ──────────────────────────────────────────────────────────────────────

test:
	go test $(GO_PKGS) -v

test-short:
	go test $(GO_PKGS) -short -v

test-race:
	go test -race $(GO_PKGS)

cover:
	go test -coverprofile=coverage.out -covermode=atomic $(GO_PKGS)
	go tool cover -func=coverage.out | tail -1
	@echo "HTML report: go tool cover -html=coverage.out"

# ── Lint / format ─────────────────────────────────────────────────────────────

vet:
	go vet $(GO_PKGS)

fmt:
	gofmt -w $(GO_DIRS)

fmt-check:
	@unformatted=$$(gofmt -l $(GO_DIRS)); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-formatted (run 'make fmt'):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi; \
	echo "All Go files are gofmt-clean."

# Full lint: Go formatting + vet, plus the web ESLint config.
lint: fmt-check vet
	cd web && npm run lint

# ── Dev infra ─────────────────────────────────────────────────────────────────

# Applies pending migrations against $DATABASE_URL. The server also runs these
# at startup; this target is for running them standalone.
migrate-up:
	go run ./cmd/migrate up

dev-up:
	docker compose up -d postgres

dev-down:
	docker compose down

clean:
	rm -rf bin coverage.out
