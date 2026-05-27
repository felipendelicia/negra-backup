# nat-backup

Self-hosted backup manager. A central server orchestrates agents running on your machines — agents execute backups and push archives to configured storage destinations.

## Features

- **File backups** — tar + compress (gzip or zstd) + optional AES-256-GCM encryption
- **Database backups** — PostgreSQL, MySQL, SQLite, MongoDB
- **Storage backends** — local filesystem, S3-compatible, SFTP
- **Cron scheduling** — standard cron expressions per job
- **Retention** — automatic cleanup of old runs
- **Email notifications** — on backup failure
- **Web UI** — dashboard, agents, jobs, run history with live logs
- **Real-time logs** — WebSocket stream while a backup runs

## How it works

```
┌─────────────────┐        WebSocket         ┌─────────────────┐
│   nat-backup    │ ◄──── agent connects ──── │  nat-backup     │
│   server        │ ──── dispatch job ──────► │  agent          │
│  (PostgreSQL)   │ ◄──── progress/done ───── │  (on host)      │
└─────────────────┘                           └─────────────────┘
        │                                             │
   Web UI (React)                            backup → storage
```

Agents connect outbound to the server — no inbound firewall rules needed on agent hosts.

## Quick start

### 1. Start the server

```bash
# Start Postgres
docker compose up -d postgres

# Configure
cp .env.example .env
# Edit .env: set JWT_SECRET, ENCRYPTION_KEY (64-char hex), ADMIN_PASSWORD

# Build and run
make build-full
export $(cat .env | xargs)
./bin/nat-backup-server
```

Open `http://localhost:8080` — login with `admin` / your `ADMIN_PASSWORD`.

### 2. Add an agent

In the Web UI → **Agents** → create agent → copy the API key.

On the machine to back up:

```bash
# Download the agent binary (or build it: make build-agent)
cat > agent.yaml <<EOF
server_url: http://your-server:8080
api_key: <paste-api-key>
EOF

./nat-backup-agent agent.yaml

# Install as system service (Linux/Windows)
./nat-backup-agent install
```

### 3. Create a job

Web UI → **Jobs** → configure source (files or database), storage destination, cron schedule.

## Configuration

### Server (environment variables)

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL connection string |
| `JWT_SECRET` | Secret for signing JWT tokens (min 32 chars) |
| `ENCRYPTION_KEY` | 64-char hex key for encrypting storage configs and passphrases at rest |
| `ADMIN_PASSWORD` | Initial admin password |
| `PORT` | HTTP port (default `8080`) |
| `TLS_ENABLED` | `true` to enable TLS |
| `TLS_CERT_FILE` | Path to TLS certificate |
| `TLS_KEY_FILE` | Path to TLS key |

### Agent (`agent.yaml`)

```yaml
server_url: https://your-server.example.com
api_key: your-api-key
```

## Building

```bash
make build              # server + agent
make build-full         # UI + server + agent
make build-agent-windows  # Windows agent cross-compile
```

## Development

```bash
make dev-up             # start Postgres via Docker
make test               # all tests
make test-short         # skip tests requiring external services
```

## Storage destinations

| Type | Config fields |
|------|--------------|
| `local` | `path` — directory on the **agent's** host |
| `s3` | `endpoint`, `bucket`, `region`, `access_key`, `secret_key` |
| `sftp` | `host`, `port`, `user`, `password` or `private_key`, `path` |

When using `local` storage with the server as the destination, the agent uploads via `POST /api/upload/{run_id}`.

## Security

- Storage configs and backup passphrases are AES-256-GCM encrypted at rest using `ENCRYPTION_KEY`.
- Decrypted values are only held in-process and transmitted over the existing authenticated WebSocket connection.
- Agents authenticate with per-agent API keys.
- Web UI uses short-lived JWTs.
