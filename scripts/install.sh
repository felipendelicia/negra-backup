#!/usr/bin/env bash
# install.sh — instala o actualiza nat-backup-server y nat-backup-agent en Linux
#
# Uso básico:
#   curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | bash
#
# Solo agente, con configuración automática:
#   curl -fsSL .../install.sh | bash -s -- --agent-only --server-url http://HOST:8080 --api-key TU_KEY

set -euo pipefail

REPO="felipendelicia/negra-backup"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/nat-backup"
AGENT_ONLY=false
SERVER_URL=""
API_KEY=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --agent-only)         AGENT_ONLY=true ;;
    --server-url)         SERVER_URL="$2"; shift ;;
    --server-url=*)       SERVER_URL="${1#*=}" ;;
    --api-key)            API_KEY="$2"; shift ;;
    --api-key=*)          API_KEY="${1#*=}" ;;
  esac
  shift
done

# ── Colores ───────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
info() { echo -e "${YELLOW}→${NC} $*"; }
err()  { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# ── Dependencias ──────────────────────────────────────────────────────────────
for cmd in curl grep sed; do
  command -v "$cmd" &>/dev/null || err "Se requiere '$cmd' pero no está instalado."
done

# ── Arquitectura ──────────────────────────────────────────────────────────────
case "$(uname -m)" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Arquitectura no soportada: $(uname -m)" ;;
esac

# ── Última versión ────────────────────────────────────────────────────────────
info "Obteniendo última versión..."
LATEST=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 \
  | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
[ -n "$LATEST" ] || err "No se pudo obtener la última versión. ¿El repositorio tiene releases publicados?"
info "Última versión: $LATEST"

# ── Función de instalación ────────────────────────────────────────────────────
install_binary() {
  local name="$1" asset="$2" dest="$INSTALL_DIR/$name"
  if [ -x "$dest" ]; then
    CURRENT=$("$dest" version 2>/dev/null || echo "desconocida")
    if [ "$CURRENT" = "$LATEST" ]; then
      ok "$name ya está en $LATEST, sin cambios."; return
    fi
    info "Actualizando $name  $CURRENT → $LATEST..."
  else
    info "Instalando $name $LATEST..."
  fi
  TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT
  curl -fsSL "https://github.com/$REPO/releases/download/$LATEST/$asset" -o "$TMP" \
    || err "Error al descargar $asset"
  chmod +x "$TMP"
  if [ -w "$INSTALL_DIR" ]; then mv "$TMP" "$dest"
  else sudo mv "$TMP" "$dest"; fi
  ok "$name instalado en $dest"
}

# ── Instalación ───────────────────────────────────────────────────────────────
if [ "$AGENT_ONLY" = false ]; then
  install_binary "nat-backup-server" "nat-backup-server-linux-$ARCH"
fi
install_binary "nat-backup-agent" "nat-backup-agent-linux-$ARCH"

# ── Configuración del agente (si se proporcionaron credenciales) ──────────────
if [ -n "$SERVER_URL" ] && [ -n "$API_KEY" ]; then
  if [ -w "$CONFIG_DIR" ] || sudo mkdir -p "$CONFIG_DIR" 2>/dev/null; then
    sudo mkdir -p "$CONFIG_DIR"
  else
    mkdir -p "$CONFIG_DIR"
  fi
  CONFIG_FILE="$CONFIG_DIR/agent.yaml"
  sudo tee "$CONFIG_FILE" > /dev/null <<EOF
server_url: $SERVER_URL
api_key: $API_KEY
EOF
  sudo chmod 600 "$CONFIG_FILE"
  ok "Configuración guardada en $CONFIG_FILE"
fi

# ── Resultado ─────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}nat-backup $LATEST instalado correctamente.${NC}"

if [ "$AGENT_ONLY" = false ]; then
  echo ""
  echo "Próximos pasos (servidor):"
  echo "  1. Iniciar Postgres:  docker compose up -d postgres"
  echo "  2. Configurar:        cp .env.example .env  &&  editar .env"
  echo "  3. Ejecutar:          nat-backup-server"
else
  echo ""
  if [ -n "$SERVER_URL" ] && [ -n "$API_KEY" ]; then
    echo "Agente configurado. Para iniciar:"
    echo "  nat-backup-agent /etc/nat-backup/agent.yaml"
    echo ""
    echo "O como servicio systemd:"
    echo "  nat-backup-agent install && systemctl enable --now nat-backup-agent"
  else
    echo "Próximos pasos (agente):"
    echo "  1. Crear $CONFIG_DIR/agent.yaml:"
    echo "       server_url: http://TU_SERVIDOR:8080"
    echo "       api_key:    TU_API_KEY"
    echo "  2. Ejecutar:  nat-backup-agent /etc/nat-backup/agent.yaml"
    echo "  3. Servicio:  nat-backup-agent install"
  fi
fi
