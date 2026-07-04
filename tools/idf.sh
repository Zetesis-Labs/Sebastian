#!/usr/bin/env bash
# Envoltorio de idf.py que funciona en host y en devcontainer.
# - Contenedor: idf.py ya está en PATH (o IDF_PATH exportado) → se usa directo.
# - Host macOS: sourcea ~/esp/esp-idf/export.sh (o $IDF_PATH) una vez.
# Las tareas de VS Code llaman a este script en vez de a idf.py a pelo.
set -euo pipefail
if ! command -v idf.py >/dev/null 2>&1; then
    # shellcheck disable=SC1091
    source "${IDF_PATH:-$HOME/esp/esp-idf}/export.sh" >/dev/null 2>&1
fi
cd "$(dirname "$0")/../firmware"
exec idf.py "$@"
