!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="${REPO_ROOT}/.devcontainer/pi-config"
AGENT_DIR="${PI_CODING_AGENT_DIR:-/var/lib/pi/agent}"
MARKER="${AGENT_DIR}/.seeded"
FORCE="${PI_CONFIG_FORCE:-0}"

echo "=========================================="
echo "🚀 Inicializando entorno de desarrollo"
echo "=========================================="

# ---------------------------------------------------------------
# 1. Cliente psql (antes venía del Dockerfile)
# ---------------------------------------------------------------
if ! command -v psql >/dev/null 2>&1; then
  echo "==> Instalando cliente PostgreSQL..."
  sudo apt-get update -qq
  sudo apt-get install -y -qq --no-install-recommends postgresql-client
  sudo rm -rf /var/lib/apt/lists/*
fi

# ---------------------------------------------------------------
# 2. RTK (Rust Token Killer) — binario único, sin runtime
# ---------------------------------------------------------------
if ! command -v rtk >/dev/null 2>&1; then
  echo "==> Instalando RTK..."
  case "$(uname -m)" in
    x86_64)  RTK_ASSET=rtk-x86_64-unknown-linux-musl.tar.gz ;;
    aarch64) RTK_ASSET=rtk-aarch64-unknown-linux-gnu.tar.gz ;;
    *) echo "⛔ Arquitectura no soportada: $(uname -m)"; exit 1 ;;
  esac
  curl -fsSL -o /tmp/rtk.tar.gz \
    "https://github.com/rtk-ai/rtk/releases/latest/download/${RTK_ASSET}"
  mkdir -p /tmp/rtk && tar -xzf /tmp/rtk.tar.gz -C /tmp/rtk
  sudo install -m 0755 "$(find /tmp/rtk -type f -name rtk -print -quit)" /usr/local/bin/rtk
  rm -rf /tmp/rtk /tmp/rtk.tar.gz
fi
rtk --version

# ---------------------------------------------------------------
# 3. Angular CLI
# ---------------------------------------------------------------
if ! command -v ng >/dev/null 2>&1; then
  echo "==> Instalando Angular CLI..."
  npm install -g @angular/cli
fi

# ---------------------------------------------------------------
# 4. Sembrar plantilla de configuración de Pi (solo primer arranque)
# ---------------------------------------------------------------
if [ -d "$TEMPLATE" ]; then
  echo "==> Configuración de Pi..."
  mkdir -p "$AGENT_DIR"

  if [ -f "$MARKER" ] && [ "$FORCE" != "1" ]; then
    echo "ℹ️  Plantilla ya sembrada; no se toca la config del agente"
  else
    for src in "$TEMPLATE"/*.json; do
      [ -e "$src" ] || continue
      name="$(basename "$src")"

      # Lista negra explícita: jamás sembrar material sensible
      case "$name" in
        auth.json|trust.json|mcp.json|models.json)
          echo "⛔ Omitido por política de seguridad: $name"
          continue
          ;;
      esac

      if [ -f "${AGENT_DIR}/${name}" ] && [ "$FORCE" != "1" ]; then
        echo "↷ Conservado (ya existe): $name"
        continue
      fi

      cp "$src" "${AGENT_DIR}/${name}"
      echo "✅ Sembrado: $name"
    done
    touch "$MARKER"
  fi
fi

# ---------------------------------------------------------------
# Install rigrep
# ---------------------------------------------------------------
if ! command -v ripgrep >/dev/null 2>&1; then
  echo "==> Instalando ripgrep..."

  sudo apt-get update -qq
  sudo apt-get install -y ripgrep
fi

# ---------------------------------------------------------------
# 6. Dependencias del proyecto
# ---------------------------------------------------------------
[ -f "${REPO_ROOT}/frontend/package.json" ] \
  && (cd "${REPO_ROOT}/frontend" && npm install) \
  || echo "ℹ️  Sin frontend/package.json"

[ -f "${REPO_ROOT}/backend/go.mod" ] \
  && (cd "${REPO_ROOT}/backend" && go mod download) \
  || echo "ℹ️  Sin backend/go.mod"

# ---------------------------------------------------------------
# 7. Verificación
# ---------------------------------------------------------------
echo ""
echo "=========================================="
echo "✨ Entorno listo"
echo "=========================================="
printf '  rtk      : %s\n' "$(rtk --version 2>/dev/null || echo 'NO')"
printf '  node     : %s\n' "$(node --version 2>/dev/null || echo 'NO')"
printf '  go       : %s\n' "$(go version 2>/dev/null || echo 'NO')"
printf '  pi       : %s\n' "$(command -v pi || echo 'NO')"
printf '  ng       : %s\n' "$(command -v ng || echo 'NO')"
