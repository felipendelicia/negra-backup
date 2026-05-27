#!/usr/bin/env bash
# install.sh — instala o actualiza nat-backup-server y nat-backup-agent en Linux
# Uso: curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | bash
# Flags: --agent-only  (solo instala el agente)

set -euo pipefail

REPO="felipendelicia/negra-backup"
INSTALL_DIR="/usr/local/bin"
AGENT_ONLY=false

for arg in "${@:-}"; do
  case "$arg" in
    --agent-only) AGENT_ONLY=true ;;
  esac
done

# ── Colores ──────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
info() { echo -e "${YELLOW}→${NC} $*"; }
err()  { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# ── Dependencias ─────────────────────────────────────────────────────────────
for cmd in curl grep sed; do
  command -v "$cmd" &>/dev/null || err "Se requiere '$cmd' pero no está instalado."
done

# ── Arquitectura ─────────────────────────────────────────────────────────────
case "$(uname -m)" in
  x86_64)          ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
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
  local name="$1"
  local asset="$2"
  local dest="$INSTALL_DIR/$name"

  if [ -x "$dest" ]; then
    CURRENT=$("$dest" version 2>/dev/null || echo "desconocida")
    if [ "$CURRENT" = "$LATEST" ]; then
      ok "$name ya está en $LATEST, sin cambios."
      return
    fi
    info "Actualizando $name  $CURRENT → $LATEST..."
  else
    info "Instalando $name $LATEST..."
  fi

  TMP=$(mktemp)
  trap 'rm -f "$TMP"' EXIT

  curl -fsSL \
    "https://github.com/$REPO/releases/download/$LATEST/$asset" \
    -o "$TMP" \
    || err "Error al descargar $asset"

  chmod +x "$TMP"

  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP" "$dest"
  else
    sudo mv "$TMP" "$dest"
  fi

  ok "$name instalado en $dest"
}

# ── Instalación ───────────────────────────────────────────────────────────────
if [ "$AGENT_ONLY" = false ]; then
  install_binary "nat-backup-server" "nat-backup-server-linux-$ARCH"
fi

install_binary "nat-backup-agent" "nat-backup-agent-linux-$ARCH"

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
  echo "Próximos pasos (agente):"
  echo "  1. Crear agent.yaml con server_url y api_key"
  echo "  2. Ejecutar:          nat-backup-agent agent.yaml"
  echo "  3. O como servicio:   nat-backup-agent install"
fi
