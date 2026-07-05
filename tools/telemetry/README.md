# Device Telemetry — serial → OpenTelemetry → Grafana

Complete observability of Sebastian on the development Mac. The transport is
the **serial port**: it keeps working exactly when WiFi/LiveKit fail,
which is when you need it most. None of this touches the firmware at runtime nor
resets the device upon attachment.

```
device ──serial──▶ bridge.py ──OTLP/HTTP──▶ grafana/otel-lgtm ──▶ Grafana :3000
                   (parsing → metrics          (Collector + Prometheus
                    + OTel logs)                + Loki + Grafana)
agent.py (Python) ──OTLP/HTTP─────────────────────▲
                   (agent/telemetry.py: logs + session metrics)
```

The two halves of each conversation in the same Grafana:
`service_name="sebastian-device"` (firmware, via serial) and
`service_name="sebastian-agent"` (LiveKit/OpenAI/Home Assistant, via OTel SDK).

## Startup

```bash
# 1. LGTM Stack (a single image with everything)
docker run -d --name sebastian-lgtm -p 3000:3000 -p 4317:4317 -p 4318:4318 \
    grafana/otel-lgtm:latest

# 2. Bridge (inline deps via uv; there CANNOT be another process reading the port)
uv run tools/telemetry/bridge.py

# 3. Dashboard (once, or after recreating the container)
curl -s -X POST http://localhost:3000/api/dashboards/db -u admin:admin \
    -H "Content-Type: application/json" -d @tools/telemetry/dashboard.json
```

Grafana: **http://localhost:3000** (admin/admin) → dashboard
"Sebastian — Device Telemetry" (`/d/sebastian-device`).

## What it measures

| Metric | Meaning |
|---|---|
| `sebastian_pcm_peak` / `sebastian_pcm_dc` | I2S channel health. `peak=0` = deaf; high `dc` = pegged channel (crushed audio). The two degenerate states that drove us crazy. |
| `sebastian_wake_prob_max` | Max model probability per 5s window. |
| `sebastian_wake_detections_total` / `sebastian_prob_spikes_total` | Detections and near-detections (>30%, labeled by decade). |
| `sebastian_sessions_opened_total` / `closed_total` | LiveKit session cycle. |
| `sebastian_session_active` / `sebastian_agent_state` | Live state (the agent publishes speaking/listening via data channel). |
| `sebastian_echo_gated_peak` / `sebastian_echo_live` | Residual echo while the agent is speaking. `gated_peak` = half-duplex (0 in full-duplex, no gate); `live_echo` = full-duplex equivalent (mic level with the agent speaking — **high = the AEC is leaking**). |
| `sebastian_render_peak` / `sebastian_keepalive` | Speaker output while the agent is speaking, and the keepalive that keeps the session alive with that signal — **independent of the SCTP data channel** (which drops). See [esp-webrtc-solution#186](https://github.com/espressif/esp-webrtc-solution/issues/186). |
| `sebastian_reboots_total{reason=…}` / `sebastian_panics_total` | Reboots with their cause (`0xc`=SW crash, `0x15`=USB reset…). |
| `sebastian_channel_heals_total` | I2S self-healing (forced resyncs). |
| `sebastian_sctp_init_total` | SCTP retries — storm = orphaned SDK session. |
| `sebastian_feed_max_us` / `gap_max_us` | Real-time budget of the wake task (feed <10ms = healthy). |
| `sebastian_preroll_sent_total` | Pre-rolls delivered to the agent. |
| `sebastian_muted` | 1 while the mute button is pressed (the XVF streams zeros — not to be confused with a dead channel). |
| `sebastian_serial_age_seconds` / `serial_attached` | **Heartbeat**: a frozen gauge seems healthy; this distinguishes "alive and quiet" from "dead/unplugged". Age >15s = something is wrong. |
| `sebastian_device_uptime_seconds` | Uptime from IDF `I (ms)` timestamps — a backward jump = reboot. |
| `sebastian_sessions_closed_total{reason=…}` | Labeled closures: `max_duration` vs `silence_timeout` vs `disconnected_early`. |

**Agent side** (`sebastian_agent_*`): `jobs_total` (accepted sessions),
`turns_total{role}` (conversation turns), `tool_calls_total{tool}` (Home
Assistant), `state_changes_total{state}`, `errors_total`.

**Complete logs** in Loki: `{service_name="sebastian-device"}` — each serial
line with severity (`E (…)`/PANIC as error, `W (…)` as warning) — and
`{service_name="sebastian-agent"}` with **the transcription of each turn**
(`turn [user]: …` / `turn [assistant]: …`), the executed tools and the
session errors on the Python side.

## Operational notes

- **A single port reader**: to flash, stop the bridge (`pkill -f bridge.py`),
  flash, and relaunch it. It auto-reconnects if the port re-enumerates.
- The bridge never touches DTR/RTS — attaching does not reset the device.
- If you change the format of a firmware log line, update the corresponding regex
  in `bridge.py` (and vice versa: the parsed lines are
  documented there).
