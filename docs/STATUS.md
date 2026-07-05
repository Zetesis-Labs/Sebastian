# Project Status (2026-07-03)

**Wake word "Sebastián" WORKING on-device and validated on
hardware.** The device listens locally with a 62 KB TFLite-Micro model
(zero cost at rest) and only opens the LiveKit session when it hears its name; it closes it
by local silence and returns to detection mode. Full cycle verified:
detection (both pronunciations) → session → close → re-detection.

## 2026-07-03 — natural conversation, observability and devcontainer

- **Event-driven pre-roll**: the ring (12 s PSRAM) records up to the instant of the
  handoff (room CONNECTED + agent inside), with a window anchored 2 s before the
  wake and send retries. "Sebastián, turn on the living room light" spoken continuously
  arrives entirely **without waiting for the green ring**. The agent discards the initial
  gate silence so as not to split the turn. (`pre_roll.zig`, `app.zig`, `agent.py`)
- **Device↔agent data protocol**: the agent publishes its state
  (`sebastian.agent_state`) and the device publishes the barge-in (`sebastian.barge_in`).
- **Half-duplex + LINGER**: the AEC does not converge in session, so the mic is
  muted while the agent speaks (+400 ms tail) — it kills the echo/self-talk
  loop; its voice counts as session activity, the closing changes to real silence and
  the 90 s limit → 10 min safety net. (`mic_src.zig`, `app.zig`)
- **Alexa-style barge-in**: "Sebastián" over the agent's voice interrupts it (the wake
  model monitors the gated audio → `interrupt()`); the agent is forbidden to say
  its own name. `end_session` by voice ("shut up"/"stop"/farewell) closes the room.
- **Home Assistant MCP** with anti-hallucination grounding (GetLiveContext mandatory
  for the home state).
- **Observability**: serial → `tools/telemetry/bridge.py` → OTLP → LGTM stack
  (Grafana) + Grafana MCP; the **agent exports as `sebastian-agent`** with the
  transcription per turn in Loki; heartbeat (`serial_age`) distinguishes alive-quiet
  from dead. (`tools/telemetry/`, `agent/telemetry.py`)
- **Devcontainer** (`docs/DEVCONTAINER.md`): entire environment in container (build
  ESP-IDF+Zig, agent with F5, token, LGTM), everything in the Run and Debug dropdown
  (Nixon pattern), agent venv in named volume. Flash from the host (native
  auto-reset) — over TCP the USB-JTAG does not forward the reset (manual download mode).
- **Field bug caught**: `@min(comptime, x)` narrows the type to u9 → overflow;
  it crashed the barge-in every session. Rule: reproduce the pure logic on host with
  the toolchain's zig before flashing.

The previous base (two-way voice, beam ASR, LED/mute) remains as it was —
commits in `origin/main`:

- `feat(firmware): two-way LiveKit voice on XVF3800 + XIAO ESP32-S3`
- `fix(firmware): use XVF ASR beam (right slot) to kill the tin-can double-NS`
- `feat(firmware): LED-ring DoA + mute UI, and boot health/error hardening`

## What works

- **Wake word on-device** (`docs/WAKE_WORD.md`): microWakeWord trained on the
  M4 Pro with 9 Spanish Piper TTS voices + 120 real recordings via XVF.
  99.3% recall, <1 false positive/hour. C/C++ component `firmware/components/mww`
  (TFLM + microfrontend) + Zig task `wakeword.zig`. The `app.zig` loop gates the
  LiveKit session with the detection — **activation-based architecture, not always-on**.
- **Zig Firmware** on ESP-IDF v5.4, with **handwritten `extern` bindings**
  (not `@cImport`): compiles and links clean for Xtensa. The old blocker
  translate-c↔newlib is resolved — Zig no longer parses C headers.
