# Implementation Report — Sebastian

Normative synthesis of the multi-agent technical exploration from **2026-07-02**
(8 areas investigated in parallel against the real repo code, the
vendored components and the official docs/tags from July 2026, plus a
cross-check that detected 10 contradictions and 10 gaps). Develops the
[ROADMAP.md](ROADMAP.md) to an executable level.

**How to read this:** this document resolves conflicts and sets the
decisions; the full detail of each area (designs ≤120 lines, snippets
ready to paste, risks, sources) lives in the annexes of
[`docs/implementation/`](docs/implementation/). Where an annex contradicts the
Frozen Decisions (§2), this report prevails.

---

## 1. Global Verdict

The 8 areas are **viable** (none blocked). Estimated total effort:
**~25–35 person-days**, with two cheap validators (10 min and 2 days) that
condition everything else.

| # | Area | Verdict | Effort | Annex |
|---|------|-----------|----------|-------|
| 1 | microWakeWord + LiveKit in S3 (spike) | viable with risks — memory fits comfortably (all PSRAM-able); the only unknown is the CPU p95 coexisting with WiFi/DTLS bursts | M (spike 2 d, +3–4 d integration) | [01](docs/implementation/01-fw-wakeword-spike.md) |
| 2 | Gate + pre-roll + device↔agent protocol | viable with risks — all the API exists and is **verified in the vendored code**; pre-roll per track inviable → goes via data stream | M (6–8 d) | [02](docs/implementation/02-fw-gate-preroll-protocol.md) |
| 3 | Wake word training "Sebastián" es | viable — the repo's trainer already covers the full pipeline; missing 2 datasets and the adversarial negatives | M (3–5 d) | [03](docs/implementation/03-wakeword-training.md) |
| 4 | Agent in livekit-agents 1.6.4 | viable — all APIs verified against the 1.6.4 tag; adaptive interruption/preemptive DO NOT apply to S2S (honesty) | M (4–5 d) | [04](docs/implementation/04-agent-modernization.md) |
| 5 | XVF3800 AEC Reference | viable with risks — architecture confirmed with XMOS quote; the decisive test takes **10 minutes** | M (2–3 d) | [05](docs/implementation/05-aec-verification.md) |
| 6 | Voice model matrix + costs | viable — Gemini/Nova S2S are the price floor (~$10–38/month); the pipeline is NOT the cheap path (TTS dominates) | S (1.5–2 d) | [06](docs/implementation/06-model-cost-matrix.md) |
| 7 | DDSD Gate (LINGER) + speaker ID | viable with risks — exact primitives in 1.6.4 (`turn_detection="manual"` + `commit/clear_user_turn`); +0.5–0.9 s latency on follow-ups | L (13–15 d + 2 passive weeks) | [07](docs/implementation/07-ddsd-speaker-id.md) |
| 8 | Infra: self-host LiveKit + deploy in cortes | viable with risks — fits with existing GitOps; BVC is Cloud-only and hostNetwork on single node | M (3.5–5 d) | [08](docs/implementation/08-infra-selfhost.md) |
| — | Cross-check (contradictions/gaps/order) | — | — | [09](docs/implementation/09-cross-check.md) |

---

## 2. Frozen Decisions

They resolve the contradictions of the cross-check. They are **normative** for the
implementation; changing them requires updating this document.

**D1 — Channel device→agent = `publish_data` (user packets) with retries; agent→device = RPC.**
The C SDK 0.3.10 **does not implement outgoing RPC** from the device
(`livekit_room_rpc_invoke` only exists in a comment of `livekit.h:210`;
`core/rpc_manager.c` only brings register/handle). Verified in implementation,
not in header. The "ack" of the `wake` event is the RPC `sebastian.state` that
the agent returns. ⚠️ The snippet `protocol.py` of annex 04 registers `wake`
as an incoming RPC — correct to `room.on("data_received")` over the topic
`sebastian.evt`. If a future 0.3.x adds rpc_invoke, migrate `wake` to RPC.

**D2 — Sample rate: detector at 48 kHz direct; pre-roll at 16 kHz.**
The microWakeWord frontend is frequency-normalized and ESPHome feeds it
at 48 kHz **on this same board** (`respeaker.yaml`) → the detector's tap goes to
48 kHz without decimating (zero risk of mismatch with training). The pre-roll
ring does get decimated ×3 to 16 kHz (64 KB PSRAM for 2 s; mean-of-3 as
placeholder, esp-dsp FIR later): it is the format consumed by openWakeWord,
ECAPA and the STT in the agent. Corrects the ROADMAP line that assumed decimating
for the detector.

