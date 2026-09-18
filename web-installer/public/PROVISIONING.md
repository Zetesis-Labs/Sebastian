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
   present `livekit.tokenServerUrl`, `telemetry.syslogIp`/`syslogPort`, the
   operating `mode`, and the audio behaviour it implies — `audio.fullDuplex`,
   `audio.fixedBeam`, `audio.fixedBeamAzimuthDeg`. A payload **without**
   `wifi.password` keeps the password already stored (an empty string means an
   open network).
6. Replies `sebastian.config.ok` and restarts. On next boot the device reads
   NVS: `sebastian_net_connect()` uses the WiFi creds, `token.zig` reads
   `tokenServerUrl`, and `config.zig::load()` overrides its compiled defaults
   with the stored audio/mode values **before** the XVF/AEC config is applied.

## The USB window and reading the config back

The board only has a serial port during the **first 5 s after power-up**: the
ESP32-S3 has one USB PHY and the firmware hands it to the USB microphone
interface (TinyUSB) once the boot is done. After a crash or a firmware-initiated
restart the port does not come back until the board is re-plugged.

An already provisioned unit can be edited instead of retyped. The installer's
"Load from device" sends

```text
sebastian.config.get
```

and the firmware answers with the stored config in the same shape (only the
keys present in NVS, so the installer merges them over its defaults):

```text
sebastian.config.dump {"schema":"sebastian.config.v1","provisioned":true,"wifi":{"ssid":"Home","passwordSet":true},"livekit":{"tokenServerUrl":"http://192.168.1.10:8787/token"},"telemetry":{"syslogIp":"192.168.1.10","syslogPort":514},"mode":"half_duplex","audio":{"fullDuplex":false,"fixedBeam":true,"fixedBeamAzimuthDeg":0},"profiles":[...],"activeProfile":"agent"}
```

Two extras: `provisioned` (there is a WiFi SSID in NVS) and `wifi.passwordSet`.
**The WiFi password never leaves the device**; the installer shows the field
empty and omits the key on send unless the operator types a new one.

A `config.get` also **holds the USB window open for 120 s** (`HOLD_AFTER_GET_US`
in `provisioning.c`; `app.zig` polls `sebastian_provisioning_hold()` before
handing the PHY to TinyUSB), so the edited config can come back on the same
port. The installer keeps the port open between load and send and shows the
countdown; past it, re-plug the board.

### Fleet fields (2026-09-18)

- `adoption.orgSecret` / `adoption.deviceSecret`: the secrets of
  docs/implementation/11-fleet-adoption-control-room.md §5. The device secret
  is born with the unit (generated at first boot, kept across adoptions,
  readable with `sebastian.config.get`); a document only carries it to rotate
  it. A unit that has `orgSecret` and whose control room (`tokenServerUrl`)
  does not hold its secret yet enrols on its own (`/v1/devices/{id}/enroll`),
  handing the secret over: that is how a unit provisioned from the embedded
  installer is born adopted. Stored as
  `org_secret` / `dev_secret`; an empty string erases them (that is what the
  control room's *forget* sends). With a device secret the firmware opens
  sessions through `POST /v1/sessions` instead of the legacy `/token`.
- `session.silenceTimeoutMs` / `session.voiceLevel`: now stored (`silence_ms`,
  `voice_lvl`) and read at boot — no longer compile-time.
- `configVersion`: the control room's desired-config version this document
  carries; echoed as `cfg=` in every reconciliation poll.
- `wifi` is optional in a document pushed over the network (adoption, desired
  config). A WiFi change pushed that way is a **trial**: no IP within 2 min and
  the device restores the previous network, restarts and announces
  `err=wifi-rollback`. Over USB the change is final (the unit is in hand).

Not stored on-device (still compile-time — a reflash, not a re-provision):
`audio.micChannel` (it feeds comptime slot/shift selection in `xvf_pcm.zig`)
and the boot self-tests
(`probeAecOnBoot` etc.). The self-tests are intentionally compile-time: as runtime
flags they defeat dead-code elimination and keep ~15 KB of static probe buffers in
internal RAM, which starves the TLS hardware-AES DMA and kills the LiveKit
connection. Enabling one is a reflash.

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
    "syslogIp": "192.168.1.10",
    "syslogPort": 514,
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
| `telemetry.syslogIp` / `syslogPort` | NVS, read by `syslog_sink.c` | ✅ UDP syslog sink from boot |
| `telemetry.otlpEndpoint` / `grafanaUrl` | `tools/telemetry/bridge.py` / env | ❌ device-side OTLP is future work |
| `mode`, `audio.fullDuplex`, `audio.fixedBeam`, `audio.fixedBeamAzimuthDeg` | NVS → `config.zig::load()` | ✅ applied at boot |
| `audio.micChannel` | `config.zig` comptime → `xvf_pcm.zig` slot/shift | ❌ reflash only |
| boot self-tests (`probeAecOnBoot` etc.) | `config.zig` comptime (elides ~15 KB probe buffers) | ❌ reflash only |
| `session.silenceTimeoutMs` / `voiceLevel` | NVS → `config.zig::load()` → `session_core` / `session_reducer` | ✅ applied at boot |
| `adoption.orgSecret` / `deviceSecret` | NVS, read by `adopt.c` / `token.zig` | ✅ adoption auth / `POST /v1/sessions` |
| `configVersion` | NVS, echoed by `control.zig` | ✅ reconciliation |

## Security

Do not send LiveKit API secrets, OpenAI keys, or Grafana admin credentials to the
device. The device should only know how to reach a token server that mints scoped
short-lived LiveKit tokens.
