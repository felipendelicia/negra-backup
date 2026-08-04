#!/usr/bin/env bash
# install.sh — installs or updates nat-backup-server and nat-backup-agent on Linux.
#
# Basic usage:
#   curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | sudo bash
#
# Agent only, configured and registered as a systemd service:
#   curl -fsSL .../install.sh | sudo bash -s -- --agent-only --server-url http://HOST:8080 --api-key YOUR_KEY
#
# The API key can also be supplied through the NAT_BACKUP_API_KEY environment
# variable so it never shows up in the process list or the shell history.

set -euo pipefail

REPO="felipendelicia/negra-backup"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/nat-backup"
CONFIG_FILE="$CONFIG_DIR/agent.yaml"
AGENT_SERVICE="nat-backup-agent"

# Platforms the release workflow actually builds (.github/workflows/release.yml).
# Keep in sync with that file: asking for an asset that is never published makes
# the download fail with a confusing 404.
SERVER_ARCHS="amd64"
AGENT_ARCHS="amd64 arm64"

# Candidate names for the release checksum manifest. The release does not
# publish one today; if any of these appears the download is verified against it.
CHECKSUM_ASSETS="SHA256SUMS checksums.txt SHA256SUMS.txt"

AGENT_ONLY=false
SERVER_URL=""
API_KEY="${NAT_BACKUP_API_KEY:-}"

# ── Output helpers ────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; NC=$'\033[0m'
else
  GREEN=""; YELLOW=""; RED=""; NC=""
fi
ok()   { printf '%s✓%s %s\n' "$GREEN" "$NC" "$*"; }
info() { printf '%s→%s %s\n' "$YELLOW" "$NC" "$*"; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$NC" "$*" >&2; }
err()  { printf '%s✗%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

Options:
  --agent-only            Install only nat-backup-agent (skip the server).
  --server-url <url>      Server URL the agent connects to (http:// or https://).
  --api-key <key>         Agent API key. Can also be given via NAT_BACKUP_API_KEY.
  -h, --help              Show this help.

When both --server-url and --api-key are supplied the agent is configured in
/etc/nat-backup/agent.yaml (mode 0600, root-owned), registered as a systemd
service and started. Re-running the installer is safe: it only replaces what
changed and restarts the service when the binary or the config was updated.
USAGE
}

# ── Arguments ─────────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --agent-only)   AGENT_ONLY=true ;;
    --server-url)   [ $# -ge 2 ] || err "Option --server-url requires a value."
                    SERVER_URL="$2"; shift ;;
    --server-url=*) SERVER_URL="${1#*=}" ;;
    --api-key)      [ $# -ge 2 ] || err "Option --api-key requires a value."
                    API_KEY="$2"; shift ;;
    --api-key=*)    API_KEY="${1#*=}" ;;
    -h|--help)      usage; exit 0 ;;
    *)              usage >&2; err "Unknown option: $1" ;;
  esac
  shift
done

CONFIGURE_AGENT=false
if [ -n "$SERVER_URL" ] || [ -n "$API_KEY" ]; then
  [ -n "$SERVER_URL" ] || err "An API key was given without --server-url. Both are required to configure the agent."
  [ -n "$API_KEY" ]    || err "--server-url was given without an API key. Both are required to configure the agent."
  case "$SERVER_URL" in
    http://*|https://*) ;;
    *) err "--server-url must start with http:// or https:// (got: $SERVER_URL)" ;;
  esac
  case "$SERVER_URL" in
    *[[:space:]]*) err "--server-url must not contain whitespace." ;;
  esac
  # The server issues 32-byte hex keys; keep the accepted charset tight so the
  # value cannot break out of the YAML string it is written into.
  case "$API_KEY" in
    *[!A-Za-z0-9._~+/=-]*) err "The API key contains unsupported characters. Expected only letters, digits and . _ ~ + / = -" ;;
  esac
  CONFIGURE_AGENT=true
fi

# ── Platform ──────────────────────────────────────────────────────────────────
OS="$(uname -s)"
[ "$OS" = "Linux" ] || err "Unsupported operating system: $OS. This installer only supports Linux; the release publishes Linux and Windows binaries only (use scripts/install-agent.ps1 on Windows)."

MACHINE="$(uname -m)"
case "$MACHINE" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $MACHINE. Released binaries exist for x86_64 (amd64) and aarch64 (arm64) only." ;;
esac

