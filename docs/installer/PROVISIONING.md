# Provisioning protocol

The installer page uses Web Serial to send one newline-terminated command after the
factory firmware has been flashed:

```text
sebastian.config.v1 {"schema":"sebastian.config.v1",...}
```

The firmware-side receiver is implemented in `firmware/main/provisioning.c`. It:

1. Listens on the USB-Serial-JTAG console (a task started at boot).
2. Accepts only lines prefixed with `sebastian.config.v1 `.
3. Parses the JSON payload (cJSON).
4. Checks `schema == "sebastian.config.v1"`.
5. Stores WiFi (`ssid`/`password`) and, when present, `livekit.tokenServerUrl`
   in NVS (namespace `sebastian`).
6. Replies `sebastian.config.ok` and restarts. On next boot
   `sebastian_net_connect()` uses the NVS creds, falling back to the compiled
   `CONFIG_LK_EXAMPLE_WIFI_*` when NVS is empty.

Still pending before this is safe to distribute publicly: blank the factory
`CONFIG_LK_EXAMPLE_WIFI_*` (so no secrets are baked in) and teach the CI to
build + publish that factory image. The token-server URL is stored but not yet
read back by `token.zig`.

Suggested acknowledgement:

```text
sebastian.config.ok
```

Suggested validation failure:

```text
sebastian.config.err <reason>
```

## Payload

The canonical contract is
[`sebastian-config.schema.json`](sebastian-config.schema.json). The example below is
valid for `schema = "sebastian.config.v1"`.

```json
{
  "schema": "sebastian.config.v1",
  "wifi": {
    "ssid": "Home",
    "password": "secret",
    "hidden": false
  },
  "livekit": {
    "tokenServerUrl": "http://192.168.1.10:8787/token",
    "deviceIdentity": "esp32-respeaker",
    "room": "sebastian",
    "agentName": "sebastian"
  },
  "telemetry": {
    "otlpEndpoint": "https://otel.example.com",
    "grafanaUrl": "https://grafana.example.com/d/sebastian-device"
  },
  "audio": {
    "micChannel": "right",
    "fixedBeam": true,
    "fixedBeamAzimuthDeg": 0,
    "fullDuplex": true,
    "probeAecOnBoot": false
  },
  "session": {
    "silenceTimeoutMs": 12000,
    "voiceLevel": 3000
  }
}
```

## Firmware mapping

| Field | Current source | Target storage |
|---|---|---|
| `wifi.ssid` / `wifi.password` / `wifi.hidden` | `firmware/sdkconfig` | NVS, read before WiFi connect |
| `livekit.tokenServerUrl` | `firmware/main/secrets.zig` | NVS, read by `token.zig` |
| `livekit.deviceIdentity` / `room` / `agentName` | `agent/token_server.py` constants | Token server query params or per-device backend state |
| `telemetry.otlpEndpoint` | `tools/telemetry/bridge.py` / env | Bridge/collector config; device-side WiFi OTLP is future work |
| `telemetry.grafanaUrl` | operator docs | Installer/app link only |
| `audio.*` | `firmware/main/config.zig` | NVS where safe; some values still need compile-time handling today |
| `session.*` | `firmware/main/app.zig` constants | NVS/runtime config |

## Security

Do not send LiveKit API secrets, OpenAI keys, or Grafana admin credentials to the
device. The device should only know how to reach a token server that mints scoped
short-lived LiveKit tokens.
