.PHONY: build build-server build-agent build-ui build-full test test-short migrate-up dev-up dev-down

SERVER_BIN=bin/nat-backup-server
AGENT_BIN=bin/nat-backup-agent

build: build-server build-agent

build-server:
	go build -o $(SERVER_BIN) ./cmd/server

build-agent:
	go build -o $(AGENT_BIN) ./cmd/agent

build-agent-windows:
	GOOS=windows GOARCH=amd64 go build -o bin/nat-backup-agent.exe ./cmd/agent

build-ui:
	cd web && npm install && npm run build
	rm -rf internal/api/static/dist
	cp -r web/dist internal/api/static/dist

build-full: build-ui build-server build-agent

test:
	go test ./... -v

test-short:
	go test ./... -short -v

dev-up:
	docker compose up -d postgres

dev-down:
	docker compose down