arch_supported() { # $1 = space-separated list, $2 = arch
  case " $1 " in *" $2 "*) return 0 ;; *) return 1 ;; esac
}

arch_supported "$AGENT_ARCHS" "$ARCH" \
  || err "No nat-backup-agent binary is released for linux/$ARCH (released: $AGENT_ARCHS)."

if [ "$AGENT_ONLY" = false ] && ! arch_supported "$SERVER_ARCHS" "$ARCH"; then
  err "No nat-backup-server binary is released for linux/$ARCH (the server is only built for: $SERVER_ARCHS). Re-run with --agent-only to install just the agent on this host."
fi

# ── Dependencies ──────────────────────────────────────────────────────────────
for cmd in curl grep sed tr uname mktemp head od install mv rm; do
  command -v "$cmd" >/dev/null 2>&1 || err "'$cmd' is required but not installed."
done

SHA256_CMD=""
if command -v sha256sum >/dev/null 2>&1; then
  SHA256_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SHA256_CMD="shasum -a 256"
fi

# ── Privileges ────────────────────────────────────────────────────────────────
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 \
    || err "Root privileges are required to write to $INSTALL_DIR and 'sudo' is not installed. Re-run as root."
  SUDO="sudo"
fi
run_priv() { if [ -n "$SUDO" ]; then sudo "$@"; else "$@"; fi; }

run_priv true || err "Could not obtain root privileges (sudo failed). Re-run as root."

# ── Temporary workspace ───────────────────────────────────────────────────────
TMP_DIR=""
STAGED=""
cleanup() {
  if [ -n "$TMP_DIR" ]; then rm -rf "$TMP_DIR"; fi
  if [ -n "$STAGED" ]; then run_priv rm -f "$STAGED" 2>/dev/null || true; fi
}
trap cleanup EXIT
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nat-backup-install.XXXXXX")"

download() { # $1 = url, $2 = destination
  curl -fsSL --retry 3 --retry-delay 2 --max-time 600 -o "$2" "$1"
}

# ── Latest release ────────────────────────────────────────────────────────────
info "Fetching the latest release..."
RELEASE_JSON="$TMP_DIR/release.json"
download "https://api.github.com/repos/$REPO/releases/latest" "$RELEASE_JSON" \
  || err "Could not query the GitHub releases API for $REPO. Check network access and that the repository has published releases."

LATEST="$(grep -m1 '"tag_name"' "$RELEASE_JSON" \
  | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')" || true
[ -n "$LATEST" ] || err "Could not read the latest version tag from the GitHub API response. Does $REPO have any published release?"
case "$LATEST" in
  *[!A-Za-z0-9._-]*) err "Unexpected release tag received from GitHub: '$LATEST'." ;;
esac
info "Latest version: $LATEST"

BASE_URL="https://github.com/$REPO/releases/download/$LATEST"

# ── Checksum manifest (verified when the release publishes one) ───────────────
CHECKSUMS_FILE=""
for asset in $CHECKSUM_ASSETS; do
  if download "$BASE_URL/$asset" "$TMP_DIR/$asset" 2>/dev/null && [ -s "$TMP_DIR/$asset" ]; then
    CHECKSUMS_FILE="$TMP_DIR/$asset"
    ok "Checksum manifest found in the release: $asset"
    break
  fi
done
if [ -z "$CHECKSUMS_FILE" ]; then
  warn "Release $LATEST publishes no checksum manifest ($CHECKSUM_ASSETS): downloads cannot be verified against a published hash. Falling back to transport and integrity checks (HTTPS, HTTP status, non-empty ELF binary that runs)."
elif [ -z "$SHA256_CMD" ]; then
  err "The release publishes a checksum manifest but neither 'sha256sum' nor 'shasum' is available to verify it. Install coreutils and re-run."
fi

sha256_of() { $SHA256_CMD "$1" | sed 's/[[:space:]].*//'; }

verify_checksum() { # $1 = asset name, $2 = downloaded file
  local asset="$1" file="$2" pattern expected actual
  [ -n "$CHECKSUMS_FILE" ] || return 0
  pattern="$(printf '%s' "$asset" | sed 's/[.[\*^$\\]/\\&/g')"
  expected="$(grep -E "[[:space:]][*]?${pattern}\$" "$CHECKSUMS_FILE" | head -1 | sed 's/[[:space:]].*//')" || true
  [ -n "$expected" ] || err "$asset has no entry in the release checksum manifest. Refusing to install an unverified binary."
  actual="$(sha256_of "$file")"
  [ "$expected" = "$actual" ] || err "Checksum mismatch for $asset. Expected $expected, got $actual. Refusing to install; the download may be corrupt or tampered with."
  ok "  checksum verified ($asset)"
}

