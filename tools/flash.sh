#!/usr/bin/env bash
# Flashea desde el HOST (Docker en macOS no ve el USB) los artefactos
# compilados en el devcontainer. Solo necesita uv en el host (esptool va por
# uvx); no hace falta ESP-IDF instalado.
#
#   tools/flash.sh [puerto]      # puerto por defecto: primer /dev/cu.usbmodem*
set -euo pipefail
cd "$(dirname "$0")/../firmware/build"

PORT="${1:-$(ls /dev/cu.usbmodem* /dev/ttyACM* /dev/ttyUSB* 2>/dev/null | head -1)}"
if [[ -z "${PORT}" ]]; then
    echo "Sin puerto serie (cu.usbmodem*/ttyACM*/ttyUSB*) — ¿está enchufado el device?" >&2
    exit 1
fi
if [[ ! -f flash_args ]]; then
    echo "No hay firmware/build/flash_args — compila primero: make fw-build (en el devcontainer)" >&2
    exit 1
fi

# El bridge es el único lector permitido del puerto; suéltalo mientras flasheamos.
pkill -f tools/telemetry/bridge.py 2>/dev/null || true
sleep 1

uvx --from esptool esptool.py --chip esp32s3 -p "$PORT" -b 460800 \
    --before default_reset --after hard_reset write_flash @flash_args

echo "[flash] OK. Relanza el bridge: make bridge"