**D3 — Gate vs hard-mute: two flags, a single refactor.**
`muted_flag` from `mic_src` is separated into `gate` (OR'ed reasons: `state`
ARMED, `half_duplex`) and `hard_mute` (physical button: also cuts the tap of the
wake word and the ring — true privacy). `xvf_ui.zig` no longer owns the mute
(today it forces `setMuted` every 80 ms and would step on the state machine). Two areas
arrived separately at the same correction → do it **only once, before**
the spike and the gate. Corrects the ROADMAP ("reuse setMuted() as gate").

**D4 — Pre-roll via data stream, never injected into the track.**
Injecting old audio into the track is inviable: the pipeline rewrites the pts by
counter (`gmf_audio_src.c:118`), the src→encoder queue is 3×10 ms and the
agent's jitter buffer would flush the backlog. Instead: snapshot of the ring
→ data stream `sebastian.preroll` (header `SBPR` + s16le@16k, ~64 KB chunked,
<200 ms on WiFi) → the agent re-verifies the wake word on that PCM and prepends
the frames with a chained `PrerollInput(io.AudioInput)` to
`session.input.audio`. It is the same pattern as LiveKit's official
"pre-connect audio buffer" (its native topic requires attributes that the C SDK cannot
send → custom topic).

**D5 — Gate mechanism LINGER = `turn_detection="manual"` + `commit_user_turn()`/`clear_user_turn()`** (annex 07), not `StopResponse` (annex 04).
A single code path for ENGAGED and LINGER, verified against
`agent_session.py` L1304/1311 of tag 1.6.4. Requires `RealtimeModel(...,
turn_detection=None, input_audio_transcription=None)` + parallel STT (if the
Realtime transcribes itself, the parallel STT is ignored —
`agent_activity.py:1958`). `StopResponse` in `on_user_turn_completed` remains
as plan B if the 1-day spike of manual mode encounters corners. In P0
(without gate): server `semantic_vad` and LINGER = simple time window.

**D6 — Model by factory from day 1; P0 `gpt-realtime-mini`, candidate P1 Gemini 2.5 native audio.**
Change `gpt-realtime` → `gpt-realtime-mini` now (1 line, −70% cost). The
factory (`agent/models.py`, annex 06) makes the model an env var. **Avoid
`gpt-realtime-2`** until the plugin's issues are closed (#5768 message
loss, #5808 silent truncation). The DDSD gate is implemented **agnostic
to the model**; `proactivity=True` of Gemini is a 1-week experiment and a
weak labels generator, never a dependency (only exists in native-audio
2.5 preview, not in 3.1). Instrument `session_usage_updated` from the first
day and decide with real $/min.

**D7 — Token server + explicit dispatch are brought forward to P0** (resolves the
phase misalignment between annexes). Without it there is no `RoomAgentDispatch`, the
fresh-room+reset workaround doesn't die, and the IDLE phase that makes the free tier viable
is not possible. In P0–P1 it runs wherever the agent runs (Mac/LAN, FastAPI
of ~40 lines); to cortes in P2. The firmware does `GET /token` on boot
(TTL 24 h, secret per device, token cached in NVS as fallback) —
LiveKit does not kick the participant when the JWT expires live.

**D8 — The "silence ≈ 0 cost" of the ROADMAP is false; the economics are decided with IDLE.**
Opus DTX only exists at 8/12/16 kHz (we publish at 48 kHz) and the SDK does not
expose it: ARMED costs a few kbps of network **and minutes of Cloud participant**
(86,400/month connected 24/7 vs 5,000 of the free tier). Decision: implement
**IDLE (room disconnection after N min in ARMED + reconnection on wake, ~1–2 s)**
at the end of P0, after the token server. With real use (~180 min/month) the free
tier is plenty. Ship ($50/month) remains as a cushion; self-host eliminates it in P2.

**D9 — BVC/AEC Order: close the AEC finding BEFORE any A/B without BVC.**
The ASR beam (mux 7,3) **does not** have residual echo suppression — it is not "the already
clean signal" that the self-host plan assumed. `noise_cancellation.BVC()` is
Cloud-only and fails hard against self-hosted server → gate by env var
(`LIVEKIT_CLOUD=0`) right away; the beam-only vs
`livekit-plugins-dtln` comparison is done after closing AEC (Phase D).

**D10 — Turn detector parameterized v1/v1-mini via config from day 1.**
`inference.TurnDetector` v1 (audio-native, es) runs only on LiveKit Inference
(Cloud); in self-host there is only local v1-mini (CPU). If the P1 gate is
coupled to v1 without a selector, the migration to cortes breaks it.