is_elf() { # $1 = file
  [ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ]
}

# ── Binary installation ───────────────────────────────────────────────────────
RESTART_NEEDED=false

install_binary() { # $1 = command name, $2 = release asset name
  local name asset dest tmp current reported
  name="$1"
  asset="$2"
  dest="$INSTALL_DIR/$name"
  tmp="$TMP_DIR/$asset"

  if [ -x "$dest" ]; then
    current="$("$dest" version 2>/dev/null || echo "unknown")"
    if [ "$current" = "$LATEST" ]; then
      ok "$name is already at $LATEST, nothing to do."
      return 0
    fi
    info "Updating $name  $current → $LATEST..."
  else
    info "Installing $name $LATEST..."
  fi

  download "$BASE_URL/$asset" "$tmp" \
    || err "Failed to download $asset from $BASE_URL (HTTP error or network failure). Does release $LATEST publish that asset?"
  [ -s "$tmp" ] || err "$asset downloaded empty from $BASE_URL."
  is_elf "$tmp" || err "$asset is not a Linux executable (bad ELF magic). The download is corrupt or the release asset is wrong."
  verify_checksum "$asset" "$tmp"

  # Stage inside INSTALL_DIR: same filesystem as the destination, so the final
  # move is an atomic rename that also works while the old binary is running
  # (a plain copy over a busy executable fails with ETXTBSY).
  [ -d "$INSTALL_DIR" ] || run_priv install -d -m 0755 "$INSTALL_DIR"
  STAGED="$INSTALL_DIR/.$name.new"
  run_priv install -m 0755 -o root -g root "$tmp" "$STAGED" \
    || err "Could not stage $name into $INSTALL_DIR."

  reported="$(run_priv "$STAGED" version 2>/dev/null || echo "")"
  [ -n "$reported" ] || err "The downloaded $name does not run on this host (architecture mismatch or corrupt download). Aborting without touching $dest."
  [ "$reported" = "$LATEST" ] || warn "$name reports version '$reported' but the release tag is '$LATEST'."

  run_priv mv -f "$STAGED" "$dest" || err "Could not install $name into $dest."
  STAGED=""
  RESTART_NEEDED=true
  ok "$name installed at $dest (version $("$dest" version 2>/dev/null || echo "unknown"))"
}

if [ "$AGENT_ONLY" = false ]; then
  install_binary "nat-backup-server" "nat-backup-server-linux-$ARCH"
fi
install_binary "nat-backup-agent" "nat-backup-agent-linux-$ARCH"

# ── Agent configuration ───────────────────────────────────────────────────────
if [ "$CONFIGURE_AGENT" = true ]; then
  [ -d "$CONFIG_DIR" ] || run_priv install -d -m 0755 -o root -g root "$CONFIG_DIR"

  NEW_CONFIG="$TMP_DIR/agent.yaml"
  # Written with umask 077 so the key is not world-readable even in $TMPDIR.
  ( umask 077; cat > "$NEW_CONFIG" <<EOF
server_url: "$SERVER_URL"
api_key: "$API_KEY"
EOF
  )

  if [ -f "$CONFIG_FILE" ] && run_priv cmp -s "$NEW_CONFIG" "$CONFIG_FILE"; then
    ok "Configuration at $CONFIG_FILE is already up to date."
  else
    # install(1) creates the destination with the requested mode from the start,
    # so the API key is never readable by other local users, not even briefly.
    run_priv install -m 0600 -o root -g root "$NEW_CONFIG" "$CONFIG_FILE" \
      || err "Could not write $CONFIG_FILE."
    RESTART_NEEDED=true
    ok "Configuration saved to $CONFIG_FILE (mode 0600, root-owned)"
  fi
  rm -f "$NEW_CONFIG"
elif [ -f "$CONFIG_FILE" ]; then
  # Harden a config left behind by an older installer or written by hand.
  run_priv chown root:root "$CONFIG_FILE" 2>/dev/null || true
  run_priv chmod 0600 "$CONFIG_FILE" 2>/dev/null || true
