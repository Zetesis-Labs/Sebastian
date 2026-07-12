# ROADMAP — Sebastian as an advanced voice agent

> Living doc. Started as a 2026-07-02 architecture audit; it now tracks the path
> from working bidirectional voice to an Echo/Gemini-for-Home-class agent, built
> on what already exists in the repo.
>
> *Refactored 2026-07-08 from a 10-section chronological accretion (thin-endpoint
> pivot §8→§6, anti-phantom §9→§7, multi-room §10→§8), then extended: §9 is the
> control-plane + device-control MCP execution layer, §10 is Risks. Commit
> messages referencing old numbers point to the same content by title.*

**Thesis:** far-field audio (XVF3800) and transport (LiveKit/WebRTC) are already
well resolved. The work is (1) an **invocation model** — *shipped*, (2) a
**product-grade agent** — *mostly shipped*, and now (3) two open axes that compete
for time: **Echo-level audio quality on-device** vs. **thin endpoint + server-side
brains**. Self-hosted LiveKit is the shared foundation under both.

---

## 1. Status at a glance (2026-07-08)

**Works today, hardware-validated:**

- **Voice loop**: bidirectional voice-to-voice; LED DoA; mute button.
- **Invocation**: on-device microWakeWord "Sebastián" gating a per-session
  LiveKit room; event-driven 12 s pre-roll in PSRAM; token server mints a
  **unique room per session** (`sebastian-<hex>`) with **explicit-only dispatch**
  (the LiveKit-recommended design — deleted the room-reuse guard/self-heal that
  a shared room needed; two same-named dispatches into one room = two agents both
  answering, so unique rooms make it structurally impossible).
- **Conversation**: barge-in ("Sebastián" cuts the agent off); LINGER follow-up;
  a **stop/farewell ends the session** (re-wake required) — model-driven (any
  language), a kill switch + phantom signal (§7); half-duplex gate while the
  agent speaks.
- **Audio/AEC**: AEC reference closed and converged; **path B SHIPPED
  (2026-07-08)**: full-duplex **with talker tracking** in production — session
  audio from the comms channel (on-chip residual echo suppression + NS +
  de-reverb), adaptive beam, wake word validated on the processed channel, and
  crisp barge-in (agent publishes `interrupted` → firmware flushes the render
  FIFO). Details + what remains (double-talk fine-tuning, `calibrate_audio`) in §5.
- **Agent**: livekit-agents 1.6.x; Gemini Live default (OpenAI Realtime
  fallback); Home Assistant via MCP with anti-hallucination grounding.
- **Ops**: observability (agent→Loki per-turn, heartbeat; `tools/telemetry/`);
  devcontainer (`docs/DEVCONTAINER.md`); web installer **and** factory firmware
  built in CI on the self-hosted `herschel-runners` (PR #3, `docs/installer/`
  gitignored — never committed; the `deploy-pages` "try again later" is GitHub
  Pages flakiness, cleared by a fresh `workflow_dispatch`, not a pipeline bug);
  runtime modes via provisioning (full/half duplex without reflash); **+20 KB
  internal RAM reclaimed** (→ §6).