- **XVF3800 on master 1.0.7 "inthost" firmware**: our own firmware
  DFU-flashes it via I2C on the first boot (`xvf_dfu.zig`, without ESPHome) and
  un-mutes it (GPIO30). Subsequent boots detect it already on 1.0.7 and skip it.
- **I2S Clock**: the XVF is **master at 48 kHz** (32-bit, stereo); the ESP is
  **slave** over **two separate I2S ports** (mic RX on `I2S_NUM_1`, speaker TX
  on `I2S_NUM_0`) sharing BCLK/WS. The RX channel is shared between the
  wake word task (idle) and `mic_src` (active session) with resync on each hand-off.
- **Microphone capture**: direct reading from the I2S paced by the consumer
  (consumer-paced, no ring buffer), taking the **RIGHT/ASR beam** from the XVF
  (channel selectable at installation via `config.zig`); publishes **Opus at
  48 kHz**. The **NS is done by the agent's BVC** (single pass).
- **Speaker**: `av_render` → I2S TX → **AIC3104** → speaker.
- **UI**: **LED ring pointing to whoever is speaking** (DoA) + **mute button**
  (ring off when muted).
- **Boot health banner**: the boot reports `BOOT OK` / `DEGRADED`
  (includes wake word model loading).

## Recently resolved

- **Simple silence closing**: the fixed session timeout was replaced by
  local VAD on `mic_src.level()` with a 20 s minimum, closing after 12 s of
  silence and a 90 s safety maximum. This avoids cutting off sentences due to a
  fixed counter and leaves LINGER/DDSD as a later semantic improvement.
- **AEC WORKING (2026-07-02, `docs/AEC.md`)**: the factory build came with
  `AEC_FAR_EXTGAIN=0.0` → the AEC never adapted (it believed the speaker was muted). Fix:
  `FAR_EXTGAIN=1.0` via I2C at boot, verified by readback and reflected in
  the boot health (`xvf_aec.applyConfig`). `AECCONVERGED=1` verified; the agent
  no longer transcribes itself at full scale. Collateral: the software volume
  was a no-op all along (noop set_vol suppressed esp_codec_dev's sw-vol)
  — now the control is real. The diagnosis left the complete table of XVF I2C
  commands (`docs/xvf3800_command_map.txt`), an in-band reference probe
  (`xvf_aec.probeReference`) and serialized I2C access (lock in `xvf_dfu.xfer` —
  the control protocol is write+read in two transactions and xvf_ui polls
  DoA every 80ms).
- The session is opened by wake word with **fresh token per session + explicit
  agent dispatch** (see `docs/BUILD_AND_RUN.md`).

## Open points (non-blocking)

1. **AEC fine-tuning** (`docs/AEC.md`): measure the real delay with chirp if
   we want to squeeze ERLE; try volume >60; suppression of residual echo from the
   comms channel if necessary.

## Recurring operational notes

- **Exactly one agent process.** Several processes make it "talk to itself":
  `pkill -9 -f "agent.py"` and start just one.
- **Explicit dispatch by token (there is NO MORE "fresh room + reset").** The token
  server creates the agent dispatch by API on each token, so re-waking
  to a live room already puts the agent in (the "not responding after re-wake" bug resolved).
- **Board at `/dev/cu.usbmodem101`.** If it does not flash (boot-loop / esptool does
  not connect): physical USB **power-cycle**, or hold down **BOOT** when
  plugging in (manual download mode). The board does not brick. To flash from the
  **devcontainer** over TCP: `make serial-share` on the host + manual download mode
  (the USB-JTAG auto-reset does not cross rfc2217).
- **If the entire I2C bus gives probe timeout** (XVF and AIC3104 muted): the XVF has
  hung — physical USB power-cycle. Usually happens after many consecutive reflashes.

See [BUILD_AND_RUN.md](BUILD_AND_RUN.md) for the complete operation,
[WAKE_WORD.md](WAKE_WORD.md) for the wake word system and
[TROUBLESHOOTING.md](TROUBLESHOOTING.md) for the debugging playbook.
