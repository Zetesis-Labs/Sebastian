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
5. Stores into NVS (namespace `sebastian`): WiFi (`ssid`/`password`), and when
   present `livekit.tokenServerUrl`, the operating `mode`, and the audio
   behaviour it implies — `audio.fullDuplex`, `audio.fixedBeam`,
   `audio.fixedBeamAzimuthDeg`.
6. Replies `sebastian.config.ok` and restarts. On next boot the device reads
   NVS: `sebastian_net_connect()` uses the WiFi creds, `token.zig` reads
   `tokenServerUrl`, and `config.zig::load()` overrides its compiled defaults
   with the stored audio/mode values **before** the XVF/AEC config is applied.

Not stored on-device (still compile-time — a reflash, not a re-provision):
`audio.micChannel` (it feeds comptime slot/shift selection in `xvf_pcm.zig`),
`session.*` (the pure `session_core.zig` timing), and the boot self-tests
(`probeAecOnBoot` etc.). The self-tests are intentionally compile-time: as runtime
flags they defeat dead-code elimination and keep ~15 KB of static probe buffers in
internal RAM, which starves the TLS hardware-AES DMA and kills the LiveKit
connection. Enabling one is a reflash.

Still pending before public distribution: blank the factory
`CONFIG_LK_EXAMPLE_WIFI_*` so no secrets are baked in, and have CI build +
publish that factory image.

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
  "mode": "full_duplex",
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
    "fullDuplex": true
  },
  "session": {
    "silenceTimeoutMs": 12000,
    "voiceLevel": 3000
  }
}
```

## Firmware mapping

| Field | On-device source | Applied today? |
|---|---|---|
| `wifi.ssid` / `wifi.password` | NVS, read by `sebastian_net_connect()` | ✅ before WiFi connect |
| `wifi.hidden` | — | ❌ not consumed yet |
| `livekit.tokenServerUrl` | NVS, read by `token.zig` | ✅ per-session token fetch |
| `livekit.deviceIdentity` / `room` / `agentName` | `agent/token_server.py` constants | ❌ token server owns these |
| `telemetry.otlpEndpoint` / `grafanaUrl` | `tools/telemetry/bridge.py` / env | ❌ device-side OTLP is future work |
| `mode`, `audio.fullDuplex`, `audio.fixedBeam`, `audio.fixedBeamAzimuthDeg` | NVS → `config.zig::load()` | ✅ applied at boot |
| `audio.micChannel` | `config.zig` comptime → `xvf_pcm.zig` slot/shift | ❌ reflash only |
| boot self-tests (`probeAecOnBoot` etc.) | `config.zig` comptime (elides ~15 KB probe buffers) | ❌ reflash only |
| `session.silenceTimeoutMs` / `voiceLevel` | `config.zig` / `session_core.zig` | ❌ reflash only |

## Security

Do not send LiveKit API secrets, OpenAI keys, or Grafana admin credentials to the
device. The device should only know how to reach a token server that mints scoped
short-lived LiveKit tokens.