**Original audit findings — all resolved** except "document the invocation states
in `config.zig`": no-invocation (#1), AEC reference (#2), agent-prototype (#3),
lk-agents upgrade (#4), static embedded token (#5), `wakeword/` gitignore (#6).

**Solid — do not touch:** the audio chain (XVF I2S master, two separate slave
ports, consumer-paced capture, raw ASR beam + single BVC NS pass); XVF DFU via
custom I2C in Zig with the factory image as safety net; secrets hygiene.

**Next up** → the prioritized backlog is §3.

---

## 2. The two axes and the tension

**Axis A — Echo-level audio/mic quality** (deep dive §5). What still separates
Sebastian from an Echo, staying strictly in the audio/mic pipeline: full-duplex
**with tracking**, real endpointing/VAD, a robust wake word, and daily session
robustness. This is mostly **firmware** work.

**Axis B — thin endpoint, server brains** (deep dive §6). The S3 is at its
internal-RAM edge, so stop adding capability to the firmware and add it
server-side instead: media publisher, ML over the already-streamed audio,
homelab voice-ops. "Add a feature" becomes server code at zero firmware/RAM cost.

**The tension:** Axis A wants firmware work; Axis B says freeze the firmware. The
resolution the repo is converging on: finish the **one validated firmware win**
(full-duplex with tracking — path B is proven in hardware, §5), then hold the
firmware and move everything else server-side.

**The shared foundation: self-host LiveKit on Cortes.** It appears as a
prerequisite in §6, §7 and §8, and as the economics fix for "always connected".
Deploy the OSS Helm chart on the Talos `cortes` cluster via Mileto GitOps
(ArgoCD/Infisical): **24/7 at zero per-minute cost**, **LAN-local** (sub-ms, home
audio stops round-tripping a third-party cloud), keys in Infisical, the Python
agent as a Deployment. The device barely changes — it already gets `serverUrl`
from the token response, so point the token server at `wss://livekit.<lan>` and
rotate keys. **Fiddly part:** WebRTC-on-K8s — exposing the media **UDP port
range** + the **announced node IP** (hostNetwork or a UDP LoadBalancer on Talos).
**Bonus to MEASURE, not assume:** `ws://` LAN signaling would skip the WSS
handshake that once starved esp-aes, but media DTLS still uses AES, so relief is
not guaranteed.

### What self-hosting the OSS server unlocks (2026-07-08)

Running the Go `livekit-server` (not Cloud) is not only cheaper — it hands us
server-side capabilities Cloud gated or billed, three of which close problems we
hit in the field:

- **Egress → free per-session audio recording, server-side, zero device cost.**
  Records each room's audio to our storage. This is the **audio forensics** the
  pre-deploy checklist (§6) wants: replay a failure moment and *hear* whether it
  was the user, residual echo, or noise — the exact ambiguity that cost hours on
  2026-07-08.
- **Webhooks + SFU stats → server-side observability that survives production.**
  The server emits room/participant/track lifecycle events + transport stats
  (packet loss, jitter, RTT, DTLS timeouts — the SCTP storm was diagnosed from
  these). Runs server-side, no USB — it partly covers the "device is blind once
  it leaves the bench" gap (§6 pre-deploy), for the transport layer at least.
- **Ingest (RTMP/WHIP) → push external audio into the device's room** — the
  plumbing for the music publisher and announcements (§6, §9).
- **Unlimited server-side participants at zero per-minute** → makes the whole §9
  control-plane platform economically real (agent + music publisher + recorder +
  classifiers, ×N rooms).
- **Full room/server API** (move participants, server-initiated dispatch, per-room
  config) — the building blocks of the control plane (§9).

Caveats: Egress/Ingest are **separate services to deploy** (a container each);
the SFU sees only the transport layer, not firmware internals (heap/mic levels
still need their own wireless path, §6 pre-deploy); uptime couples to the homelab.

---

## 3. Live backlog (prioritized)

> **Gate first:** before this backlog resumes, the **[living-room-ready
> milestone](MILESTONE.md)** has to close — the auth / OTA / observability /
> privacy exit criteria that stand between the bench and a deployed device. Items
> below whose ordering it revisits (self-host #2, telemetry #5, validation #1) are
> subsumed there; treat MILESTONE.md as the authoritative pre-deploy order.

> **Re-prioritized 2026-07-11 — pure-speaker focus.** Three decisions prune the
> list: (a) **MILESTONE A1/A2 will not be done incrementally** — the
> token-server + control-plane layer gets rebuilt as a single smart-speaker
> **provisioning server**, and auth/rate-limiting land there by design (see the
> MILESTONE annotation); (b) the **volume actuator is rejected**, and with it
> its dependents (`calibrate_audio`, whisper mode, volume-by-voice); (c) the §5
> **full-duplex self-interrupt finding is obsolete** — it does not reproduce in
> daily use, so no `PP_DTSENSITIVE` session is pending. Near-term order, all
> pure-speaker work:
>
> 1. **Real endpointing** (§5 #3) — replace "amplitude ≥ `SESSION_VOICE_LEVEL`,
>    12 s fixed" (`app.zig:104`, `core/session_core.zig`) with a real close
>    decision: proper VAD (speech vs noise, not level), adaptive window, and
>    the **agent** deciding end-of-conversation — it knows turns, open
>    questions, farewells; the model-driven `end_session` already exists —
>    signalled over the existing data channel. The firmware timeout demotes to
>    last-resort watchdog (net down / agent dead). Mostly agent-side Python.
>    *v1 SHIPPED 2026-07-13 (`feat/endpointing`, bench-validated: idle close
>    12.5 s with device-initiated disconnect <1 s, question window 25 s
>    (`expects_answer=True`), farewell via the same primitive, no premature
>    cuts on long user pauses, and the 60 s watchdog live-tested — agent held
>    open on purpose, device self-rescued at exactly quiet_ms=60000).* Known
>    edge, unfixed: an agent dying mid-"speaking" leaves the device's
>    `agent_speaking` flag stale (no "listening" ever arrives) → the keepalive
>    holds the session to the 10-min cap. Cheap guard if it ever bites:
>    invalidate the flag when the render has been silent for a few seconds
>    (the `render_peak` signal already exists).
> 2. **Wake word: split recall from precision** (§7 #1) — board threshold loose
>    (`wakeword.zig:40`, one constant → recall), agent re-verifies every fire
>    on the already-streamed pre-roll and aborts silently (precision). The old
>    caveat (per-trigger connects billed on Cloud) died with the Cortes
>    self-host — LAN connects are free. Every rejected fire is a labeled hard
>    negative, so the §7 retraining dataset builds itself. Do the
>    "para/stop"-during-TTS wake model (§4 extras) in the same pass.
> 3. **In-session speaker attribution — the TV-hijack finding (§5,
>    2026-07-13)**: with a session open, TV speech reads as user turns and the
>    adaptive beam tracks the TV. Layer 1 now (session beam lock at wake DoA —
>    small firmware, the pieces exist); layers 2/3 (DoA turn-gating,
>    per-turn speaker verification) after the wake word split, sharing its
>    plumbing. Full plan in §5.
> 4. **On-device timers via RPC** (#9) — agent schedules, firmware rings even
>    if the internet drops.
> 5. **Reliability hygiene batch** (one firmware PR, allowed under the freeze):
>    MILESTONE B3 reboot-on-boot-failure + boot-loop guard, B4 SNTP-before-TLS,
>    XVF mute state in telemetry (the §9.3 silent-speaker lesson), pre-roll
>    PSRAM health (#11).
> 6. **MILESTONE C4 "mark this moment" + C2 correlation ID** — daily-use
>    validation (#1) is already running on the prod stack; these make its
>    failures analyzable instead of timestamp archaeology.
> 7. **MILESTONE B1 OTA, firmware side** (dual partition, client,
>    rollback-on-failed-boot) — before any unattended week, and before the
>    freeze becomes a trap. Image **serving** is a first-class requirement of
>    the future provisioning server, not built here.
> 8. **MILESTONE B2 secure boot / flash encryption** — last, only after OTA
>    works (rollback is the safety net), validated on a sacrificial unit.
>
> Transversal: the determinism harness (#4) pays for itself before the chained
> firmware PRs. Explicit non-dos: endpoint mode to `main` (still blocked on the
> liveness ping/pong), barge-in over loud music (needs the music publisher —
> platform work, not speaker work).

Actionable, deduplicated. Deep multi-session efforts link to their design section.

1. **Validation in daily real-world use** — closures by `silence_timeout` (not
   `max_duration`); SCTP storm dies with clean closures
   (`sebastian_sctp_init_total`); phantom turns ≈ 0 with half-duplex. Just
   requires using the device and watching the dashboard.
2. **Self-host LiveKit on Cortes** — *elevated to #2 (2026-07-08): it is the
   critical path, not an economics option.* Every conclusion of the platform
   direction assumes always-connected server-side participants — the §6 feature
   menu (music publisher, recorder), the §7 two-stage verify (spurious connects
   per board trigger), §8 multi-room (N units 24/7), and the endpoint/conference/
   control-plane modes — and on LiveKit Cloud every one of those bills
   participant-minutes continuously. Design and the fiddly parts in §2.
   - [x] **Local dev edition SHIPPED + E2E-validated (2026-07-08):** SFU in the
     devcontainer compose (`livekit` service + `livekit.yaml`), signaling
     `ws://<mac>:7880`, media via **single-port UDP mux 7882** (the trick that
     makes WebRTC mappable through Docker/OrbStack — same trick to reuse on
     Talos), `--node-ip` = the Mac's LAN IP, keys in `.devcontainer/.env`
     (gitignored). Token server + agent repointed via `agent/.env` (cloud creds
     kept commented as fallback). **Device needed zero changes** (serverUrl
     comes from the token response, as designed). Full wake→talk→response loop
     validated on hardware. **The firmware accepts `ws://` (no TLS)** — first
     measured handshake heap: pre-connect 48.9 KB int / 41.4 KB DMA free,
     post-connect 41.0 / 33.5 KB (connect cost ≈ 7.8 KB, largest block steady
     at 31.7 KB) — comfortable margin; the §6 wss-starvation scenario has ~26 KB
     more headroom than the old failure point under ws://. Remaining: the Cortes
     GitOps deploy (Helm/ArgoCD/Infisical + the same UDP/node-ip plumbing).
3. **Wake word reliability / anti-phantom** — the full plan is §7. First step:
   let the board threshold go loose and add server-side re-verify; measure false
   positives with the "session without user turn" proxy to build a phantom
   dataset.
4. **Determinism harness** — host Zig tests for pure logic (decimator, softClip,
   pre-roll window, barge chopping — the class of bug that crashed 5× in one day,
   catchable in seconds without hardware); a Python device simulator (fake
   participant: pre-roll + audio + asserts on transcriptions/states); `make check`
   pre-flashing.
5. **Firmware telemetry batch** — *partial:* internal/DMA free-heap logging landed
   (`logHeap()` at boot + around connect, §6). Still pending: in-session audio
   levels, free heap trend, RSSI, temperature + Grafana alerts (reboot, SCTP,
   deaf channel, mute serial).
6. **AEC project → full-duplex with tracking** — *core SHIPPED 2026-07-08 (§5:
   path B integrated, live-validated end to end).* ~~Remaining: `PP_DTSENSITIVE`
   double-talk fine-tuning~~ — *obsolete 2026-07-11: the §5 self-interrupt
   finding did not persist in daily use; full-duplex works with the knob
   untouched.* `calibrate_audio` died with the volume actuator (#8, rejected).
7. **Control-plane `announce(text)` slice** (§9) — *HTTP face SHIPPED +
   ear-validated (2026-07-08):* `agent/control_plane.py` (`make control`, :8790)
   → finds the device's active room → data on `sebastian.announce` → agent
   speaks it. Zero firmware. Two field lessons baked in: (a) data packets are
   NOT queued for future participants — the control plane requires device AND
   agent present before sending; (b) **courtesy = sustained idle**: an announce
   fired the instant a session opens races the user's opening command (Gemini
   garbled both → "Anuncio en curso."), and one fired mid-generation is the
   orphaned-generate_reply collision — so the agent queues it and speaks only
   after ~3 s of continuous idle (validated live: queued mid-story, waited 18 s,
   spoke in the pause). Remaining: the MCP face, more modes (`record_note`,
   `play_media`, `set_mode`), and proactive announce to an *idle* device — which
   is exactly the always-connected enabler (§9). Also shipped alongside:
   **per-session mic recordings** (`agent/recordings/<ts>_<room>.wav`, on by
   default, `SEBASTIAN_RECORD=0` disables) — the local Egress for
   forensics/dataset.
8. ~~**Volume by voice**~~ — *rejected 2026-07-11*, together with the
   `audio_render` volume actuator and its dependents (`calibrate_audio`,
   whisper mode, ducking-by-volume). LED by agent state already received.
9. **On-device timers via RPC** — they ring even if the internet drops.
10. **Agent AGC** — re-framer to 10 ms before the APM, if recovered.
11. **Pre-roll PSRAM silently degrades** — reflect it in boot health.
12. **Document invocation states in `config.zig`** — to the detail level of the
    LEFT/RIGHT channel decision (the last open audit finding).
13. ~~**NS replacement for self-host**~~ — *RESOLVED 2026-07-08 by the path-B
    integration (#6): the comms channel carries the XVF's own NS + de-reverb
    on-chip; transcription validated clean without BVC.*

---

## 4. Reference: the attention engine (invocation design)

Invocation is not a trigger, it's a **layered attention system** (what Alexa+
solves with on-device fusion and Google with Continued Conversation / Gemini
Live). Sebastian can play in that league with audio alone because it exposes what
an Echo does not: **DoA and beam control of the XVF via I2C**.

### State machine (firmware, mirrored in the agent)

```
IDLE ──activity──► ARMED ──wake──► ATTENDING ──turn──► ENGAGED
                      ▲   (LED beam)     │                   │
                      │                   └─── response ─────┤
                      └──── timeout ◄── LINGER (follow-up) ◄─┘
```

- **ARMED** — room connected, `mic_src` publishes **silence** (reuse `setMuted()`
  as a state-gated gate); Opus DTX ≈ 0 network cost; wake word runs on-device, no
  voice leaves the device.
- **ATTENDING** — wake detected: gate open **with pre-roll**, LED toward speaker,
  **beam lock** in that direction (existing I2C command).
- **ENGAGED** — conversation with semantic turn detection and barge-in.
- **LINGER** — post-response listening **without wake word** with the DDSD gate;
  dim LED = "still listening". Parity with Alexa+'s blue bar.

### The invocation signals

1. **microWakeWord "Sebastián" on-device** — *shipped*. (V2 MixedNet models could
   cut the arena to ~23 KB if ever retrained; current model needs 36 KB, → §6.)
2. **Two-stage verification** — loose on-device recall + agent re-verify on the
   pre-roll. This is the spine of the anti-phantom plan → **§7**.
3. **Follow-up DDSD gate** (LINGER) — agent-side fusion of stable DoA / same
   speaker (acoustics), coherent STT partial (lexicon, cheap LLM <100 ms), and
   prosody/energy. Recipe: Apple arXiv 2411.00023; Gemini Live Proactive Audio is
   an evaluable shortcut.
4. **Button** — tap = wake without voice; long-press = push-to-talk.
5. **Proactivity with courtesy** (agent→device RPC) — chime + LED before speaking;
   if `mic_level` shows conversation, the announcement waits or degrades to LED.
6. **DoA as continuous telemetry** (lossy user packets ~5 Hz) — feeds the DDSD
   gate, the ring UI, and multi-device arbitration (best wake+SNR wins) → **§8**.
7. **Speaker ID on the agent** (pre-roll embedding) — per-person memory and
   policies; hardened threshold vs. unknown voices → also anti-phantom (**§7**).

### Extras with personality

- **"para"/"stop" wake word** active only during TTS/timer (multi-model
  microWakeWord, like HA Voice PE's internal `stop` model).
- **Whisper mode** — low-RMS utterance + nighttime → response at proportional
  volume (`esp_codec_dev_set_out_vol`).

### The agent, up to 2026 standards (mostly shipped)

- **livekit-agents 1.6.x** — *shipped* (turn detection via OpenAI semantic VAD;
  LiveKit's turn detector doesn't compose with `RealtimeModel`).
- **Decoupled model via config** — *shipped* (Gemini default, OpenAI fallback).
  2026 alternatives to measure by real $/min with `SessionUsageUpdatedEvent`:
  `gpt-realtime-2` (~$0.18–0.46/min), `gpt-realtime-mini`, Gemini native audio
  (~10× cheaper, Proactive Audio), Nova 2 Sonic (~$0.015/min, Spanish).
- **Tools via MCP** — HA *shipped*; device timers/alarms via RPC (backlog §3.9);
  Zetesis search (mcp-typesense) as documentary memory (→ §6 feature menu).
- **Memory per person** keyed by speaker ID (mem0/Zep or Payload+Typesense) —
  pending, pairs with signal #7.

---

## 5. Deep dive: Echo-level audio — the gap and the XVF plan

Staying strictly in the audio/mic pipeline (not skills/services), this is what
separates Sebastian from an Echo, and the plan to close it by **using the DSP the
XVF3800 already ships** (AEC, beamforming, non-linear residual suppressor, NS,
de-reverb, AGC, limiter) rather than building new DSP. Today we cap the **raw ASR
beam (RIGHT slot)** and throw away the post-processing of the **comms channel
(LEFT slot)**.

### The gap (ranked)

1. **Convergent AEC WITH tracking — the real big one.** An Echo hears you from
   anywhere with the beam tracking you *and* the echo canceled. Today Sebastian
   must choose: **fixed beam** (linear AEC converges, no tracking) or **adaptive
   beam** (tracks you, half-duplex). Reconciling them is the AEC project below.
2. **Barge-in over loud playback.** Tested over its own TTS, not over loud music;
   needs the AEC to hold a high-SNR far-end reference and the wake to fire on the
   residual.
3. **Decent endpointing / VAD.** ~~Today a level-based `silence_timeout` (crude,
   and what was cutting sessions).~~ *v1 shipped 2026-07-13
   (`feat/endpointing`): the agent closes idle sessions over the data channel
   (`agent/endpointing.py` — turns + agent/user state + question-aware window)
   and the device disconnects itself; the firmware level timeout is a 60 s
   last-resort watchdog.*
4. **Reliable wake word.** Threshold raised 0.62→0.80 with field data, but lacks
   systematic false-positive measurement and distance/noise validation → **§7**.
5. **AGC / distance normalization.** Fixed `mic_gain` + static SHIFT vs. Alexa's
   automatic level normalization.
6. **NS / dereverb in far-field.** The XVF has both (comms channel) but we block
   it by using the raw ASR channel — exploit it without ruining the ASR.
7. **Session/transport robustness.** `silence_timeout` disconnects and the
   fragile S3 native USB (flash and Web Serial re-enumerate/reset the board).
   The "never fails" axis. **The SCTP INIT storm (esp-webrtc-solution#186) is
   RESOLVED (2026-07-08)** via a 1-line patch in our `components/livekit`
   override (`peer.c`): the device's two PCs behave differently — the
   **publisher** peer's SCTP associates and carries ALL working data channels
   both ways (pre-roll/barge_in out, agent_state in); the **subscriber** peer's
   SCTP INIT is never answered and esp_peer retransmits with **no backoff** at
   ~9/s all session (232-398 INITs observed; identical against LiveKit Cloud
   and self-hosted v1.13.3 — device SDK bug, not server). Fix: only the
   publisher requests a data channel → 0 INITs, and **~7 KB of internal heap
   freed per session** (the storm's SCTP machinery). Negative result also
   documented in-code: `no_auto_reconnect=true` does NOT help (it gates
   whole-peer reconnect, not INIT retransmission). Peer↔role map was pinned by
   the inverse experiment (disabling the publisher DC instead kills pre-roll →
   no mic handoff → level=0 silence-timeout sessions).
8. **Acoustic design (hardware-ish).** Mic array + speaker in one enclosure; the
   non-stationary echo is partly physical.

The first three (#1 full-duplex+tracking, #3 endpointing, #4 robust wake) are the
core of "how a smart speaker manages audio"; #7 is what makes it usable daily.

### Field finding (2026-07-13) — the TV hijacks an open session (in-session speaker attribution)

With a session legitimately open and the full-duplex mic live, TV speech is
transcribable audio: the realtime model treats it as user turns and answers
it — and worse, the **adaptive beam tracks the loudest active source**, so when
the TV talks the array aims *at the TV*. This is NOT the §7 phantom-wake
problem (the wake was real); it is *who is the agent listening to* once the
session exists. No single simple fix — a three-layer plan, cheap to durable:

1. **Session beam lock at wake DoA (firmware, small — the pieces exist).** The
   wake already reads DoA (the LED points at the caller, §4 ATTENDING) and
   `fixed_beam`/`azimuth` are runtime XVF knobs: lock the beam toward the
   caller at handoff, release at teardown. Spatially attenuates the TV whenever
   it is off the user's axis. Limits: a colinear TV, a user who walks around
   mid-session. Allowed under the §6 freeze (controllability, not capability).
2. **DoA turn-gating in the agent.** Stream DoA at ~5 Hz over the lossy channel
   (§4 signal #6, unbuilt) and drop turns whose direction disagrees with the
   wake direction. Also builds the telemetry that §7 fusion and §8 arbitration
   reuse.
3. **Per-turn speaker verification (the durable fix).** Embedding (ECAPA-class)
   of each turn against enrolled voices, agent-side: unknown voice → turn
   ignored. Kills the TV even on-axis; prerequisite of per-person memory (§4
   signal #7). Real work: enrolment UX, threshold tuning, per-turn latency.

Freebie worth one line (unreliable alone): a system-instruction telling the
model to ignore clearly-unaddressed background speech (TV/radio).

### The AEC project — full-duplex WITH tracking

**Two capabilities separate us from an Echo:** (1) full-duplex with tracking, and
(2) **auto-calibration by voice** (*"Sebastián, calibrate"*) that persists to NVS,
solving **moving the device** and **plugging in a different speaker**.

**Path B — VALIDATED in hardware** (`probeDualChannel`). The trade-off was "fixed
beam OR tracking". Path B breaks it: the **non-linear** residual suppressor of the
comms channel cancels the echo **without a converged linear AEC and with the
adaptive beam**. Measured with noise at agent level:

| | residual echo (avg echo-rise) |
|---|---|
| Raw ASR beam (RIGHT), adaptive | **+101 655** (unusable) |
| Comms channel (LEFT), adaptive, unconverged AEC | **−2 568** (below ambient) |

The mechanism is proven — no longer a research risk.

**SHIPPED 2026-07-08** — integration validated live on the self-hosted stack:
1. `mic_channel = .left` + adaptive beam + `fullDuplexAllowed` path-B branch
   (comms channel needs neither a fixed beam nor AEC convergence — only the
   verified `FAR_END_DSP_ENABLE=1` far-end reference).
2. **Wake word works on the processed channel as-is** (fired at 88%, 3/3 field
   tests) — no channel split needed, the shared-slot invariant stands.
3. Transcription on comms is CLEAN without BVC (the "tinny/double-NS" concern
   died with BVC: comms is now the single NS pass by design) — this also
   resolved the self-host NS gap (old backlog #12).
4. No self-echo phantom turns; residual suppressed (live_echo 32765→2270 while
   render at 4.8M).
5. **Crisp barge-in required one extra piece:** the model generates faster than
   realtime, so seconds of reply sit in the device render FIFO — cancelling
   generation alone leaves the speaker narrating ("no para"). The agent now
   publishes `interrupted` on the agent_state topic when an assistant item
   arrives truncated, and the firmware calls `av_render_flush` (stream stays
   open). Also: `aec_warmup_duration=0.0` (the framework's 3s software-AEC
   warmup blocked interruptions; ours is hardware), and instructions separate
   "para/cállate" (stop + stay listening) from explicit farewell (end_session).

**Still open (fine-tuning):** double-talk knob `PP_DTSENSITIVE` if the
suppressor eats near-end speech in practice (first field tests: user speech got
through, transcribed clean mid-playback); comms AGC only if levels prove hot.

**Field finding (2026-07-08) — full-duplex self-interrupts, and it is NOT
acoustic.** Extended use surfaced "it won't finish a sentence": the agent starts
speaking and stops after a word or two. Confirmed from logs: **0 wake barge-ins**
(the wake word is not false-firing) — the interruptions are **transcribed user
turns arriving mid-speech**. With the mic always live (full-duplex), *any* audio
the STT transcribes during the agent's turn — genuine user speech, or residual
the AEC removed from the *audible* signal but not fully from the *transcribable*
one — trips the realtime model's turn detection → it stops. So "no audible
coupling" (verified) and "self-interrupts" are both true at once. Two levers:
(a) `PP_DTSENSITIVE` to cut the transcribable residual harder; (b) the realtime
model's own interruption sensitivity (it interrupts on any transcript, with no
min-duration/word gate exposed under `turn_detection="realtime_llm"`).
**Half-duplex sidesteps it entirely** (mic gated during speech → cannot
self-interrupt; intentional barge-in still works via the wake word) — the robust
fallback until the double-talk round is tuned. Full-duplex-on-self-host is
therefore *validated but not yet daily-driver-solid*.

> **Update 2026-07-11: this finding is obsolete.** The self-interrupts did not
> persist in daily use on the prod (Cortes) stack — full-duplex runs clean with
> `PP_DTSENSITIVE` untouched, and no tuning session is pending. The paragraphs
> above are kept for the diagnostic method (how to tell acoustic coupling from
> transcribable residual), not as an open issue.

**⚠️ The only real gate: double-talk.** The probe measured **echo** (agent
speaking, user quiet), **not double-talk** (both at once) — the Achilles heel of
non-linear suppressors: too aggressive and **it eats your voice when you talk over
it**. Knob: `PP_DTSENSITIVE` (17/31, currently 0). Only validatable with voice in
the room. Estimate: **~1 home session** for a first version, possibly a 2nd round
tuning `DTSENSITIVE`/`gamma_e`.

### The `calibrate_audio` tool ("Sebastián, calibrate")

Productize the probes as a voice tool. Prerequisite: the **volume actuator** (see
anchor-findings). Flow: agent tool → data-channel command (ack/retry for the SCTP
storm) → firmware routine (~10–15 s) sweeps output gain with noise, measures
pre-AEC echo, picks the highest level with −6 dB headroom, verifies `converged=1`,
saves `out_gain_db` to NVS; `applyConfig` re-applies it every boot with verified
write. Bonus: the firmware already detects in-session clipping (`live_echo=32767`)
→ it can suggest calibration or auto-correct.

### Anchor-findings (XVF reference — avoid repeating work)

- **`FAR_END_DSP_ENABLE` (35/25) is an AEC prerequisite.** At 0 the far-end
  detector sees no energy → AEC never adapts → speaker↔mic feedback. The XVF
  reverts to build default (0) on **power-cycle** (not `esp_restart`), so
  unplugging killed the AEC. **Fixed in `applyConfig`**, verified fail-closed
  (commit `a5b1534`).
- **`FAR_EXTGAIN` is NOT a master volume** (measured: −12 dB moved echo ~3%); it's
  AEC metadata. There is **no volume API** in the render chain (why sw-vol was
  always a no-op). **The real actuator** = a custom `audio_render` wrapper (~80
  lines) scaling PCM (Q15 gain) before the internal I2S render — coherent with the
  AEC reference, reduces THD, enables ducking + volume-by-voice + `calibrate`.
- **XVF config-drift:** reverts to build defaults on every power-cycle. Every
  adopted knob MUST be written in `applyConfig` (or `SAVE_CONFIGURATION` 48/9; we
  prefer explicit, git-visible writes).
- **Programmable slot routing** (`OP_L`/`OP_R`, 35/15,19): comms can be routed to
  any slot without touching `mic_src`.
- **Knobs to touch:** `PP_AGC*` (17/10-18, distance norm), `PP_DTSENSITIVE`
  (17/31, double-talk), `PP_GAMMA_E/ETAIL/ENL` (17/24-26, suppressor
  aggressiveness), `PP_MIN_NS` (17/21, NS floor), `SYS_DELAY` (35/26, ref↔mic
  alignment).
- **Gapless reference:** `auto_clear_after_cb` injects zeros on every underrun →
  gaps in the far-end reference that degrade adaptation; larger FIFO / no
  underruns = more stable AEC.

**Diagnostic tools** (in the tree, boot flags at `false`): `logPpConfig()`
(post-processor dump), `probeDualChannel()` (the path B experiment, ~12 s,
reversible), `probeOutputGain()` (the probe that discarded `FAR_EXTGAIN` as
volume). The AEC reference close: commit `2f611f8`, `docs/AEC.md`. Full phased
plan: `docs/implementation/10-aec-fullduplex-plan.md`.

**Agent provider note:** Gemini Live default (`SEBASTIAN_MODEL_PROVIDER=gemini`,
lower cost), OpenAI fallback. Native-audio models **reject explicit BCP-47 codes**
(`es-ES` → APIError 1007 → Live session closes → agent mutes with the device
session open); language is now `SEBASTIAN_GEMINI_LANGUAGE`, default auto-detect
(commit `55b869f`).

---

## 6. Deep dive: the device as a thin audio endpoint (server-side features)

Strategic pivot (2026-07-05): the S3 is at its internal-RAM edge, so the winning
shape is a **dumb, low-footprint endpoint** (4-mic array + speaker + LED ring +
on-device wake word + one WebRTC session) with **all brains server-side**. "Add a
feature" = server code at **zero firmware/RAM cost** — cheap and safe (no touching
the fragile stack).

**What the freeze does and doesn't mean (clarified 2026-07-08).** The pivot bans
*features* in firmware, not everything: (a) **reliability hygiene** is allowed and
encouraged (the +20 KB RAM reclaim below is exactly that — it bought headroom for
the fragile TLS path, not feature capacity); (b) **small one-time enablers of the
server model** are allowed (e.g. a server-owned "endpoint mode" where the session
is opened and held by the server instead of gated by the wake word — the enabler
for conference-mic / music / recording modes). The test for any firmware change:
*does it add a capability the server could host instead?* → server-side. *Does it
make the endpoint thinner, safer, or more controllable by the server?* → firmware
is fine.

### The RAM edge (why the pivot exists) — and the +20 KB reclaim

- **The memory-bug lesson (var/DCE).** Making the three `probe_*_on_boot` flags
  runtime `var` (for a "diagnostics" mode) defeated Zig's dead-code elimination,
  so `xvf_aec`'s ~15 KB of static probe buffers (`tone_buf` + `probe_rx_buf`,
  `[960*2]i32` each) stayed resident in internal RAM. Those 15 KB starved the TLS
  hardware-AES DMA (`esp-aes: Failed to allocate memory`) → the WSS handshake
  looped `Connection already in progress` forever and LiveKit never connected. It
  looked exactly like a network/IDF bug for hours; it wasn't. **Fix:** probes back
  to comptime `const false` (compiler elides them + the 15 KB). **Rule: never turn
  a config `const` into a runtime `var` if it gates a code path with large static
  buffers — `var` keeps them all.**
- **Internal RAM measured + reclaimed: +20 KB (2026-07-08).** Added `logHeap()`
  (free/largest block for `MALLOC_CAP_INTERNAL` and `…|DMA`) at boot and around
  `room_connect`, plus `arena_used_bytes()` in `mww_init`. Two hardware-validated
  reclaims: (1) **wake-word arena right-sized 48→40 KB (+8 KB)** — the model needs
  **36 268 B**, 40 KB keeps a 13 % margin; (2) **audio scratch buffers → PSRAM
  (+12 KB)** — `mic_src.read_buf` (8 KB) + `wakeword.i2s_buf` (3.8 KB) were static
  internal arrays, but `i2s_channel_read` memcpy's into them from the internal DMA
  descriptors so they need not be internal RAM; moved to PSRAM at init, **no
  hot-path cost** (`feed_max`/`gap_max` unchanged). Net at boot: internal free
  **57.5 → 77.8 KB**, DMA free **50.1 → 70.3 KB** — the pool that starved the TLS
  handshake. Largest contiguous block held at ~31.7 KB (bounded by a fixed
  allocation above the freed regions; individual TLS/AES DMA allocations are
  smaller, so more total DMA-free headroom still helps). **Remaining levers** (WiFi
  static buffers ~24 KB, IRAM opt ~15-20 KB) trade against the §5 #7
  network-reliability axis — **not worth it**; this is the sensible floor.
- **Shipped alongside:** mode-based web installer (PR #2) — `full_duplex`/
  `half_duplex` + `audio.fixedBeamAzimuthDeg` provisioned into NVS,
  `config.zig::load()` reads at boot; switching duplex is a re-provision, not a
  reflash (`full_duplex`/`fixed_beam`/`azimuth` runtime `var`; `mic_channel` +
  `session.*` stay compile-time).

### HA-native `media_player` without touching the firmware

Requirement: HA sees the device as a controllable `media_player` (music via Music
Assistant, announcements). Two routes:

- **DLNA/UPnP MediaRenderer in firmware** → HA's `dlna_dmr` auto-discovers it. Zero
  HA code, serverless — **but adds a decode+HTTP+SSDP stack to the RAM-tight
  device** as a mutually-exclusive mode. **Deferred**: only if we ever want native
  HA music with *no* always-on server.
- **Custom HA integration backed by WebRTC (chosen).** A `custom_component`
  exposing a `media_player` whose `play_media`/volume map to a **publisher** that
  joins the LiveKit room and streams to the device; Music Assistant feeds it,
  ducking/mixing server-side. → native HA entity + zero device firmware + one
  transport. Cost: real dev we own (component + robust publisher), depends on
  self-hosted LiveKit being up.

### Notifications / announcements / Grafana → voice

Reuse the pipeline: the agent already does TTS through the device. Add a "say
this" path (RPC/data-channel or a small HTTP API) → HA/Grafana call it → device
speaks. Grafana alert → webhook → *"CPU 95% on cortes"*. **No DLNA, no publisher
needed** — pure extension of the voice pipeline, highest value-per-effort.

### Feature menu (all server-side, device untouched)

| Axis | Features | Reuses |
|---|---|---|
| **Mic ML** (on the streamed audio) | sound-event detection (glass break, smoke alarm, crying, barking) → HA; speaker ID / presence; room transcription + summary; baby-monitor / remote-listen | Zetesis transcription stack; the WebRTC mic already published |
| **Homelab voice-ops** | query Grafana/Prometheus/K8s by voice; conversational incident announcements; "restart pod X" | Grafana + agent + MCP already wired |
| **Smarter assistant** | RAG over the Zetesis knowledge base; per-person memory; multilingual; personas/voices | mcp-typesense (already MCP-connected) |
| **Home** | scenes/conditional routines; DoA follow-me; camera/doorbell announcements (+ vision on the snapshot) | HA MCP already wired |
| **Media** | music (Music Assistant), TTS notifications from anything, timers/alarms/reminders, ambient sounds, multi-room/intercom | the media_player + WebRTC rooms |
| **LED ring** | expose as an HA `light`, visual notifications, DoA | existing ring UI |

### Suggested order

1. **Self-host LiveKit on Cortes** (§2) — foundation for everything else.
2. **Notifications / Grafana → agent speaks** — quick win, reuses the voice pipeline.
3. **Custom HA `media_player` + publisher** — music via Music Assistant.
4. **Sound-event detection + homelab voice-ops** — highest home value, reuse the
   audio stream + Grafana/K8s.
5. *(Optional)* measure whether `ws://` LAN signaling relieves the esp-aes margin.

### Caveats

- **Privacy:** streaming the mic to the server 24/7 has real implications — gate it
  (only after wake, or an explicit "listen mode").
- **WebRTC-on-K8s** UDP/IP exposure is the real time-sink of the foundation.
- **RAM edge stands:** on-device modes (assistant / DLNA-if-ever / intercom) remain
  **mutually exclusive**, not simultaneous. Vision needs a camera; multi-room needs
  more units.
- **Self-hosting loses BVC (discovered the hard way, 2026-07-08).** LiveKit's
  noise cancellation is a **Cloud-authenticated service**: on a self-hosted SFU
  the audio filter fails `not authenticated` and is silently disabled — and the
  device intentionally publishes the raw ASR beam with **no on-chip NS**,
  counting on BVC as the single NS pass, so the model hears raw far-field audio
  (mangled transcriptions, lost turns → "it's gone dumb"). Now explicit
  (`audio_input._bvc_if_available`, `SEBASTIAN_BVC` override). **Replacement
  needed for self-host:** (a) enable the XVF's own NS/post-processing (comms
  channel, §5 #6) or (b) an open-source NS (e.g. RNNoise) inside the agent's
  custom `AudioInput` — server-side, fits the thin-endpoint model.
  **Cascade discovered in the field:** full-duplex mode also leaned on BVC as
  the second defense against residual speaker echo — without it the agent
  heard its own voice as phantom turns ("se acopla"). **RESOLVED same day: the
  §5 path-B integration shipped** — the comms channel's on-chip residual
  suppressor + NS replace both of BVC's roles, self-hosted full-duplex works
  with talker tracking.

### Pre-deploy observability checklist (2026-07-08)

The current setup is a **bench** setup: everything on the Mac, device tethered by
USB. The plan "deploy in the living room in a few weeks, mark failure moments,
analyze later" needs these before it actually works — because the weird
per-session failures live in **device-side** signals that don't reach production
as-is. What's logged today: agent side (OTLP → Loki: per-turn transcripts, model
latency/tokens, tool calls, phantom flags, `sessions_ended{short=}`) survives
production; **device side** (heap, mic levels, echo, wake probs, SCTP, AEC state)
goes out the **serial port → `bridge.py` → OTLP**, which needs a USB-tethered
host — so in the living room it is **lost**. Gaps to close:

- [ ] **Wireless device telemetry** (the big one; backlog §3.5, Pending #4): push
      firmware vitals over the LiveKit data channel or a small UDP/MQTT path to
      Grafana, so heap/mic/echo/SCTP reach production without USB. The SFU
      webhooks/stats (§2) cover the *transport* layer for free but not firmware
      internals.
- [ ] **LGTM on Cortes, 24/7**, retention in weeks (today it's the devcontainer's
      default-retention image on the Mac — a Tuesday failure is gone by Thursday).
- [ ] **"Mark this moment"** — a button gesture or voice command that drops a
      correlated marker into the logs, so "analyze later" is a jump-to-marker, not
      timestamp archaeology from memory.
- [ ] **Correlation ID** (the session/room) logged on *both* device and agent so
      they can be joined — today they're aligned by wall clock only because both
      run on one machine.
- [ ] **Audio forensics** — Egress (§2) records each session; replay the failure
      moment to hear whether it was the user, residual echo, or noise (the exact
      ambiguity that cost hours on 2026-07-08).

---

## 7. Deep dive: anti-phantom wake word

A daily-use priority: the assistant must **not wake when nobody called it** (TV,
music, a near-homophone, its own voice). Consolidates the wake-word threads from
§4 (signals #2/#7) and §5 #4.

**The framing that dictates the approach:** false positives (phantoms) and misses
("didn't fire" — seen live at 81 % confidence) **pull in opposite directions on
the on-device threshold**. So the fix is **not** raising the board threshold —
that trades recall away and worsens misses. Split recall from precision:

- **On the board → recall.** Keep the threshold **loose** (never misses you).
- **On the server → precision.** Re-verify each trigger with real compute and
  **abort silently** if it wasn't real.

This is exactly the §6 thin-endpoint model: dumb board, brains server-side. Most of
what follows is **server code, zero firmware/RAM cost**.

### Options (highest leverage first)

| # | Option | What it does | Cost |
|---|---|---|---|
| 1 | **Two-stage verification** | board fires loose → agent re-checks the pre-roll (openWakeWord / heavier classifier / ASR "did they say *Sebastián*?") → abort session silently on fail. Recall **and** precision at once. | server, medium |
| 2 | **Retrain on the phantoms** | pre-rolls of false wakes are **hard negatives** → the model stops firing on TV / music / near-homophones. Durable. | training loop |
| 3 | **Speaker verification** | wake only for **enrolled voices** → kills TV/media/guest phantoms wholesale (pre-roll embedding, ECAPA). | server, embeddings |
| 4 | **DoA + energy fusion** | a real person arrives from a **stable direction**; diffuse TV/room noise doesn't → cheap discriminator on the already-streamed DoA. | server, cheap |
| 5 | **Context gating** | require verify **while music/agent is playing** (where phantoms cluster). Gate the wake on the AEC'd channel so **own-speaker echo** can't self-trigger. | medium |
| 6 | **On-device threshold/window** | quick knob, but **sacrifices recall**. Last resort, not the primary lever. | trivial |

### Caveats

- **Two-stage verify still opens a brief session per board trigger** to abort
  before speaking. It kills the **audible** phantom, not the connect. With
  **self-hosted LiveKit on LAN** (§2) that connect is nearly free; on Cloud it
  bills participant-seconds — a reason to pair this with the self-host foundation.
- **Own-speaker echo** (music/TTS re-triggering the wake) is a distinct source —
  attack it with #5 (gate during playback / wake on the AEC'd channel), not the
  server re-verify.
- All of this needs a **false-positive dataset** — the "session without a user
  turn" proxy (backlog §3.3) measures it before re-training.

### Stop-order as kill switch + phantom signal (2026-07-08)

A stop/farewell now **ends the session** (re-wake with "Sebastián" required),
not just pauses it — decided in the field. Two payoffs: (1) a **reliable kill
switch** for a phantom that started babbling — say "para" and it closes; (2) a
**phantom signal in the logs** — a phantom shows up as a very short session the
user shut down. Two design corrections learned here:
- **Model-driven, not a keyword list.** A hardcoded stop-word list (`para`,
  `cállate`…) breaks on Spanish **polysemy** ("enciende la luz *para* el salón")
  and on **multilingual** (the agent auto-detects language). The LLM understands
  intent in any language — let it call `end_session`. Deterministic keyword
  matching was tried and reverted.
- **Phantom detection lives at the session level, language-agnostic:**
  `end_session` logs the duration and tags `[likely-phantom]` under
  ~12 s (`sebastian_agent_sessions_ended_total{short=…}`) — no keyword, no
  language dependency. This is the same signal §8 arbitration and the §3.3 proxy
  feed on. Reliability backstops: wake-word barge-in + hardware mute.

### Suggested order

1. **Two-stage server re-verify** (#1) — kills audible phantoms now, lets the board
   threshold go loose → also fixes the "didn't fire at 81 %" miss.
2. **Measure** false positives with the session-without-turn proxy → phantom dataset.
3. **Retrain** (#2) on it; add **DoA fusion** (#4) as a cheap gate.
4. **Speaker-ID** (#3) if TV/guests turn out to be the dominant source.

---

## 8. Deep dive: multi-room / multi-unit

N distributed devices. Question that triggered it: *"could we have several
microphones in a mesh instead of just one?"*

**The shape: N identical thin endpoints, zero mesh.** The §6 pivot is what makes
this scale. Each unit is an independent WebRTC client joining LiveKit; rooms
natively take N publishers. **Adding a microphone = flashing another unit and
pointing it at the same token server — zero firmware changes.** All coordination
lives server-side.

**Explicitly rejected: ESP-WIFI-MESH / ESP-NOW between units** (one unit relaying
audio for the others):

- The S3 is at its internal-RAM edge (§6); a mesh stack + Opus aggregation on a
  gateway unit is the opposite of the thin-endpoint rule.
- Conversational audio needs sustained bandwidth and low jitter; every mesh hop
  adds both, on a device that already handles erratic networks poorly (§5 #7).
- It solves nothing: in a home with Wi-Fi coverage every unit reaches the AP
  directly. If coverage is the problem, fix it at the **network layer** (mesh
  APs/routers) — transparent to the firmware.

**The one genuinely new piece: wake-word arbitration** (Amazon calls it ESP, Echo
Spatial Perception). If two units hear "Sebastián" at once:

1. Each reports a wake event with a **score** (wake confidence + beam energy/SNR;
   the XVF already exposes DoA and level, §4 signal #6).
2. The server collects events in a **~200 ms window**, picks the winner, the rest
   stand down silently — **the same silent-abort pipeline as the §7 two-stage
   verify** (arbitration score and phantom-verify score are one mechanism).
3. 100 % server-side — zero firmware/RAM cost.

**Room topology (decide when building):** *room per unit/zone* (isolation, simplest
arbitration) vs. *shared room* (intercom + follow-me for free, but arbitration must
handle units that don't hear each other). Likely hybrid: room per zone +
agent-orchestrated bridging (the §6 publisher covers the announce path).

**Prerequisites and order:** (1) self-hosted LiveKit (§2) — N always-connected
units on Cloud multiply participant-minutes, on LAN cost zero; (2) §7 two-stage
verify shipped first — arbitration piggybacks on it; (3) a second hardware unit —
arbitration is untestable with one device.

**Caveats:** hardware cost (~ReSpeaker + XIAO per room) is the real scaling limit,
not software; arbitration tuning (window, score weighting) needs real two-unit
testing in adjacent rooms; multi-unit multiplies the §6 privacy caveat.

---

## 9. Control plane + device-control MCP (execution layer of §6)

The keystone that turns "a pile of server-side features" into one coherent,
voice-drivable system. **The unifying idea: the device is a LiveKit room; every
feature is a server-side participant in it.** Play music / notifications / TTS →
something *publishes* audio into the room. Voice notes / transcription / sound
events → something *consumes* the mic track that's already published. You don't
build N integrations; you build **one control plane** and each feature is a
producer or a consumer on the same room.

### The shape — one control plane, three front-ends

```
  voice → agent → MCP ─┐
  web UI ────────────►├─► CONTROL PLANE (mode state machine) ─► LiveKit room ─► device
  Grafana/HA → HTTP ──┘        (publishers / recorders / classifiers)
```

A control plane that owns the device's **mode state** and exposes two faces of
the same logic:
- an **MCP server** → the agent uses it as tools ("Sebastián, pon música" → tool
  `play_music`), exactly as it already uses the Home Assistant MCP;
- an **HTTP/web API** → the web UI, Grafana and HA drive it directly.

Both faces call the **same state service** — the MCP tools and the web endpoints
must not duplicate logic. Single source of truth for "what mode is the device in".
Precise note: LiveKit (the SFU) does not call MCP — the **agent** (the
livekit-agents worker) is the MCP client.

Vertical slice that proves the whole thing: build the control plane with **one
mode first — `announce(text)`** — exposed as MCP *and* HTTP. That exercises all
three front-ends (voice, web, Grafana webhook) with **zero firmware**. Every
later mode (`record_note`, `play_music`, `set_mode`, conference) is additive on
the same skeleton.

### The three real design decisions

1. **Output arbitration (one speaker).** Modes are partly mutually exclusive: you
   can't play music AND have the assistant talk without **ducking/mixing**. The
   SFU can mix, but the control plane decides — Grafana alert → duck music →
   announce → restore. That ducking is a real control-plane responsibility, not
   free.
2. **The agent is both a client and a managed resource.** It calls the control
   plane (via MCP) *and* the control plane manages its room presence (in
   conference/music mode you may not want it answering). Resolve by separating
   *the LLM brain* (MCP client) from *the worker's room membership* (managed
   resource); conflate them and you get an ugly circularity.
3. **Mode has a physical indicator.** The device already has the RPC/data channel
   for LED + agent state — the control plane drives the ring to reflect the mode
   (recording = red, music, listening). Reuses existing plumbing; the mode stops
   being invisible.

### Where it lives, and the conference/endpoint mode

Fits the **ZetesisPortal** MCP ecosystem (mcp-typesense, agent-runtime, Payload
already there) — the device-control MCP is one more server in that monorepo. It
also subsumes **"use it as a remote mic/speaker"** (conference, baby-monitor,
remote-listen): a browser joining the room via LiveKit Meet already *is* remote
mic access; what's missing is a firmware **"endpoint mode"** where the server
opens and holds the session instead of the wake-word gating it (the §6
freeze-exception enabler), plus a token minted without the agent dispatch.

### Order

1. **Self-host LiveKit** (§2) + the token server already does explicit dispatch. ✓
2. **`announce(text)` slice** — HTTP face SHIPPED + validated (backlog #7); MCP
   face pending.
3. **Always-connected / endpoint mode — SHIPPED v1, ear-validated (2026-07-08).**
   Firmware: `alwaysConnected` provisioned mode — connects at boot, mic gated at
   idle (readFrame's pre-handoff silence path, indefinitely), wake = open the
   gate (green ring instantly, no connect phase), `sebastian.session` wake/sleep
   signals, transport kept across conversations, reconnect on room death + a
   15 s idle health check (CONNECTED + agent present). Agent
   (`SEBASTIAN_ENDPOINT=1`): stays in the room, **lazy LLM lifecycle** — session
   opens on the wake signal, closes on sleep; idle announces run a brief
   TTS-only session. **Proactive announce to a sleeping device works** (heard:
   "puedo avisarte sin que me despiertes", via curl, device untouched).
   **Field-earned caveats / follow-ups:**
   - **Announces speak via TTS (`session.say`), never the realtime model**: a
     cold Gemini Live session returns empty/garbled first generations (~6 cases
     measured: 0.007s/0-token replies, "Anuncio en curso.", "Let me check") —
     TTS is deterministic and verbatim.
   - **Cold-start toll on the first turn of every conversation** (lazy = fresh
     Gemini per wake; first generation often empty → the nudge or the user's
     repeat unblocks at ~6-12 s). TODO: shorten the endpoint nudge (~2.5 s),
     and/or a keep-warm window (hold the LLM session ~2 min after each
     conversation), and/or test OpenAI Realtime for cold-start behavior.
   - **The client SDK lies about room state** after a server-side room delete
     (stuck CONNECTED/RECONNECTING forever — #186 family): state-based health
     checks are insufficient. TODO #1: **app-level liveness ping/pong** over the
     data channel (device pings at idle; endpoint agent pongs; no pong 30 s →
     recycle).
   - **XVF state survives esp_restart**: a wedged audio path (mute speaker) only
     clears on POWER-CYCLE — serial resets are not enough; also make the mute
     state visible in telemetry/agent logs (half an hour was lost to a silent
     speaker).
4. **`record_note`** (consume the mic → store → Zetesis transcription).
5. **Music publisher** (Ingest / publisher participant).
6. **The web UI** ties the modes together.

**Privacy is day-one, not a later patch:** always-on mic → server needs an
explicit listen-mode/mute model, gated (only after wake, or an explicit mode).

---

## 10. Risks & honest caveats

- **microWakeWord + WebRTC without public precedent** — was validated with the P0
  spike before the rest. WakeNet (ESP-SR) is not a Plan B: its self-serve pipeline
  doesn't support custom Spanish.
- **client-sdk-esp32 in Developer Preview** — APIs subject to change; pin the
  version and review changelogs before each bump.
- **Market data** (versions, prices, Alexa+/Gemini features) are a 2026-07
  snapshot; re-verify before cost decisions.
- **The RAM edge is real** — server-side features (§6) exist precisely because the
  device can't take more; on-device modes stay mutually exclusive.
