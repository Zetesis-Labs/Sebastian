#!/usr/bin/env bash
# Provisiona el dashboard de Grafana. Idempotente: se puede re-ejecutar tras
# recrear el contenedor LGTM (el dashboard vive en el FS del contenedor).
# GRAFANA_URL: http://localhost:3000 (host) | http://lgtm:3000 (devcontainer).
set -euo pipefail
cd "$(dirname "$0")"
GRAFANA_URL="${GRAFANA_URL:-http://localhost:3000}"

for _ in $(seq 1 30); do
    curl -sf "$GRAFANA_URL/api/health" > /dev/null && break
    sleep 2
done

curl -sf -X POST "$GRAFANA_URL/api/dashboards/db" -u admin:admin \
    -H "Content-Type: application/json" -d @dashboard.json > /dev/null
echo "[provision] dashboard OK en $GRAFANA_URL/d/sebastian-device"
