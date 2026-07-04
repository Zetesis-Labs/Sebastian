#!/usr/bin/env bash
# Despachador de acciones para el launch.json (estilo Nixon devtools.sh).
# Cada modo es una entrada del pickString en .vscode/launch.json.
set -euo pipefail
cd "$(dirname "$0")/.."

# El bridge es el único lector del serie; suéltalo antes de tocar el USB.
release_serial() { pkill -f tools/telemetry/bridge.py 2>/dev/null || true; sleep 1; }

# Resuelve el puerto serie en orden de preferencia:
#   1. SEBASTIAN_SERIAL_URL explícito (override manual)
#   2. device local (host, o contenedor Linux con 'devices:')
#   3. host compartiendo el serie por TCP (make serial-share) — se autodetecta
#      probando host.docker.internal:4000, así el flash desde el contenedor en
#      macOS/Windows "just works" sin configurar nada.
serial_port() {
  if [[ -n "${SEBASTIAN_SERIAL_URL:-}" ]]; then echo "$SEBASTIAN_SERIAL_URL"; return; fi

  local dev; dev="$(ls /dev/cu.usbmodem* /dev/ttyACM* /dev/ttyUSB* 2>/dev/null | head -1)"
  if [[ -n "$dev" ]]; then echo "$dev"; return; fi

  # ?ign_set_control es OBLIGATORIO para conectar (sin él, esptool se cuelga
  # negociando 'control'). Pero con él NO hay auto-reset por la red: el
  # USB-Serial-JTAG del XIAO no reenvía su secuencia de reset por RFC2217.
  # ⇒ hay que poner el chip en download mode A MANO (BOOT+RESET) antes de flashear.
  local host="${SEBASTIAN_SERIAL_HOST:-host.docker.internal}"
  if (exec 3<>"/dev/tcp/$host/4000") 2>/dev/null; then
    exec 3>&- 3<&-
    echo "rfc2217://$host:4000?ign_set_control"
  fi
}

# El zig del fork Xtensa se descarga al compilar (cmake/zig.cmake).
require_zig() {
  local z; z="$(ls firmware/build/zig-relsafe-*/zig 2>/dev/null | head -1)"
  if [[ -z "$z" ]]; then
    echo "Zig aún no está: compila una vez (Firmware → Build) y se descarga." >&2
    exit 1
  fi
  echo "$z"
}

require_port() {
  local port; port="$(serial_port)"
  if [[ -z "$port" ]]; then
    {
      echo "── No hay puerto serie visible aquí ──"
      echo "En el devcontainer (macOS/Windows) el USB NO cruza al contenedor."
      echo "Compártelo por TCP desde el HOST (una vez):   make serial-share"
      echo "…y este Flash lo detecta solo (host.docker.internal:4000)."
      echo "Alternativas: flashear desde un terminal del HOST, o en Linux"
      echo "descomentar 'devices:' en .devcontainer/docker-compose.yml."
    } >&2
    exit 1
  fi
  echo "$port"
}

case "${1:-}" in
  "Build")            exec tools/idf.sh build ;;
  "Flash")            release_serial; exec tools/idf.sh -p "$(require_port)" flash ;;
  "Monitor")          exec tools/idf.sh -p "$(require_port)" monitor ;;
  "Flash + Monitor")  release_serial; exec tools/idf.sh -p "$(require_port)" flash monitor ;;

  "Bridge (telemetría)") exec uv run tools/telemetry/bridge.py ;;
  "LGTM up")             exec docker compose -f .devcontainer/docker-compose.yml up -d lgtm ;;
  "Provision dashboard") exec tools/telemetry/provision.sh ;;

  "Zig fmt")       exec "$(require_zig)" fmt firmware/main/ ;;
  "Zig fmt check") exec "$(require_zig)" fmt --check firmware/main/ ;;

  *) echo "devtools.sh: modo desconocido '${1:-}'" >&2; exit 1 ;;
esac
