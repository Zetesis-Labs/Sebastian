# AEC Project → full-duplex — implementation plan

> Synthesis of the multi-agent exploration from 2026-07-03 (4 agents: root cause,
> change-map, validation, risks). Goal: XVF3800 AEC converging in
> session → remove half-duplex gate → natural full-duplex (talking over the
> agent with any phrase, high volume, no echo).

## The finding that reorders the project

**The `FAR_EXTGAIN` "fix" was almost certainly a placebo.** The official XMOS doc
(Control Command Appendix v3.2.1) defines `AEC_FAR_EXTGAIN` in **dB**, not linear:
factory 0.0 means **0 dB = unity gain**, not "muted speaker". The
narrative of `docs/AEC.md` ("0.0 left the AEC inert") is a misdiagnosis: the
verification experiment changed both the config AND the stimulus at the same time (from full-scale voice
to a 5s continuous tone at 15% FS) — most likely the AEC
would have always converged with that tone. 2-min refutation test: probe with
`FAR_EXTGAIN=0.0` after power-cycle → if it converges the same, placebo confirmed.

Two more hard facts from the XMOS doc:
- **`AECCONVERGED` (33/3) is latched for life** ("once set to 1, never
  reset"). That the telemetry reads 0 in every session means the AEC **has never
  converged since boot** in production. It is not a sampling issue.
- **`SPENERGY` (33/80) is near-end energy (mic side), not far-end.** The
  inference "the AEC sees reference signal" was incorrect: today there is NO
  telemetry of the reference level. It is the #1 gap to instrument.

## Root cause hypotheses (ranked)

| # | Hypothesis | Prob. | Diagnosis | Lever |
|---|---|---|---|---|
| H1 | **The reference clips inside the XVF**: `REF_GAIN=8.0` (ReSpeaker build; XMOS default 1.5) × 0 dBFS playback saturates the internal reference → clipped signal that does not correlate → the linear filter never converges. The probe escaped (-16.5 dBFS tone). Explains ALL history (sw-vol was always a no-op → always full-scale). Corollary: `MIC_GAIN=90` (default 10) violates XMOS headroom. | High | Write `REF_GAIN=1.0` (35/1) and run a session; measure linearity with tone at increasing amplitudes via `MUX_FAR_END_W_GAIN` | `REF_GAIN≈1.0` in `applyConfig()`; review `MIC_GAIN`/`SHIFT` |
| H2 | **Non-linear echo path at full scale**: the tiny speaker distorts (already measured: ~6 dB harmonic cancellation at vol 60) and the linear AEC doesn't model it. | Medium-high | Session at vol 50-60 → converged=1? | Cap session volume; recover loudness with residual suppression (`PP_ECHOONOFF` 17/23) |
| H3 | **The agent's mono doesn't reach the LEFT slot** (the AEC reference is slot 0 of the XVF I2S-in; the probe wrote BOTH slots and never discriminated; that av_render duplicates mono is an unverified assumption). | Medium | Per-slot probe (tone only in slot 0, then only in 1, measuring `MUX_FAR_END`); hot probe in session | Explicitly duplicate to render if LEFT goes empty |
| H4 | Intermittent TTS: never accumulates the ~30s of far-end content that convergence requires. | Low-medium | Forced 60s monologue with converged sampled at 1s | — |
| H5/H6 | Different delay in session / XVF loses state | Low (almost completely ruled out) | Read real filter (33/91-93); expanded logState | — |

## Remote execution results (2026-07-03)

Phase 0 + Phase 1 executed **remotely** via a boot auto-test
(`config.probe_aec_on_boot`) that plays audio through the speaker and reports
convergence without session or human, read by Grafana after a reset.

**Systematically ELIMINATED suspects:**
- `REF_GAIN=8.0` (XMOS default 1.5): clipped the reference to full-scale.
  Confirmed in boot config, corrected to 1.0. **Necessary, not sufficient.**
- `MIC_GAIN=90` (default 10): tested 90→10; XMOS headroom rule.
- `FAR_EXTGAIN`: is dB; the correct one is 0.0 (factory). 1.0 was a placebo.
- `AECSILENCELEVEL` (33/2): read = **1e-9 (default)**, Seeed didn't raise it.
- **Excitation**: the probe used a pure tone at 0.85 FS (one band + speaker
  THD) → false `converged=0`. Redone with **white noise at -15 dBFS** (the
  real XMOS stimulus).
- **"Routing problem" from the first attempt: REFUTED by XMOS docs.** The agent
  relied on an erroneous reading of `AEC_CURRENT_IDLE_TIME` (it is a **CPU
  profiling counter in 10ns ticks**, not far-end activity). The
  `far_end_w_gain` mux that the probe measured **IS the AEC input** → the reference
  does arrive. `inthost` = INT build (native I2S slot 0 far-end, without source
  selector). The semi-official ReSpeaker ESPHome project uses the standalone AEC
  as is, without routing config — it is the intended design.

**Clean negative**: with REF_GAIN=1.0 + MIC_GAIN=10 (the combo never tested
together) + white noise + FAR_EXTGAIN=0.0 + default silence-level → `converged_at=-1`
(never). Under textbook conditions, the AEC does not adapt.

**Only major suspect left: CAUSALITY / DELAY.** XMOS: the reference
must arrive ≤40 samples *before* the echo, and "any variation of the
mic↔reference delay severely degrades"; gaps due to lost samples break
adaptation. Our TX is an I2S slave with `auto_clear_after_cb` (every av_render
underrun injects zeros = gaps in the reference). **Definitive instrument
pending**: read the real AEC filter coefficients (`SPECIAL_CMD_AEC_*`
33/90-94) — flat filter = doesn't adapt; peak at sample 0 = acausal; healthy peak
aligned with the delay = the path works and the problem is purely the delay
(adjust `SYS_DELAY` 35/26). And measure the mic↔reference cross-correlation for
the real delay. NO more gain sweeps.

**Definitive instrument executed — filter coefficients reading
(33/90-94), 90s of noise, snapshot every 15s:**
- The filter is NOT flat and the peak is at index ~38-64 (NOT at 0) → **the AEC
  DOES adapt, and causally.** Rules out routing AND delay/causality.
- But in 90s the peak never exceeds ~0.003 (echo is ~2.5% of the reference →
  the converged peak should be around 0.025, 8x more) and its index **jitters**
  (38↔62↔64). `ref_gaps=0`. → **It is NOT slow convergence, NOR gaps.** The filter
  **fails to lock a stable model of the echo.**
- Points to a **non-stationary / non-linear** echo path — most likely the
  **beamformer/DoA moving** (the echo path changes continuously) or
  speaker distortion. Underlying problem, not a config knob.

**BREAKTHROUGH — Experiment A: freezing the beam (2026-07-03).** The beamformer
hypothesis was not speculative: it was **confirmed and reproducible**. The probe
activates FIXED beams (`AEC_FIXEDBEAMSONOFF=1` 33/37 + `FIXEDBEAMSAZIMUTH` 33/81 to
the front) before the noise, and the result is sharp:

| Metric | Adaptive beam (before) | FIXED beam (experiment A) |
|---|---|---|
| `AECCONVERGED` | 0 (never, latched) | **1** |
| `converged_at` | -1 (never in 90s) | **1s** |
| filter peak | ~0.003, jittering 38↔64 | **0.024–0.032, stable** (idx 64/39) |
| `nonzero` taps | scrawny | **345–399** (populated filter) |
| `path_change` | — | **0** (stationary path) |

Two identical runs. **The adaptive beamformer WAS the root cause**: by tracking
the speaker it continuously changes the mic→echo path (non-stationary target that the
AEC can never lock). Frozen → stationary path → the AEC converges in ~1s
with a healthy filter. It was not a hardware limit.

**AEC project conclusion**: root cause found and **fixed**. After
exhaustively eliminating config (routing, REF_GAIN, MIC_GAIN, FAR_EXTGAIN,
silence-level, excitation, delay, gaps), the decisive instrument (filter
reader) pointed to the beam, and experiment A confirmed it. **The AEC CAN
converge.** Productized: `config.fixed_beam` + `fixed_beam_azimuth_deg` hardwire
the fixed beam in `applyConfig` (default off = adaptive, no UX change). The
trade-off is speaker tracking ↔ functional AEC: for a desktop speaker
a fixed beam towards the usage area is usually enough.

**Pending (requires being at home):** validate real full-duplex = `fixed_beam=true`
+ **remove the half-duplex gate** + measure in session (is the user heard well from
the fixed direction? is the echo cancelled without phantom turns?). Convergence
is tested remotely; session quality needs a human in the room. Device
left in known-good state: REF_GAIN=1.0 + FAR_EXTGAIN=0.0 + MIC_GAIN=90 +
`fixed_beam=false` + probe off. Reusable noise auto-test and filter reader.

## Phases

### Phase 0 — Instrumentation (without touching behavior; 1 flash)

1. Expanded `logState()`: `FAR_EXTGAIN`, `REF_GAIN`, `I2S_INACTIVE`,
   `AEC_CURRENT_IDLE_TIME` re-read in session (closes H6 and gives visibility).
2. Improved boot probe after flag (`probe_on_boot`): ERLE by **Goertzel at
   1 kHz** (the broadband peak is contaminated by speaker harmonics) + RMS,
   `t_converge_ms` (AECCONVERGED poll at 250 ms), parsable single line
   `probe result: ... erle_1k_cdb=... hot=0/1`.
3. Bridge: new gauges (`aec_path_change`, `aec_rt60_ms`, `aec_far_extgain`,
   `aec_ref_gain`, `erle_*`, `aec_converge_ms`), counter
   `aec_sessions_total{converged}` (`aec post-session:` line), and
   `echo_gated_peak` (the peak of the beam DURING the agent's speech, today
   invisible because the gate forces `mic_level=0`).
4. Agent: **phantom turns detector** (`phantom_turns_total{reason}`) —
   timing (during speaking / tail ≤2.5 s) + trigram overlap with what the
   agent just said + non-Spanish language. Full pseudocode in the
   validation report. With gate ON it must be 0 by construction: it is the
   main metric of the "after".
5. Dashboard: "AEC / Echo" row (converged+path_change+agent_state, converged
   sessions ratio, ERLE, gated_peak, phantoms).
6. **Redo the honest FAR_EXTGAIN A/B** (tone + power-cycle between runs)
   and correct the `docs/AEC.md` narrative — the "factory bug" story
   will contaminate future debugging if it stays.

### Phase 1 — Root cause experiments (one afternoon, in cost/benefit order)

1. `REF_GAIN=1.0` in `applyConfig()` + test session (H1, candidate #1).
2. Session at volume 50-60 (H2) — combinable with (1) the same afternoon.
3. If `converged=1` does not beat: hot probe (§T-HOT of the validation plan — the
   agent plays a 1 kHz tone through the real LiveKit→av_render path with
   the mic gated, the device switches `OP_R` to `MUX_FAR_END` and measures inside
   without touching TX) → discriminates H3 and gives the config snapshot in session.
4. Diagnostic matrix if the hot probe fails: `FAR_EXTGAIN≠1.0` in session →
   the fix reverts in runtime; `path_change` incrementing → I2S resyncs
   trigger the PCD; high `IDLE_TIME` → the AEC self-resets due to idle.

**Exit gate**: `converged=1` during real agent speech in session, ERLE
1 kHz ≥ 15 dB, `echo_gated_peak < 3000` (would not trigger the local VAD).

### Phase 2 — Stable convergence (F1 protocol)

10 consecutive sessions with real conversation ≥60 s: `aec_sessions_total{converged="1"}`
= 10/10, converged before the end of the first long turn, 0 reboots/heals.

### Phase 3 — Full-duplex by stages (change-map)

- **Stage 0 (plumbing, zero change)**: `sebastian.duplex` runtime flag via data
  channel — default **fail-safe**: every session is born half-duplex and the agent
  promotes it to full (`SEBASTIAN_DUPLEX=full|half`, default half). Rollback =
  restart the agent, without reflashing. `TurnDetection` with `interrupt_response=True`
  + `create_response=True` **explicit and commented** (today they default).
  The gate is conditioned (`if (!duplex_full and gatedByAgent())`), not yet deleted.
- **Known pitfalls to solve in this stage** (from the risk register):
  - The **wake word barge-in only runs over gated audio** — move the feed
    to the normal capture path (keep watching during `speaking`, the frame is
    published AND watched). It is KEPT as a deterministic safety net even
    in full-duplex (if the AEC degrades mid-session, "Sebastián" still cuts).
  - The **LINGER depends on the gate** (level=0 during the agent's speech): with the
    mic open, the echo would refresh `last_voice_tick` and sessions would not
    close due to silence. The close must rely on `agent_state` +
    level, and be re-validated with echo entering the meter.
  - The agent's gate-silence drop **stays** (it's from the pre-roll handoff gate,
    not the half-duplex one).
- **Stage 1 (canary)**: `SEBASTIAN_DUPLEX=full` + F2-F4 protocol: long
  monologue without gate (phantoms=0, no auto-interruptions), **volume sweep
  60/80/100** (acceptance: 0 phantoms up to 80; 100 = goal), interruption
  with content without wake word (p95 ≤1.5 s and responds to what was said, ≥4/5).
- **Stage 2 (tripwire)**: the agent detects the auto-interruption signature and
  hot-publishes `sebastian.duplex: half` (auto-degradation).
- **Stage 3**: F5 endurance (10 mixed sessions, 0 phantoms, closures due to
  silence/end_session, never by cap).

### Phase 4 — Cleanup and bake-in

Stable weeks later: truly delete `gatedByAgent`/hangover, decide the
fate of the flag (ops lever vs comptime), update `ROADMAP.md`,
`docs/STATUS.md` and rewrite `docs/AEC.md` with the real root cause.

## Open product decisions (for Rubén)

1. **Target volume**: if the AEC only cancels well ≤80, cap it, or adaptive volume
   (lower during TTS)? The ERLE-vs-volume curve of Phase 1 gives the
   data. (Remember: the historical "35" never existed — it always sounded at 100.)
2. **Channel**: stay in RIGHT (raw residual) vs PP residual suppression
   (`PP_ECHOONOFF`) — measured risk: suppression can clip the double-talk
   which is exactly the goal.
3. **Keep name barge-in in full-duplex** (recommended: yes).

## Do not repeat (summary; full list in the risk report)

Do not tune delays/gains without confirming the AEC adapts; no energy gates
(echo ≈ voice in level: it's a signal problem, not a threshold one); do not gate in
"thinking"; do not trust I2C writes without readback; do not assume that a probe result
in boot transfers to session; do not flash pure logic without host test
(`@min`→u9); the `stash@{0}`/`stash@{1}` stashes are the only record of the
reverted experiments — extract before deleting.
