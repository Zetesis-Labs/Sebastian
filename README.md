# Sebastian

**Bi-directional conversational** voice speaker over **Seeed ReSpeaker XVF3800
+ XIAO ESP32-S3**, connected to **LiveKit Cloud**. The user speaks, the
device captures and cleans the voice via hardware (4-microphone array with
beamforming + AEC + noise suppression in the XMOS XVF3800), publishes it in a
LiveKit room, and a **Python agent (OpenAI Realtime)** responds through the
speaker. The entire loop is voice-to-voice.

- **Repository:** `github.com/Zetesis-Labs/Sebastian`

## Status: IT WORKS

The **bi-directional voice conversation works and is validated in hardware**: the
agent speaks through the speaker and hears the user intelligibly. The **LED ring
points to who is speaking** (direction of arrival / DoA) and the **mute button**
works (the ring turns off when muted). Details in
[docs/STATUS.md](docs/STATUS.md).

```
┌─────────────────────────┐         WebRTC          ┌──────────────────┐
│ ESP32-S3 (Zig firmware)  │  ◄───────────────────►  │  LiveKit Cloud   │
│ ReSpeaker XVF3800        │      room / Opus        │      (room)      │
│  · 4 mics → XVF3800      │                         └────────┬─────────┘
│  · speaker ← AIC3104     │                                  │ dispatch
└─────────────────────────┘                         ┌────────▼─────────┐
                                                     │ Agent (Python)   │
                                                     │ OpenAI Realtime  │
                                                     └──────────────────┘
```

## How it fits together (summary)

- The **XVF3800 is the I2S master** (generates the clock at **48 kHz**, 32-bit,
  stereo) and the **ESP32-S3 is the slave**, over **two separate I2S ports** (RX
  mic / TX speaker) to avoid corrupting the DMA.
- The firmware **DFU-flashes the XVF** to its **"inthost" (I2S-master)** firmware via
  I2C **from our own Zig code** (without ESPHome or external tools)
  and un-mutes it on boot.
- The microphone uses the **raw RIGHT/ASR beam of the XVF** (without on-chip NS) and the
  **agent's BVC noise cancellation** does the only noise suppression pass
  — this avoids the "tinny" artifact of double NS.
- The mic audio is published as **Opus at 48 kHz**; LiveKit resamples downstream.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for full details.

## Key decisions

- **Firmware in Zig**: memory safety, `comptime`, native C-interop. The
  core WebRTC/network remains in C (the LiveKit SDK, not rewritable); Zig covers the
  application layer (board bring-up, mic source, speaker path, XVF DFU,
  room logic).
- **Agent in Python**: this is how LiveKit agents are built (`agents` framework
  with batteries included: VAD, turn-detection, BVC noise cancellation).
- **LiveKit Cloud + sandbox token**: no custom token backend.

> ⚠️ This is a **bleeding edge** project: first LiveKit-on-ESP32 in Zig (Espressif
> `0.16-xtensa` fork, LLVM Xtensa backend).

## Documentation

| Document | What it covers |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Full system architecture: voice-to-voice flow, I2C/I2S buses, mic and speaker pipelines, channel decision, agent. |
| [docs/HARDWARE.md](docs/HARDWARE.md) | ReSpeaker XVF3800 + XIAO ESP32-S3 board: components, pinout, I2C addresses, audio topology. |
| [docs/FIRMWARE.md](docs/FIRMWARE.md) | Zig firmware on ESP-IDF v5.4: modules, manual `extern` bindings, build system. |
| [docs/XVF3800.md](docs/XVF3800.md) | The XVF3800 chip: firmware families, DFU via I2C, protocol, mute, the two output channels. |
| [docs/BUILD_AND_RUN.md](docs/BUILD_AND_RUN.md) | Operator guide: build, flash, run the agent, dispatch and audio verification. |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Playbook symptom → root cause → solution from the bring-up session. |
| [docs/STATUS.md](docs/STATUS.md) | Current project status and open quality items. |

## Structure

| Folder | What |
|---|---|
| [`firmware/`](firmware/) | ESP32-S3 firmware. App in **Zig** on ESP-IDF + LiveKit C SDK. |
| [`agent/`](agent/) | Voice agent in **Python** (`livekit-agents` + OpenAI Realtime). |
| [`docs/`](docs/) | Documentation (see table above). |

## Quick start

See [docs/BUILD_AND_RUN.md](docs/BUILD_AND_RUN.md) for the full guide. In short:

1. **Firmware**: `source ~/esp/esp-idf/export.sh && cd firmware && idf.py build && idf.py -p /dev/cu.usbmodem101 flash monitor`
2. **Agent** (exactly one): `cd agent && uv sync && uv run agent.py dev`
3. **Dispatch** to a fresh room: `lk room delete sebastian` and reset the board.

Secrets (WiFi, LiveKit token, OpenAI key) go in gitignored files;
they are never committed.
