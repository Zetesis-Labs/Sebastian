#!/usr/bin/env bash
# Prepare the static GitHub Pages web installer from an existing firmware build.
#
# This does not build firmware. Run `make fw-build` first in the devcontainer.
# The generated binary can embed local WiFi/token-server config; publish only
# factory images that are intentionally safe to distribute.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="$ROOT/firmware/build"
OUT_DIR="$ROOT/docs/installer/firmware"
MANIFEST="$ROOT/docs/installer/manifest.json"
VERSION="${1:-$(git -C "$ROOT" describe --tags --always --dirty)}"
MERGED_BIN="$OUT_DIR/sebastian-esp32s3-merged.bin"

require_file() {
    if [[ -f "$1" ]]; then
        return
    fi
    echo "Missing $1. Run: make fw-build" >&2
    exit 1
}

require_file "$BUILD_DIR/bootloader/bootloader.bin"
require_file "$BUILD_DIR/partition_table/partition-table.bin"
require_file "$BUILD_DIR/sebastian.bin"

mkdir -p "$OUT_DIR"

uvx --from "esptool==4.11.0" esptool.py --chip esp32s3 merge_bin \
    -o "$MERGED_BIN" \
    --flash_mode dio \
    --flash_freq 80m \
    --flash_size 8MB \
    0x0 "$BUILD_DIR/bootloader/bootloader.bin" \
    0x8000 "$BUILD_DIR/partition_table/partition-table.bin" \
    0x10000 "$BUILD_DIR/sebastian.bin"

cat >"$MANIFEST" <<JSON
{
  "name": "Sebastian",
  "version": "$VERSION",
  "new_install_prompt_erase": true,
  "new_install_improv_wait_time": 0,
  "builds": [
    {
      "chipFamily": "ESP32-S3",
      "improv": false,
      "parts": [
        {
          "path": "firmware/sebastian-esp32s3-merged.bin",
          "offset": 0
        }
      ]
    }
  ]
}
JSON

echo "[installer] wrote $MERGED_BIN"
echo "[installer] wrote $MANIFEST"
echo "[installer] WARNING: generated firmware may embed local secrets; review before publishing."