fi

# ── systemd service ───────────────────────────────────────────────────────────
have_systemd() { command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; }
service_installed() { systemctl cat "$AGENT_SERVICE.service" >/dev/null 2>&1; }

wait_active() { # $1 = unit
  local unit="$1" state="" i=0
  while [ "$i" -lt 20 ]; do
    state="$(systemctl is-active "$unit" 2>/dev/null || true)"
    case "$state" in
      active) sleep 2
              [ "$(systemctl is-active "$unit" 2>/dev/null || true)" = "active" ] && return 0
              ;;
      failed) return 1 ;;
    esac
    sleep 1
    i=$((i + 1))
  done
  return 1
}

SERVICE_RUNNING=false
if have_systemd; then
  if [ "$CONFIGURE_AGENT" = true ] && ! service_installed; then
    info "Registering the systemd service $AGENT_SERVICE..."
    run_priv "$INSTALL_DIR/nat-backup-agent" install \
      || err "'nat-backup-agent install' failed; the systemd unit was not written."
    run_priv systemctl daemon-reload
    run_priv systemctl enable "$AGENT_SERVICE" >/dev/null \
      || err "Could not enable $AGENT_SERVICE."
    RESTART_NEEDED=true
  fi

  if service_installed; then
    if [ "$RESTART_NEEDED" = true ]; then
      info "Restarting $AGENT_SERVICE to pick up the new binary/config..."
      run_priv systemctl daemon-reload
      run_priv systemctl restart "$AGENT_SERVICE" \
        || err "Could not restart $AGENT_SERVICE. Inspect it with: journalctl -u $AGENT_SERVICE -n 50"
      if wait_active "$AGENT_SERVICE"; then
        SERVICE_RUNNING=true
        ok "$AGENT_SERVICE is running version $("$INSTALL_DIR/nat-backup-agent" version 2>/dev/null || echo "unknown")"
      else
        err "$AGENT_SERVICE did not stay active after the restart (state: $(systemctl is-active "$AGENT_SERVICE" 2>/dev/null || echo unknown)). Inspect it with: journalctl -u $AGENT_SERVICE -n 50"
      fi
    elif systemctl is-active --quiet "$AGENT_SERVICE"; then
      SERVICE_RUNNING=true
      ok "$AGENT_SERVICE is already running the current version."
    else
      warn "$AGENT_SERVICE is installed but not running. Start it with: sudo systemctl start $AGENT_SERVICE"
    fi
  fi
elif [ "$RESTART_NEEDED" = true ]; then
  warn "systemd is not available on this host: restart the agent yourself so the new binary takes effect."
fi

# ── Result ────────────────────────────────────────────────────────────────────
echo ""
printf '%snat-backup %s installed successfully.%s\n' "$GREEN" "$LATEST" "$NC"

if [ "$AGENT_ONLY" = false ]; then
  echo ""
  echo "Next steps (server):"
  echo "  1. Start Postgres:  docker compose up -d postgres"
  echo "  2. Configure:       cp .env.example .env  &&  edit .env"
  echo "  3. Run:             nat-backup-server"
  echo "     (run it under a supervisor, e.g. a systemd unit with Restart=always,"
  echo "      so a crash does not turn into a permanent outage)"
fi

echo ""
if [ "$CONFIGURE_AGENT" = true ]; then
  if [ "$SERVICE_RUNNING" = true ]; then
    echo "Agent configured and running as the systemd service '$AGENT_SERVICE'."
    echo "  Status:  systemctl status $AGENT_SERVICE"
    echo "  Logs:    journalctl -u $AGENT_SERVICE -f"
  else
    echo "Agent configured. Start it with:"
    echo "  nat-backup-agent $CONFIG_FILE"
    echo ""
    echo "Or as a systemd service:"
    echo "  nat-backup-agent install && systemctl enable --now $AGENT_SERVICE"
  fi
else
  echo "Next steps (agent):"
  echo "  1. Create $CONFIG_FILE (mode 0600, root-owned):"
  echo "       server_url: http://YOUR_SERVER:8080"
  echo "       api_key:    YOUR_API_KEY"
  echo "  2. Run:      nat-backup-agent $CONFIG_FILE"
  echo "  3. Service:  nat-backup-agent install && systemctl enable --now $AGENT_SERVICE"
fi
