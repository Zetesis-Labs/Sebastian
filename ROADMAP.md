# ROADMAP — Sebastian as an advanced voice agent

Architecture audit result (2026-07-02). Defines the path from the
current state (working bidirectional voice, no invocation model) to a
home voice agent on par with Alexa+ / Gemini for Home, building upon what
already exists in the repo.

**Thesis:** far-field audio (XVF3800) and transport (LiveKit/WebRTC) are already
well resolved. What's missing is not signal or network: it's (1) an **invocation
model** — today the mic is published to the cloud 24/7 and the agent responds to
everything — and (2) an **agent** that goes from prototype (79 lines) to product
(tools, memory, turn detection, dispatch).

---

## 1. Starting state (audit)

### Solid — do not touch

- Audio chain: XVF I2S master, two separate slave ports, consumer-paced
  capture without ring buffer, raw ASR beam + single pass of NS (BVC).
- XVF DFU via custom I2C in Zig, with factory image as safety net.
- Secrets hygiene (no sensitive data tracked).

### Actionable findings

| # | Finding | Where |
|---|---------|-------|
| 1 | **No invocation**: audio always published → privacy, cost and UX. Gap #1. | `mic_src.zig` |
| 2 | **AEC/barge-in not closed**: volume capped at 35/100 due to residual echo. The RIGHT/ASR channel is post-AEC but without residual echo suppression; the XVF's AEC reference enters through the speaker's I2S line (shared GPIO44 — see `num_channels: 1 # Try mono for XVF AEC` in `homeassistant/respeaker.yaml`). Verify AEC convergence via control protocol (AEC resid). | `board.zig`, `docs/STATUS.md` |
| 3 | **`agent.py` is a prototype**: no tools, no memory, no turn detection, unconditional recording to `/tmp`, dispatch via workaround (fresh room + reset). | `agent/agent.py` |
| 4 | **livekit-agents ~=1.2 vs 1.6.4 current**: deprecated text turn detector (replaced by native-audio Turn Detector v1.0, with Spanish), Adaptive Interruption Handling GA, `MCPToolset`. Update before building on top of it. | `agent/pyproject.toml` |
| 5 | **Static sandbox token embedded** in the binary: no rotation, requires reflash to change it. | `firmware/main/secrets.zig` |
| 6 | **Entire `wakeword/` gitignored and untracked**: datasets yes, but custom scripts (`download_es_voices.py`, `split_wakeword.py`) should be versioned. | `.gitignore` |
| 7 | C SDK in Developer Preview (v0.3.10). Pros: since v0.3.7 it includes **RPC, data streams and user packets** — the device↔agent control channel already exists. | `managed_components/livekit__livekit` |

---

## 2. Target architecture: the attention engine (invocation)

Invocation is not a trigger, it's a **layered attention system** (what
Alexa+ solves with its on-device fusion and Google with Continued Conversation /
Gemini Live). Sebastian can play in that league with audio alone because it exposes something
an Echo does not: **DoA and beam control of the XVF via I2C**.

### State machine (firmware, mirrored in the agent)

```
IDLE ──activity──► ARMED ──wake──► ATTENDING ──turn──► ENGAGED
                      ▲   (LED beam)     │                   │
                      │                   └─── response ─────┤
                      └──── timeout ◄── LINGER (follow-up) ◄─┘
```

- **ARMED** — LiveKit room connected, `mic_src` publishes **silence** (reuse
  `setMuted()` as a state-machine controlled gate). Opus DTX ≈ 0
  network cost. Wake word runs on-device; no voice leaves the device.
- **ATTENDING** — wake detected: gate open **with pre-roll** (~1.5 s in PSRAM
  including the wake word), LED towards speaker and **beam lock** in that direction
  (existing I2C command, see `lock_beam()` in the formatBCE component).
- **ENGAGED** — conversation with semantic turn detection and barge-in.
- **LINGER** — ~8 s of post-response listening **without wake word** with DDSD gate on
  the agent (below). Dim LED = "I'm still listening". Parity with Alexa+'s
  blue-bar and Continued Conversation.

### The seven invocation signals

1. **microWakeWord "Sebastián" on-device.** Trainer already in `wakeword/` with Piper
   es-ES/es-MX voices. V2 Models (MixedNet): arena ~23 KB, <10 ms per
   20 ms hop; up to 3 models + VAD on an S3. Requires decimating the ASR beam
   48 kHz→16 kHz (which also feeds the pre-roll).
2. **Two-stage verification**: loose on-device threshold (recall) +
   re-verification on the agent with the pre-roll (openWakeWord/classifier).
   Sensitive from afar without false positives.
3. **Follow-up with DDSD gate** (LINGER): agent-side fusion of (a) acoustics —
   stable DoA, same speaker —, (b) lexicon — is the STT partial coherent as a
   reply? (cheap LLM <100 ms) —, (c) prosody/energy. Recipe = Apple
   arXiv 2411.00023. Evaluable shortcut: Gemini Live's **Proactive Audio** gives this
   layer for "free".