**D11 — Detector arena sized by manifest, not hardcoded.**
The spike uses 26,080 B (okay_nabu); the custom trainer outputs
`tensor_arena_size=30000`. The firmware component reads the manifest's JSON and
probes 1×/1.5×/2× like `streaming_model.cpp`.

---

## 3. device↔agent Protocol Contract (normative)

Freezes the assumptions of annexes 02, 04 and 07. Versioning: `ver` field in
all JSON payloads; agent identity = convention `agent_name=
"sebastian"` **and** verification `kind==AGENT` + state ACTIVE in
`on_participant_info`; the device validates `sender_identity` in RPC/data.

| Message | Dir | Channel | Payload |
|---|---|---|---|
| `wake` | dev→ag | `publish_data` **reliable**, topic `sebastian.evt` (custom retry+backoff: the SDK doesn't buffer if engine≠CONNECTED) | `{ver,type:"wake",wake_id,score,doa_deg,speaker_hint:null}` |
| `button` | dev→ag | ditto | `{type:"button",kind:"tap"\|"long"\|"double"}` |
| mirror `state` | dev→ag | ditto | `{type:"state",state:"armed"}` |
| DoA telemetry | dev→ag | `publish_data` **lossy**, topic `sebastian.doa`, ~5 Hz | binary `<f32 azimuth_rad, u32 mic_level>` (+u8 state) |
| pre-roll | dev→ag | **data stream** bytes, topic `sebastian.preroll` | header 12 B `SBPR` (ver u8, wake_id u32, sr u16=16000) + s16le |
| `state` | ag→dev | **RPC** `sebastian.state` (synchronous ack) | `{state:"listening"\|"thinking"\|"speaking"\|"linger"\|"idle", ttl_ms?}` |
| `led` / `volume` | ag→dev | RPC `sebastian.led` / `sebastian.volume` | `{pattern,r,g,b}` / `{level}` |
| `announce` | ag→dev | RPC `sebastian.announce` | req `{chime:true}` → resp `{busy:bool}` (courtesy: `mic_level` gives away conversation) |

Hard rules of the firmware: the RPC handlers run in the `esp_peer` task
with the invocation on stack → **parse, enqueue, `send_result` before
returning**, never block; payloads ≤1 KB. Incoming JSON with
`std.json.parseFromSliceLeaky` + `FixedBufferAllocator`; outgoing with
`std.fmt.bufPrint` (comptime templates — the Stringify of the 0.16 fork is
unstable post-writergate).

### State Machine (distributed source of truth, defined reconciliation)

```
        Local WW / button-tap (device)         turn accepted (agent)
ARMED ─────────────────────► ATTENDING ─────────────────────► ENGAGED
  ▲  closed gate             │ OPEN gate already (optimistic)   │ speaking ⇒ half_duplex
  │  (publishes silence)     │ + wake evt + pre-roll stream     ▼
  ├── watchdog 8 s ◄─────────┘ agent veto ("idle")            LINGER (ttl 8 s, open gate,
  └────── ttl expires (device) / "idle" (agent) ◄──────────────┘        DDSD gate on agent)
```

- The **device** owns: optimistic opening on wake (no RTT — the pre-roll
  covers the gap), 8 s watchdog without agent response, ttl expiration,
  button. The **agent** owns: veto after re-verifying the wake word,
  transitions within ENGAGED/LINGER, LINGER ttl.
- **Reconciliation**: if the agent disappears (participant disconnect) or the
  room drops with the gate open → the device closes the gate and returns to ARMED.
  Upon reconnecting, the device publishes its mirror `state` and the agent responds with
  its own. IDLE (disconnect room) enters at the end of P0 (D8); until then
  IDLE≡ARMED.
- False positive of the wake = bounded leak of ~200–500 ms of audio until the veto
  (optimistic opening). Mitigation: LED on whenever the gate is
  open (visible honesty) and optional mode "opening only after ack".

---

## 4. Integrated Plan (cross-check order)

**Phase A — cheap validators, in parallel (week 1)**
1. **AEC Test 0** (10 min, annex 05): mux `AUDIO_MGR_OP_R ← (5,0)` (far-end
   passthrough) + play TTS + record with `record.py`. TTS is heard →
   the AEC reference reaches the XVF electrically. Decides if there is a path to
   full-duplex/barge-in; a NO repositions half the plan (permanent half-duplex).
   Afterwards, reading of `AEC_AECCONVERGED` (33,3) and A/B tests from the playbook.
2. **microWakeWord Spike** (2 d, annex 01): component `firmware/components/
   wakeword/` with exact ESPHome pins (`esp-tflite-micro 1.3.3~1`,
   `esp-nn 1.1.2`, `esp-micro-speech-features ^1.2.3`), `extern "C"` shim
   (`mww_init/feed/reset`), tested `okay_nabu` model, tap at 48 kHz.
   GO: p95(frontend+Invoke) <10 ms/hop, internal heap >40 KB stable, 0
   artifacts in 10 min of conversation, ≥8/10 detections at 3 m. Includes the
   **measurement of stationary heap** (gap #5). Stepped Plan B:
   `feature_step_size` 20 ms → core 1 affinity → temporary PTT.
3. **Training "Sebastián"** starts in parallel (annex 03): audit
   `personal_samples/` (the 42 `mix_*` go in as
   positives ×3.0 — listen to them), `prepare_datasets.py` (wham_16k is empty,
   chime_16k doesn't exist), generate the ~5,000 adversarial negatives
   (`sebastiana`, `san sebastián`, `bastión`, `se bastan`, `es bastante`,
   `la estación`…), and launch
   `MWW_LANGUAGE=es MWW_CALIBRATION_TARGET_FAPH=0.5 ./train_microwakeword_macos.sh "sebastián" 50000 100 --language es`
   (~8–14 h M-series). Do not train a separate model for "oye sebastián": the
   streaming model triggers with the suffix. Acceptance: recall ≥95% @1 m /
   ≥90% @3 m / ≥80% @3 m with TV; FAPH ≤0.5 over ≥10 h of Spanish TV/podcast.
4. **Agent step 0** (annex 04): create custom LiveKit Cloud project
   with API key/secret (gap #1 — current token is from a sandbox that **expires
   ~Aug-2026**) + upgrade to `livekit-agents ~=1.6.4` + `AgentServer` +
   `@server.rtc_session(agent_name="sebastian")`.

**Phase B — freeze contracts (days 3–5, after spike's GO)**
§2 and §3 of this document are that freeze: D1 (publish_data), D2
(48 k/16 k), D5 (manual gate) and the single refactor D3, which is implemented here,
before the two firmware areas touch `mic_src`.

**Phase C — P0 implementation (weeks 2–3)**
`invocation.zig` + gate + pre-roll + incoming RPCs (annex 02, ready
snippets) against the already modernized agent, using **button-tap as synthetic
wake** (validates gate, pre-roll and protocol before integrating the model);
in parallel the **models factory** (annex 06 — before DDSD because
it fixes the model); token server + explicit dispatch (D7); at the end: integrate
`sebastin.tflite` (spike + training converge) and E2E with real wake; IDLE (D8).

**Phase D — P1 barge-in (weeks 4–5)**
AEC closure according to annex 05 tree — expected case: OK reference + non-linear
residual → **dynamic switch of the mux via I2C** (`OP_R ← (6,3)` comms with
residual suppression only while the agent speaks, `← (7,3)` when finishing; 5
I2C bytes without reflashing) + reconstruct the real volume (the current `set_out_vol(35)`
falls on a noop codec — the effective control must be wired to the
AIC3104) + gradual increases monitoring `AECCONVERGED`. Upon closing:
deactivate `half_duplex`. Week of **Gemini Proactive Audio** experiment
as UX calibrator and weak labels generator.

**Phase E — DDSD + speaker ID (P1/P2, ~13–15 d + 2 passive weeks)**
Annex 07: `readAzimuthRad()` (the current `readBeamLed` quantizes to 30°; the
gate criteria is ±20°) + telemetry 5 Hz → fusion gate (acoustics
ΔDoA/duration/speaker + LLM judge `gemini-2.5-flash-lite` or `claude-haiku-4.5`
— gpt-4.1-mini discarded for TTFT ~4× — with the exact prompt from the annex) →
`commit/clear_user_turn`. **Shadow mode** 1–2 weeks (log-only) → 50–100
labeled examples → calibrate. Local Speaker ID with ECAPA-TDNN (192-dim,
cosine ≥0.45 / grey zone 0.30) enrolled **through the same XVF channel via LiveKit**.
Metrics: false triggers <1/day, follow-up loss <10%. Assumed latency
budget: follow-up replies in ~1.1–1.7 s (vs ~0.8–1.2 s with wake).

**Phase F — self-host infra (P2, 3.5–5 d)**
Annex 08: `livekit/livekit-server` chart in cortes (hostNetwork — Talos NodePort
doesn't cover 50000+; no Redis or TURN in single LAN replica; UDP range
50000–50100), direct `ws://10.0.0.151:7880` (the media goes DTLS-SRTP anyway;
wss with cert-manager as an improvement — the C SDK already attaches the CA bundle), token
service and agent as Deployment in `manifests/sebastian/` of Mileto with
dedicated ArgoCD Application (langfuse/herschel pattern: the ZP monorepo is
unaware) + ExternalSecret from Infisical path `/sebastian`. Before: decide
registry (ghcr.io vs Harbor of px-socrates) and CI of the Sebastian repo, and
check Talos ingress firewall (open UDP 50000–50100 + TCP 7880/7881
if there is NetworkRuleConfig). A/B beam-only vs `livekit-plugins-dtln` **after**
AEC closure (D9).

### Economics (jul-2026 snapshot, recalculate with `session_usage_updated`)

| Concept | Figure |
|---|---|
| Conversation (moderate use, 15/day): gpt-realtime-2 / mini / Gemini / Nova / pipeline | $41 / $12.8 / $9.6 / $9.6 / $15–26 per month |
| LINGER without DDSD gate (moderate) | +$0.16–3.46/month depending on model — **the gate is justified by UX/privacy, not cost** (except with realtime-2) |
| LiveKit Cloud 24/7 connected | 86,400 min/month ≫ free tier 5,000 → IDLE (D8) or Ship $50/month or self-host $0 |

---

## 5. Detected Gaps → assigned owner

| Gap (cross-check) | Resolution |
|---|---|
| Custom LiveKit Cloud project with API keys (sandbox expires ~Aug-2026) | **Phase A.4** — first task of the agent area |
| Single protocol specification | **§3 of this document** (normative) |
| State machine with no owner/reconciliation | **§3** — owners and reconciliation defined |
| Wake word re-verification engine in the agent | openWakeWord on the pre-roll's 16k PCM as first option; measure the veto budget (200–500 ms) in Phase C; STT+textual match as fallback without new dependency |
| Consolidated memory budget for the firmware | The spike (Phase A.2) measures the stationary heap; the GO includes margin for the pre-roll ring (64 KB PSRAM) and the StreamBuffer |
| Single refactor `mic_src`/`xvf_ui` (gate vs hard_mute) | **D3**, executed in Phase B before both firmware areas |
| Where agent and token server run in P0/P1 and how the ESP32 sees `/token` | Mac/LAN in P0–P1 (clear HTTP within the LAN, secret per device), cortes in P2 (F) |
| CI/registry for the images | Proposal: ghcr.io + GH Actions in the Sebastian repo; decide vs Harbor before Phase F |
| Privacy policy and audio retention | Define before shadow mode (Phase E): recordings behind env var (already in ROADMAP), enrollment WAVs and DDSD logs on local disk with 30-day retention, documented deletion; the pre-roll of false positives is discarded in the agent after the veto |
| IDLE decision brought forward | **D8** — at the end of Phase C |

---

## 6. Transversal Risks

- **Cascading dependency pins**: C SDK 0.3.10, `esp-tflite-micro/esp-nn`
  (real upstream breakage precedent: ESPHome PR #15628), `livekit-agents`,
  Speechmatics model (invalidates enrolled speaker IDs). A single owner for
  bumps; freeze versions and read changelogs.
- **Models in preview**: `gemini-2.5-flash-native-audio-preview-12-2025`
  (only one with `proactivity`), recent Nova 2 Sonic. The factory (D6) makes the
  change a 1-variable deploy; do not couple architecture to any.
- **Cost calculations are jul-2026 snapshot** on preview prices:
  instrument `session_usage_updated` from the first deploy of Phase C.
- **"Sebastián" is a common name**: legitimate mentions on TV/conversation
  will trigger the detector — mitigated in the state machine and the
  re-verification, not by lowering the model's recall.
- **Audio leak on false positives** (optimistic opening): bounded and
  visible (LED); document it in the privacy policy.

---

## 7. Pending updates to ROADMAP.md

1. Line "reuse `setMuted()` as gate" → separate `gate` vs `hard_mute` (D3).
2. Assumption "Opus DTX ≈ 0 network cost" → false at 48 kHz; the real economics are
   IDLE/Cloud minutes (D8).
3. "Decimate 48→16 kHz for the detector" → the detector eats 48 kHz directly;
   16 kHz only for pre-roll/agent (D2).
4. Re-label the DDSD gate as a **privacy/UX** feature (not savings):
   with cheap models LINGER without a gate costs <$4/month (annex 06).
5. The protocol uses publish_data for dev→agent (not outgoing RPC) (D1).
