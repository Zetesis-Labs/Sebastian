# Telemetría del dispositivo — serial → OpenTelemetry → Grafana

Observabilidad completa del Sebastián en el Mac de desarrollo. El transporte es
el **puerto serie**: sigue funcionando exactamente cuando WiFi/LiveKit fallan,
que es cuando más lo necesitas. Nada de esto toca el firmware en runtime ni
resetea el dispositivo al engancharse.

```
device ──serial──▶ bridge.py ──OTLP/HTTP──▶ grafana/otel-lgtm ──▶ Grafana :3000
                   (parseo → métricas          (Collector + Prometheus
                    + logs OTel)                + Loki + Grafana)
agent.py (Python) ──OTLP/HTTP─────────────────────▲
                   (agent/telemetry.py: logs + métricas de sesión)
```

Las dos mitades de cada conversación en el mismo Grafana:
`service_name="sebastian-device"` (firmware, vía serial) y
`service_name="sebastian-agent"` (LiveKit/OpenAI/Home Assistant, vía OTel SDK).

## Arrancar

```bash
# 1. Stack LGTM (una sola imagen con todo)
docker run -d --name sebastian-lgtm -p 3000:3000 -p 4317:4317 -p 4318:4318 \
    grafana/otel-lgtm:latest

# 2. Bridge (deps inline via uv; NO puede haber otro proceso leyendo el puerto)
uv run tools/telemetry/bridge.py

# 3. Dashboard (una vez, o tras recrear el contenedor)
curl -s -X POST http://localhost:3000/api/dashboards/db -u admin:admin \
    -H "Content-Type: application/json" -d @tools/telemetry/dashboard.json
```

Grafana: **http://localhost:3000** (admin/admin) → dashboard
"Sebastián — Telemetría del dispositivo" (`/d/sebastian-device`).

## Qué mide

| Métrica | Significado |
|---|---|
| `sebastian_pcm_peak` / `sebastian_pcm_dc` | Salud del canal I2S. `peak=0` = sordo; `dc` alto = canal clavado (audio machacado). Los dos estados degenerados que nos volvieron locos. |
| `sebastian_wake_prob_max` | Probabilidad máx del modelo por ventana de 5s. |
| `sebastian_wake_detections_total` / `sebastian_prob_spikes_total` | Detecciones y casi-detecciones (>30%, etiquetadas por decena). |
| `sebastian_sessions_opened_total` / `closed_total` | Ciclo de sesiones LiveKit. |
| `sebastian_session_active` / `sebastian_agent_state` | Estado en vivo (el agente publica speaking/listening por data channel). |
| `sebastian_echo_gated_peak` / `sebastian_echo_live` | Eco residual mientras habla el agente. `gated_peak` = half-duplex (0 en full-duplex, no hay gate); `live_echo` = equivalente full-duplex (nivel de micro con el agente hablando — **alto = el AEC está fugando**). |
| `sebastian_render_peak` / `sebastian_keepalive` | Salida del altavoz mientras habla el agente, y el keepalive que mantiene viva la sesión con esa señal — **independiente del data channel SCTP** (que se cae). Ver [esp-webrtc-solution#186](https://github.com/espressif/esp-webrtc-solution/issues/186). |
| `sebastian_reboots_total{reason=…}` / `sebastian_panics_total` | Reinicios con su causa (`0xc`=crash SW, `0x15`=reset USB…). |
| `sebastian_channel_heals_total` | Auto-curaciones del I2S (resyncs forzados). |
| `sebastian_sctp_init_total` | Retries SCTP — tormenta = sesión huérfana del SDK. |
| `sebastian_feed_max_us` / `gap_max_us` | Presupuesto real-time del wake task (feed <10ms = sano). |
| `sebastian_preroll_sent_total` | Pre-rolls entregados al agente. |
| `sebastian_muted` | 1 mientras el botón de mute está pulsado (el XVF streamea ceros — no confundir con canal muerto). |
| `sebastian_serial_age_seconds` / `serial_attached` | **Heartbeat**: un gauge congelado parece sano; esto distingue "vivo y callado" de "muerto/desenchufado". Edad >15s = algo pasa. |
| `sebastian_device_uptime_seconds` | Uptime desde los timestamps `I (ms)` de IDF — un salto hacia atrás = reinicio. |
| `sebastian_sessions_closed_total{reason=…}` | Cierres etiquetados: `max_duration` vs `silence_timeout` vs `disconnected_early`. |

**Lado agente** (`sebastian_agent_*`): `jobs_total` (sesiones aceptadas),
`turns_total{role}` (turnos de conversación), `tool_calls_total{tool}` (Home
Assistant), `state_changes_total{state}`, `errors_total`.

**Logs completos** en Loki: `{service_name="sebastian-device"}` — cada línea del
serial con severidad (los `E (…)`/PANIC como error, `W (…)` como warning) — y
`{service_name="sebastian-agent"}` con **la transcripción de cada turno**
(`turn [user]: …` / `turn [assistant]: …`), las herramientas ejecutadas y los
errores de sesión del lado Python.

## Notas operativas

- **Un solo lector del puerto**: para flashear, para el bridge (`pkill -f bridge.py`),
  flashea, y relánzalo. Se auto-reconecta si el puerto re-enumera.
- El bridge nunca toca DTR/RTS — engancharse no resetea el device.
- Si cambias el formato de una línea de log del firmware, actualiza el regex
  correspondiente en `bridge.py` (y viceversa: las líneas parseadas están
  documentadas allí).