4. **Button**: tap = wake without voice; long-press = push-to-talk.
5. **Proactivity with courtesy** (agent→device RPC): chime + LED before
   speaking; if `mic_level` indicates conversation in the room, the announcement waits or
   degrades to LED. Home Assistant timers/calendar/announcements.
6. **DoA as continuous telemetry** (lossy user packets ~5 Hz): feeds the DDSD
   gate, the ring UI and prepares for multi-device arbitration (best
   wake+SNR score wins).
7. **Speaker ID on the agent** (pre-roll embedding): memory and instructions
   per person, hardened threshold against unknown voices, policies ("a
   guest cannot open the door"). Via STT with diarization (Speechmatics) or
   custom embeddings (ECAPA).

### Extras with personality

- **"para"/"stop" Wake word** active only during TTS/timer (multi-model
  microWakeWord, like the internal `stop` model in HA Voice PE).
- **Whisper mode**: low RMS utterance + nighttime → response at
  proportional volume (`esp_codec_dev_set_out_vol`).

### Economics of "always connected"

Mic gated ≈ 0 network, but LiveKit Cloud bills participant minutes. Options:
(a) room timeout after N mins in ARMED + reconnection upon wake (~1–2 s), or
(b) **LiveKit self-host on Cortes** (GitOps Mileto/ArgoCD/Infisical already
existing): marginal cost zero and voice doesn't transit third-party clouds except the
model segment. The Python agent deploys as a Deployment on Cortes.

---

## 3. The agent up to 2026 standards

1. **Upgrade to livekit-agents 1.6.x** and enable: Turn Detector v1.0
   (audio-native, Spanish; `v1-mini` on CPU if self-hosted), Adaptive Interruption
   Handling (auto-resume on false interruptions; requires closing AEC finding
   #2), `preemptive_generation`.
2. **Decoupled model via config**: today OpenAI Realtime; 2026 alternatives —
   `gpt-realtime-2` (~$0.18–0.46/min), `gpt-realtime-mini` (~70% less),
   Gemini native audio (~10× cheaper, Proactive Audio), Nova 2 Sonic
   (~$0.015/min, Spanish, async tools). Measure with
   `SessionUsageUpdatedEvent` and decide by real $/min.
3. **Tools via MCP** (`MCPToolset`): Home Assistant first (already in
   the project); device timers/alarms via RPC (they ring even if internet
   drops); Zetesis search (mcp-typesense) as documentary memory.
4. **Memory per person** keyed by speaker ID (mem0/Zep or custom
   Payload+Typesense). Dynamic instructions: who is speaking, time, context.
5. **Operation**: custom token server (`livekit-api`) + **explicit dispatch**
   (`agent_name`) — eliminates "fresh room + reset" and "manual single process";
   verification recording behind env var; evals with the 1.6.0 simulation
   framework in CI.

---

## 4. Phases

### P0 — Foundations (real basic invocation)

- [x] **Spike: microWakeWord + LiveKit stack coexisting on the S3** — DONE and
      validated on hardware. microWakeWord (62KB, TFLM, 48KB SRAM arena) runs on
      a free core and gates the LiveKit session. See `docs/WAKE_WORD.md`.
- [x] Train "Sebastián" with the `wakeword/` trainer (Piper es).
- [x] Upgrade `livekit-agents` to 1.6.x — explicit pin `~=1.6` + turn detection
      by OpenAI's **semantic VAD** (multilingual; LiveKit's turn detector doesn't
      compose with RealtimeModel). Recorder gated behind `SEBASTIAN_RECORD=1`.
- [x] Gate + pre-roll — DONE and validated E2E (2026-07-03): 12s ring in PSRAM
      **event-driven** — records until the handoff instant (CONNECTED room +
      agent inside), window anchored to the wake (2s before + the whole connect),
      sending retries every 500ms, and the agent discards the gate silence
      that fell between pre-roll and live. "Sebastián, turn on the living room light"
      arrives completely in one go without waiting for the green light. The room is still
      per-session (not always-connected); the remaining seam is ~10-200ms in the
      handoff (one I2S reader at a time).
- [x] Device↔agent data protocol — DONE (2026-07-03): incoming
      `sebastian.agent_state` (agent states → mic half-duplex gate +
      session activity) and outgoing `sebastian.barge_in` (wake word over its
      voice → `interrupt()`). Only volume/LED via RPC missing as an extra.
- [x] Token server + explicit dispatch with `agent_name` — DONE and validated on
      hardware. `agent/token_server.py` mints a fresh token per session with the
      embedded dispatch; the firmware requests it via `token_http.c` + `token.zig` shim.
      Out goes the static JWT and the "fresh room + reset" dance.

### P1 — Parity with the competition (natural conversation)

- [x] Close AEC reference (finding #2) — DONE (commit 2f611f8, `docs/AEC.md`):
      root cause factory `FAR_EXTGAIN=0.0`; fix via I2C with readback,
      `AECCONVERGED=1` verified, real software volume recovered. Fine tuning
      pending (chirp delay, volume >60, residual suppression if needed).
- [x] Barge-in — DONE (2026-07-03) Alexa style: the wake model monitors the
      gated audio while the agent speaks; "Sebastián" cuts it off instantly.
      (Natural acoustic barge-in is contingent on the AEC converging.)
- [x] LINGER (core) — DONE (2026-07-03): the agent's voice counts as
      activity (state via data channel), closure by real conversation
      silence, 90s cap → 10 min safety net. Pending real-world validation
      that closures occur via `silence_timeout` and not the cap.
- [x] `end_session` by voice ("shut up"/"stop"/farewell) — tool that deletes the
      room; the device returns to idle on its own.
- [x] Home Assistant MCP — DONE and validated (lights, GetLiveContext); with
      anti-hallucination grounding in the instructions. On-device timers pending.
- [ ] Remove half-duplex when the AEC converges (AEC project, below).

### P2 — Differentiation (what an Echo doesn't do)

- [ ] Speaker ID + memory per person.
- [ ] Proactivity with courtesy policy.
- [ ] Whisper mode.
- [ ] Beam lock by utterance (DoA).
- [ ] Multi-device with DoA/score arbitration.
- [ ] LiveKit self-host + agent deployed on Cortes (GitOps).

### DevEx / observability (2026-07-03)

- [x] **Observability**: serial → `bridge.py` → OTLP → LGTM stack (Grafana) + Grafana
      MCP; agent exports as `sebastian-agent` with per-turn transcription
      in Loki; heartbeat (`serial_age`) distinguishes alive-quiet from dead. `docs`
      in `tools/telemetry/README.md`. **Pending**: firmware vitals
      (heap/RSSI/temperature/in-session levels) + alerts — see Pending #4.
- [x] **Devcontainer** (`docs/DEVCONTAINER.md`): ESP-IDF+Zig build + agent (F5) +
      token + LGTM inside; all actions in Run and Debug (Nixon pattern);
      agent venv in named volume (not in the mount, prevents incomplete venvs);
      Pylance via `pyrightconfig.json` at the root. Flash: native host or TCP with
      manual download mode (USB-JTAG doesn't forward the reset via rfc2217).

### Immediate fixes (phase-independent)

- [x] Fine-tune `.gitignore`: only `wakeword/trainer/` ignored, scripts + model versioned.
- [x] `agent.py` recording behind an environment variable (`SEBASTIAN_RECORD=1`).
- [ ] Document in `config.zig` the invocation states to the level of detail
      of the LEFT/RIGHT decision.

### Pending (updated 2026-07-03, recommended order)

1. [ ] **Validation in daily real-world use**: closures by `silence_timeout` (not
       `max_duration`), SCTP storm should die with clean closures
       (`sebastian_sctp_init_total`), phantom turns ≈ 0 with half-duplex.
       Just requires using the device and looking at the dashboard.
2. [ ] **Wake word reliability**: lower threshold 0.62 → ~0.50 and measure false
       positives with the "session without user turn" proxy (transcriptions).
       If not enough, re-train with real captures (pre-rolls are a dataset).
       Full anti-phantom plan (two-stage verify, speaker-ID, DoA fusion) in **§9**.
3. [ ] **Determinism harness**: host Zig tests for pure logic
       (decimator, softClip, pre-roll window, barge chopping — the kind of bug that
       crashed 5 times today, catchable in seconds without hardware), Python device
       simulator (fake participant: pre-roll + audio + asserts on
       transcriptions/states) and `make check` pre-flashing.
4. [ ] **Firmware telemetry batch** (the missing half of "god-tier telemetry":
       the agent side + heartbeat are already there): audio levels IN session, free heap,
       RSSI, temperature + Grafana alerts (reboot, SCTP, deaf channel, mute serial).
       *Partial (2026-07-08): internal/DMA free-heap logging landed (`logHeap()` at
       boot + around connect, §8). Still pending: session audio levels, RSSI, temp,
       and wiring these into Grafana alerts.*
5. [ ] **AEC Project** — PLANNED (2026-07-03, multi-agent exploration):
       phased plan in `docs/implementation/10-aec-fullduplex-plan.md`.
       Key finding: the `FAR_EXTGAIN` "fix" was likely a placebo (the scale
       is dB, 0.0 = unity); candidate #1 = `REF_GAIN=8.0` clipping the
       internal reference to full-scale. If it converges → half-duplex out →
       natural full-duplex + loud volume without echo.
6. [ ] **Volume by voice** (agent→device RPC; the data pipeline already exists in
       both directions) + LED by agent state already received.
7. [ ] **Wake sensitivity in continuous speech** (overlaps with #2).
8. [ ] **Agent AGC**: re-framer to 10ms before the APM if recovered.
9. [ ] **Pre-roll PSRAM silently degrades**: reflect it in boot health.
10. [ ] On-device timers via RPC (they ring without internet).

---

## 5. Risks and honest caveats

- **microWakeWord + WebRTC without public precedent** — validate with the P0 spike
  before committing the rest. WakeNet (ESP-SR) is not a Plan B today: its self-serve
  pipeline does not support custom Spanish.
- **client-sdk-esp32 in Developer Preview** — APIs subject to change; pin
  version and review changelogs before each bump.
- Market data (versions, prices, Alexa+/Gemini features) are a
  2026-07 snapshot; re-verify before cost decisions.

## 6. The gap to "Echo level" — audio/mic management (2026-07-04)

Sticking to the audio and microphone pipeline (not skills/services), this is what
currently separates Sebastian from an Alexa/Echo. What **already works** (gated wake,
event-driven pre-roll, half-duplex without echo, full-duplex on fixed beam, AEC
config) is the solid foundation; the gap is mainly **#1, #3, #4 and #7**.

1. **Convergent AEC WITH tracking (the real big one).** An Echo hears you while
   music is playing, from anywhere in the room, with the beam tracking you **and**
   canceling the echo at the same time. Today Sebastian has to choose: **fixed beam** (linear AEC
   converges but you lose tracking) or **adaptive beam** (it tracks you but
   half-duplex). Reconciling adaptive tracking + stable AEC is pending
   #1 — path B (XVF comms channel) or freezing the beam only during
   adaptation.

2. **Barge-in over loud playback.** Alexa hears you say its name with music at
   full volume. Sebastian's barge-in has been tested over its own TTS voice, not
   over loud music. It requires the AEC to withstand a far-end reference at high SNR and
   the wake to detect on the residual.

3. **Decent endpointing / VAD.** Alexa knows when you have finished speaking (end of
   turn). Sebastian uses a level-based `silence_timeout` of 12s — crude, and in fact
   it's what was cutting sessions. A real VAD/endpointing is missing (energy + time
   + context), not a fixed threshold.

4. **Reliable wake word.** Fine-tuned recall/precision, few false positives, robust
   in noise and at a distance. The threshold was raised `0.62 → 0.80` with field data, but
   it's lacking **systematic false measurement** and validation at distance/noise. Pure model
   + data tuning.

5. **AGC / distance normalization.** Hearing just as clearly near as far.
   Sebastian has a fixed `mic_gain` and a static SHIFT; Alexa normalizes the level
   automatically based on distance/volume.

6. **Noise suppression and dereverb in far-field.** The XVF already has NS + de-reverb
   (the comms channel), but Sebastian blocks it by using the raw ASR channel. For
   noisy/reverberant rooms, that post-processing must be exploited without ruining the ASR.

7. **Session/transport robustness.** An Echo doesn't crash. Sebastian has the SCTP storm
   (esp-webrtc-solution#186), disconnections due to `silence_timeout`, and the fragility of the
   **S3's native USB** (both the flash and the Web Serial re-enumerate/reset the
   board). Reproducing the "never fails" reliability is an entire axis.

8. **Acoustic design (hardware-ish).** The diagnosed non-stationary echo is partly
   physical: mic-array and speaker in the same enclosure. Echos have
   careful isolation/geometry; without that, the AEC always has a harder time.

In one sentence: the first three (#1 full-duplex with tracking, #3 endpointing, #4
robust wake) are the core of "how a smart speaker manages audio/mic"; #7 is what makes it usable daily.

### Tooling: web installer publication (RESOLVED 2026-07-04)

The web installer (React) is served via GitHub Pages. The `deploy-pages` was failing on
GitHub runners ("Deployment failed, try again later"); it was moved to the self-hosted
runner **`herschel-runners`** and works. The **factory firmware is built in
the Action itself** (esp-idf-ci-action, ~4-7 min) and published alongside the installer,
with verification that the image carries no credentials. The flashed image
is built by CI, a local binary is never uploaded.

**Update (2026-07-05, PR #3):** the **web installer bundle itself is now also built
in CI** (pnpm + Node 20, on `herschel-runners`) and `docs/installer/` is no longer
committed to the repo (gitignored). A change under `web-installer/` auto-builds +
deploys; the build is never checked in. (The recurring `deploy-pages` "try again
later" is GitHub Pages platform flakiness, not the pipeline — a fresh
`workflow_dispatch` run clears it.)

## 7. Exploiting the XVF DSP → full-duplex with tracking + auto-calibration (2026-07-04)

**Thesis:** ~70% of the "top quality" mic/speaker is not building new DSP,
but **using what the XVF3800 already comes with** (AEC, beamforming, non-linear
residual echo suppressor, NS, de-reverb, AGC, limiter). Today we cap the **raw ASR
beam (RIGHT slot)** and throw away all the post-processing of the **comms channel (LEFT
slot)**. This section is the plan to squeeze it, with what's already validated in hardware.

### Goal: the two capabilities separating from an Echo

1. **Full-duplex WITH tracking** — talking over it while it plays, from
   anywhere in the room, with the beam tracking you **and** the echo canceled.
2. **Auto-calibration by voice** (*"Sebastián, calibrate"*) — adapting itself and
   persisting in NVS, solving two real scenarios: **moving the device** and
   **plugging in a different speaker** (unknown sensitivity/THD).

### Path B — VALIDATED in hardware (`probeDualChannel`)

The trade-off we had was "**fixed beam OR tracking**": fixed beam → linear AEC
converges (current full-duplex) but you lose tracking; adaptive beam → it tracks you
but the AEC never settles and echo bleeds through. **Path B breaks it**: the
**non-linear** residual suppressor of the comms channel cancels the echo **without converged linear AEC and with the
adaptive beam**. Measured with noise at agent level:

| | residual echo (average echo-rise) |
|---|---|
| Raw ASR Beam (RIGHT), adaptive | **+101 655** (unusable) |
| Comms channel (LEFT), adaptive, unconverged AEC | **−2 568** (below ambient) |

That is: **full-duplex with tracking is viable through the comms channel.** The mechanism
is proven; it's no longer a research risk.

### Current state and what's missing

- **Full-duplex WITHOUT tracking**: DONE and in production (path A — fixed beam +
  converged AEC + keepalive by render). Works if you are in the beam's zone.
- **Full-duplex WITH tracking**: mechanism validated; missing **integration + voice
  tuning** (no new architecture — the plumbing already exists: `mic_channel`,
  `full_duplex` flag, gate bypass):
  1. `mic_channel = .left` + `fixed_beam = false` → 2 flags + reflash.
  2. **Tune the comms AGC** (factory 32× = the "tinny sound") to avoid
     degrading the ASR.
  3. **Wake word on the processed channel**, or wire wake-on-RIGHT + session-on-LEFT
     (both slots arrive on the same I2S stream).

### ⚠️ The only real gate: double-talk

The probe measured **echo** (agent speaking, user quiet), **not double-talk** (both
at the same time). That is the Achilles heel of non-linear suppressors: if it's too
aggressive **it eats YOUR voice when you talk over it** → you lose exactly what you sought.
Knob: `PP_DTSENSITIVE` (resid 17/31, currently at 0). **Only validated with voice in the room.**
It can be resolved with the knob or reveal a limit. Estimate: **~1 session at home**
for a first version; possible 2nd round if double-talk requires tuning
`DTSENSITIVE`/`gamma_e`.

### Plan for the `calibrate_audio` tool ("Sebastián, calibrate")

Productize today's probes as a voice tool. Prerequisite: the **volume
actuator** (see findings). Flow:

1. `calibrate_audio` tool on the agent → data channel command (with ack/retry
   for the SCTP storm) → firmware routine (~10-15 s).
2. The routine sweeps the output gain with noise, measures pre-AEC echo, picks the
   highest level with headroom (−6 dB), verifies `converged=1`.
3. Saves `out_gain_db` to **NVS** (`sebastian` namespace, like provisioning);
   `applyConfig` re-applies it on every boot with verified write.
4. Agent: "Done, calibrated". Bonus: the firmware already detects clipping in session
   (`live_echo=32767`) → it can **suggest** calibration or auto-correct.

Resolves **moving the device** and **changing the speaker** with one phrase.

### Anchor-findings (to avoid repeating work)

- **`FAR_END_DSP_ENABLE` (35/25) is an AEC prerequisite.** With it at 0 the far-end
  detector sees no energy → AEC never adapts → speaker↔mic feedback loop. The XVF
  reverts to its build default (0) on **power-cycle** (not on `esp_restart`), so
  unplugging killed the AEC. **Fixed in `applyConfig`** with verified fail-closed
  write (commit `a5b1534`).
- **`FAR_EXTGAIN` is NOT a master volume** (measured: −12 dB moved the echo ~3%). It's
  AEC metadata. And **there is no volume API** in the entire render chain (which is
  why sw-vol was always a no-op). **The real actuator** = a custom wrapping `audio_render`
  (~80 lines) that scales the PCM (Q15 gain) before the internal I2S render;
  coherent with the AEC reference (same signal to DAC), reduces THD
  and enables ducking + volume by voice + the `calibrate` actuator.
- **XVF config-drift**: reverts to build defaults on every power-cycle. Every
  adopted knob MUST be written in `applyConfig` (or `SAVE_CONFIGURATION` 48/9, but
  we prefer explicit writing, visible in git).
- **Programmable slot routing** (`OP_L`/`OP_R`, 35/15,19): comms can be routed
  → any slot without touching `mic_src`.
- **Specific knobs to touch**: `PP_AGC*` (17/10-18, distance normalization),
  `PP_DTSENSITIVE` (17/31, double-talk), `PP_GAMMA_E/ETAIL/ENL` (17/24-26,
  suppressor aggressiveness), `PP_MIN_NS` (17/21, NS floor), `SYS_DELAY` (35/26,
  reference↔mic alignment).
- **Gapless reference**: `auto_clear_after_cb` injects zeros on every underrun →
  gaps in the far-end reference that degrade adaptation; larger FIFO / no
  underruns = more stable AEC.

### Diagnostic tools (in the tree, boot flags at `false`)

- `logPpConfig()` — post-processor dump on every boot.
- `probeDualChannel()` (`config.probe_dual_channel_on_boot`) — the path B
  experiment; reversible, ~12 s of noise, restores everything.
- `probeOutputGain()` (`config.probe_output_gain_on_boot`) — the probe that discarded
  `FAR_EXTGAIN` as volume.

### Note — agent provider (2026-07-04)

Migrated to **Gemini Live** by default (`SEBASTIAN_MODEL_PROVIDER=gemini`, lower
cost) with OpenAI Realtime as fallback. The native-audio models **reject
explicit BCP-47 codes** (`es-ES` → APIError 1007 → Live session closes →
agent goes mute with the device session open): the language is now
configurable (`SEBASTIAN_GEMINI_LANGUAGE`) and by default **auto-detect** (commit
`55b869f`).

## 8. Beyond the assistant — the device as a thin audio endpoint (2026-07-05)

Strategic pivot after shipping runtime modes and paying for a memory bug the hard
way: stop adding capability **to the firmware** and start adding it **to the
server**. The ESP32-S3 is at its internal-RAM edge (proven below), so the winning
shape is a **dumb, low-footprint endpoint** (4-mic array + speaker + LED ring +
on-device wake word + one WebRTC session) with **all brains server-side** (the
agent, a media publisher, classifiers over the already-streamed audio). "Add a
feature" then means writing server code with **zero firmware/RAM cost on the
device** — cheap and, crucially, safe (no touching the fragile stack).

### Shipped 2026-07-05

- [x] **Mode-based web installer** (PR #2): the installer provisions an operating
      **mode** (`full_duplex` / `half_duplex`) + `audio.fixedBeamAzimuthDeg` into
      NVS; `config.zig::load()` reads them at boot. Switching full↔half duplex is
      now a **re-provision, not a reflash**. `full_duplex`/`fixed_beam`/`azimuth`
      became runtime `var`; `mic_channel` + `session.*` stay compile-time.
- [x] **Memory bug + lesson.** Making the three `probe_*_on_boot` flags runtime
      `var` (for a "diagnostics" mode) defeated Zig's **dead-code elimination**, so
      `xvf_aec`'s ~15 KB of static probe buffers (`tone_buf` + `probe_rx_buf`,
      `[960*2]i32` each) stayed resident in internal RAM. Those 15 KB starved the
      TLS hardware-AES DMA (`esp-aes: Failed to allocate memory`) → the WSS
      handshake looped `Connection already in progress` forever and LiveKit never
      connected. It looked exactly like a network/IDF problem for hours; it was
      not. **Fix:** probes back to comptime `const false` (compiler elides them +
      the 15 KB, verified: `tone_buf` gone from the `.map`), diagnostics mode
      dropped from the installer. **Rule for the whole codebase: never turn a
      config `const` into a runtime `var` if it gates a code path with large static
      buffers — `var` keeps them all.**
- [x] **Internal-RAM measured + wake-word arena right-sized (2026-07-08).** Added
      `logHeap()` (free/largest block for `MALLOC_CAP_INTERNAL` and `…|DMA`) at boot
      and around `room_connect`, plus a `arena_used_bytes()` log in `mww_init`. The
      arena was a round 48 KB but the model truly needs **36 268 B** — trimmed to
      **40 KB** (13 % margin), returning **8 KB** to internal RAM. Measured at boot:
      internal free **57.5 → 65.6 KB**, DMA free **50.1 → 58.1 KB**. The margin is no
      longer a guess. (Largest contiguous block held at ~31.7 KB — bounded by a fixed
      allocation above the arena, a separate fragmentation matter.)

### Foundation: self-host LiveKit on Cortes

Elevated from a P2 economics option to **the enabler** for everything below.
LiveKit is OSS (Apache-2.0); deploy the Helm chart on the Talos `cortes` cluster
via Mileto GitOps (ArgoCD/Infisical). Wins: **24/7 at zero per-minute cost**,
**LAN-local** (sub-ms; home audio stops round-tripping a third-party cloud), keys
in Infisical, the Python agent as a Deployment. The device barely changes — it
already gets `serverUrl` from the token response, so just point the token server
at `wss://livekit.<lan>` and rotate keys. **Fiddly part:** WebRTC-on-K8s — exposing
the media **UDP port range** + the **announced node IP** (hostNetwork or a UDP
LoadBalancer on Talos). **Possible bonus to MEASURE, not assume:** `ws://` LAN
signaling would skip the WSS handshake that starved esp-aes; media DTLS still uses
AES, so it is not a guaranteed relief.

### HA-native `media_player` without touching the firmware

Requirement: HA sees the device as a controllable `media_player` (music via Music
Assistant, announcements, notifications). Two routes:

- **DLNA/UPnP MediaRenderer in firmware** → HA's built-in `dlna_dmr` auto-discovers
  it. Zero HA-side code, LAN-local, serverless — **but adds a decode+HTTP+SSDP
  stack to the RAM-tight device** as a mutually-exclusive mode. **Deferred**: only
  worth it if we ever want native HA music with *no* always-on server.
- **Custom HA integration backed by WebRTC (chosen).** A HA `custom_component`
  exposing a `media_player` whose `play_media`/volume map to a **publisher** that
  joins the LiveKit room and streams to the device. Music Assistant feeds it;
  ducking/mixing handled server-side. → **native HA entity + zero device firmware +
  one transport.** Cost: real dev we own (component + robust publisher), and it
  depends on the (now self-hosted) LiveKit being up.

### Notifications / announcements / Grafana → voice, via the agent

Reuse the pipeline: the agent already does TTS and speaks through the device. Add a
"say this" path (RPC/data-channel or a small HTTP API) → HA/Grafana call it →
device speaks. Grafana alert → webhook → HA automation (or direct) → *"CPU 95% on
cortes"*. **No DLNA, no publisher needed for this** — pure extension of the voice
pipeline, highest value-per-effort.

### Feature menu under this model (all server-side, device untouched)

| Axis | Features | Reuses |
|---|---|---|
| **Mic ML** (on the streamed audio) | sound-event detection (glass break, smoke alarm, crying, barking) → HA; speaker ID / presence; **room transcription + summary**; baby-monitor / remote-listen | Zetesis transcription stack; the WebRTC mic already published |
| **Homelab voice-ops** (very on-brand) | query Grafana/Prometheus/K8s by voice; conversational incident announcements; "restart pod X" | Grafana + agent + MCP already wired |
| **Smarter assistant** | **RAG over the Zetesis knowledge base**; per-person memory; multilingual; personas/voices | mcp-typesense (already MCP-connected) |
| **Home** | scenes/conditional routines; DoA follow-me; camera/doorbell announcements (+ vision on the snapshot) | HA MCP already wired |
| **Media** | music (Music Assistant), TTS notifications from anything, timers/alarms/reminders, ambient/sleep sounds, multi-room/intercom | the media_player + WebRTC rooms |
| **LED ring** | expose as an HA `light` entity, visual notifications, DoA | existing ring UI |

### Suggested order

1. **Self-host LiveKit on Cortes** — foundation for everything else.
2. **Notifications / Grafana → agent speaks** — quick win, reuses the voice pipeline.
3. **Custom HA `media_player` + publisher** — music via Music Assistant.
4. **Sound-event detection + homelab voice-ops** — highest home value, reuse the
   audio stream + Grafana/K8s.
5. *(Optional)* measure whether `ws://` LAN signaling relieves the esp-aes margin.

### Honest caveats

- **Privacy**: streaming the mic to the server 24/7 has real implications — gate it
  (only after wake, or an explicit "listen mode").
- **WebRTC-on-K8s** UDP/IP exposure is the real time-sink of the foundation.
- **RAM edge stands**: these features live server-side precisely because the device
  can't take more. On-device modes (assistant / DLNA-if-ever / intercom) remain
  **mutually exclusive**, not simultaneous.
- Vision needs a camera; multi-room needs more units.

## 9. Anti-phantom wake word — killing false activations (2026-07-05)

A daily-use priority in its own right: the assistant must **not wake when nobody
called it** (TV, music, a similar-sounding word, its own voice). Consolidates and
makes actionable what was scattered across §2 (signals #2/#7), §6 #4 and Pending #2.

### The framing that dictates the whole approach

False positives (phantoms) and misses ("it didn't fire" — seen live at 81 %
confidence) **pull in opposite directions on the on-device threshold**. So the fix
is **not** to raise the threshold on the board — that trades recall away and worsens
the miss problem. The right move is to **split recall from precision**:

- **On the board → recall.** Keep the threshold **loose** (fires easily, never
  misses you).
- **On the server → precision.** Re-verify each trigger with real compute and
  **abort silently** if it wasn't real.

This is exactly the §8 thin-endpoint model: dumb board, brains server-side. Most of
what follows is **server code, zero firmware/RAM cost**.

### Options (highest leverage first)

| # | Option | What it does | Roadmap ref | Cost |
|---|---|---|---|---|
| 1 | **Two-stage verification** | board fires loose → agent re-checks the **pre-roll** (openWakeWord / heavier classifier / ASR "did they say *Sebastián*?") → **abort session silently** if it fails. Gives recall **and** precision at once. | signal #2 | server-side, medium |
| 2 | **Retrain on the phantoms** | the pre-rolls of false wakes are **hard negatives** → the model learns not to fire on the TV / music / near-homophones. Durable. | Pending #2 | training loop |
| 3 | **Speaker verification** | only wake for **enrolled voices** → kills TV / media / guest phantoms wholesale (pre-roll embedding, ECAPA). | signal #7 | server-side, embeddings |
| 4 | **DoA + energy fusion** | a real person arrives from a **stable direction**; diffuse TV/room noise doesn't → cheap discriminator on the already-streamed DoA. | §2 (DoA telemetry) | server-side, cheap |
| 5 | **Context gating** | raise the bar / require verify **while music or the agent is playing** (where phantoms cluster; the agent's own voice saying the name is already forbidden in the instructions). Gate the wake on the AEC'd channel so the **own-speaker echo** can't self-trigger. | partial | medium |
| 6 | **On-device threshold / window** | quick knob, but **sacrifices recall** (worsens "didn't fire"). Last resort, not the primary lever. | §6 #4 | trivial |

### Honest caveats

- **Two-stage verify still opens a brief session per board trigger** (a spurious
  connect) just to abort before speaking. It kills the **audible** phantom, not the
  connect. With **self-hosted LiveKit on LAN** (§8) that connect is nearly free; on
  LiveKit Cloud it bills participant-seconds — a reason to pair this with the
  self-host foundation.
- **Own-speaker echo** (music/TTS re-triggering the wake) is a distinct source from
  external phantoms — attack it with #5 (gate during playback / run the wake on the
  AEC'd channel), not with the server re-verify.
- All of this needs a **false-positive dataset** to tune against — the "session
  without a user turn" proxy (Pending #2) is the cheap way to measure it before
  re-training.

### Suggested order

1. **Two-stage server re-verify** (#1) — kills audible phantoms now, and lets the
   board threshold go **loose** → also fixes the "didn't fire at 81 %" miss.
2. **Measure** false positives with the session-without-turn proxy → build the
   phantom dataset.
3. **Retrain** (#2) on that dataset; add **DoA fusion** (#4) as a cheap gate.
4. **Speaker-ID** (#3) if TV/guests turn out to be the dominant source.

## 10. Multi-unit / multi-room — N distributed devices (2026-07-08)

Makes actionable the P2 item "Multi-device with DoA/score arbitration" and the
"multi-room/intercom" row of the §8 feature menu. Question that triggered it:
*"could we have several microphones in a mesh network instead of just one?"*

### The shape: N identical thin endpoints, zero mesh

The §8 thin-endpoint pivot is exactly what makes this scale. Each unit is an
independent WebRTC client joining LiveKit; LiveKit rooms natively take N
publishers. **Adding a microphone = flashing another unit and pointing it at the
same token server — zero firmware changes.** All coordination lives server-side,
where the agent already sees every stream.

**Explicitly rejected: ESP-WIFI-MESH / ESP-NOW between units** (one unit as
gateway relaying audio for the others):

- The S3 is at its internal-RAM edge (§8 memory lesson); a mesh stack + Opus
  aggregation on a gateway unit is the opposite of the thin-endpoint rule.
- Conversational audio needs sustained bandwidth and low jitter; every mesh hop
  adds both latency and loss on a device that already handles erratic networks
  poorly (SCTP storm, §6 #7).
- It solves nothing: in a home with Wi-Fi coverage every unit reaches the AP
  directly. If coverage is the real problem, fix it at the **network layer**
  (mesh APs/routers) — transparent to the firmware.

### The one genuinely new piece: wake-word arbitration

If two units hear "Sebastián" at once, someone must pick the responder — what
Amazon calls ESP (Echo Spatial Perception):

1. Each unit reports a wake event with a **score** (wake-word confidence +
   beam energy/SNR; the XVF already exposes DoA and level, §2 signal #6).
2. The server collects events in a **~200 ms window**, picks the winner, the
   rest stand down silently (same silent-abort mechanism as the §9 two-stage
   verify — the arbitration score and the phantom-verify score are the same
   pipeline).
3. 100% server-side — zero firmware/RAM cost, consistent with §8.

### Room topology (decide when building)

- **Room per unit/zone** — isolation, per-room media, simplest arbitration
  (only same-room units compete via the audible-range window).
- **Shared room** — intercom and follow-me conversation across rooms for free,
  but arbitration must handle units that *don't* hear each other.
- Likely hybrid: room per zone + agent-orchestrated bridging for intercom /
  announcements (the §8 publisher already covers the announce path).

### Prerequisites and order

1. **Self-hosted LiveKit on Cortes** (§8 foundation) — N always-connected units
   on LiveKit Cloud multiply participant-minutes; on LAN they cost zero.
2. **§9 two-stage verify shipped first** — arbitration piggybacks on the same
   server-side wake-event pipeline; building them together avoids two parallel
   mechanisms.
3. **Second hardware unit** — arbitration is untestable with one device.

### Honest caveats

- **Hardware cost is the real scaling limit** (~ReSpeaker + XIAO per room), not
  software.
- Arbitration tuning (window size, score weighting) needs real two-unit testing
  in adjacent rooms — expect an iteration loop, not a one-shot.
- Multi-unit multiplies the §8 privacy caveat: more always-on mics streaming
  after wake; the gated-mic model must hold on every unit.
