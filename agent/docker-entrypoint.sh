#!/usr/bin/env bash
# Dispatch to one of the three Sebastian backend services by SEBASTIAN_SERVICE
# (baked per image via the SERVICE build-arg; overridable at run time). Keeping
# the three in one image means one Dockerfile + one dependency closure for code
# that is genuinely one uv package.
set -euo pipefail

svc="${SEBASTIAN_SERVICE:-agent}"

case "$svc" in
  token-server)
    # HTTP token minter + explicit agent dispatch. Bind all interfaces in-cluster.
    export SEBASTIAN_TOKEN_HOST="${SEBASTIAN_TOKEN_HOST:-0.0.0.0}"
    exec python token_server.py
    ;;
  control-plane)
    # HTTP control face (announce, …). Bind all interfaces in-cluster.
    export SEBASTIAN_CONTROL_HOST="${SEBASTIAN_CONTROL_HOST:-0.0.0.0}"
    exec python control_plane.py
    ;;
  agent)
    # LiveKit agent worker (outbound-only: registers with the SFU). No port.
    exec python agent.py start
    ;;
  *)
    echo "docker-entrypoint: unknown SEBASTIAN_SERVICE=$svc (want token-server|control-plane|agent)" >&2
    exit 64
    ;;
esac
