# TESTING — Sebastian

Testing strategy for the Sebastian speaker (ESP32-S3 + XVF3800, Zig firmware + Python agent). Honest document: **what we test today, what we want to test, and what cannot be automated.**

## Thesis (read this first)

The tests we have today are **host tests**: pure logic compiled and executed on the CI machine, without hardware. They are good and necessary, but they cover **the layer where we had almost no bugs**. Almost all real project failures lived in hardware/integration/acoustics — the layer a host test **does not touch**:

| Real bug (historical) | Caught by host test? | Layer |
|---|---|---|
| I2S helicopter/warble (clock drift) | ❌ | HW timing |
| AEC not converging (adaptive beam) | ❌ | physical/acoustic |
| `FAR_END_DSP_ENABLE` reverts on power-cycle → feedback | ❌ | XVF state |
| SCTP storm bringing down the session | ❌ | transport/network |
| Session cut (AEC cancels so well → "silent" mic → timeout) | ❌ | integration |
| USB-serial-JTAG hangs | ❌ | hardware/USB |
| `@min` narrows the type → overflow | ✅ (pure logic) | — |

**Honest conclusion:** a green CI gives a false sense of coverage. Guaranteeing quality requires climbing up the pyramid to the hardware.

## The test pyramid

```
        ┌───────────────────────────────┐
   4    │  Acoustic / end-to-end        │  ← real echo, double-talk, far-field wake
        │  (rig + human, NO automatiz.) │     MOST IMPORTANT, least covered
        ├───────────────────────────────┤
   3    │  Hardware-in-the-loop (HIL)   │  ← board in runner: flash → probe →
        │  (probes + telemetry)         │     assert telemetry. WE HAVE the pieces
        ├───────────────────────────────┤
   2    │  On-target unit tests         │  ← runs on S3 (not mounted yet)
        ├───────────────────────────────┤
   1    │  Host tests (pure logic)      │  ← WHAT WE HAVE. green CI ≠ works
        └───────────────────────────────┘
```

Rule: each layer is **necessary but not sufficient**. Layer 1 is cheap and covers little risk; layer 4 is expensive and covers the real risk.

**Transversal — Digital twin / simulation**: sits between levels 2 and 3 (target and protocol realism **without board**), but capped at acoustics. See the "Digital twin" section below.

---

## What we HAVE today

### Level 1 — Host tests (`firmware/core_test.zig`)

**How they run:**
- **CI**: `.github/workflows/firmware-tests.yml` — downloads the pinned Zig fork (kassane/zig-espressif-bootstrap `0.16.0-xtensa`, the same one in `firmware/cmake/zig.cmake`) and runs `zig test core_test.zig` on the `herschel-runners` runner. Does not use ESP-IDF or `idf.py`.
- **Local**: `cd firmware && ../tools/zig.sh test core_test.zig` (uses the zig from the build in `firmware/build/zig-relsafe-*` or the one in PATH).

**The pattern**: testable logic is **extracted to `firmware/main/core/`** (without ESP/`extern` deps) to be compiled on the host. Covered modules: `aec_core`, `decimator`, `mic_gate`, `pre_roll_core`, `token_core`, `xvf_pcm`.

**What they cover well** (39 tests, with serious techniques — they are not trivial):
- **Decimator (DSP)**: *golden* vectors with exact output (impulse response, FIR warm-up with unity gain), **differential tests** (chunked == full in different partitions + generated patterns), phase preservation, bounded saturation, "never writes past the caller's buffer".
- **Pre-roll (ring buffer)**: 6×6×5 invariant matrix + **reference model** compared step-by-step in scripted sessions; wrap/anchor well caught.
- **Mic gate (FSM)**: event table + reference model on generated scripts; finite hangover and its interaction with full-duplex/mute.
- **Token parser**: rejects control bytes (`\x00 \t \x7f`), enforces `ws/wss`, and **does not touch buffers on failure** (no partial copies).
- **PCM / AEC encoding**: shift+clip, softclip, little-endian, NaN/inf in scaled telemetry.

**Honest assessment**: high depth, but only on **logic that is easy to dismantle**. See the holes below.

### Level 3 (manual) — Hardware probes + telemetry

This is what is **not usually counted as testing, but it is**: the self-tests the device itself runs, read by telemetry. Today they are **manual**.

- **Probes** (in `firmware/main/xvf_aec.zig`, flags in `config.zig`, all `false` in production):
  - `probeReference()` (`probe_aec_on_boot`) — plays noise and reports if AEC converges (`converged_at`, filter peak, `ref_gaps`).
  - `probeDualChannel()` (`probe_dual_channel_on_boot`) — path B: residual comms echo vs raw ASR, with fixed and adaptive beam.
  - `probeOutputGain()` (`probe_output_gain_on_boot`) — measures if a knob acts as master volume.
  - `logPpConfig()` — dumps the post-processor config on every boot.
- **Telemetry** (`tools/telemetry/bridge.py`): serial → OTLP → LGTM stack (Grafana/Loki/Prometheus/Tempo). It is the **assertable output**.

When we validate "flash → `converged_at=1s`, filter 0.025 → ✓", that is a **HIL test executed by hand**. The pieces exist; what's missing is the harness to automate it.

---

## What we do NOT cover (holes, by risk)

1. **🔴 The session machine (`app.zig SessionLoopState`)** — where the field bug lived (the `silence_timeout` cutting mid-sentence, the keepalive by `render_peak`, the agent hangover). It is not in `core/`, so **zero tests**. It is pure logic and timing-dependent: extractable to `core/session_core.zig` and testable with a simulated clock.
2. **🔴 The AEC fail-closed (`applyConfig`)** — the safety decision ("if the beam is not verified by readback → drops to half-duplex → do not open the mic over echo") is not tested; only the *encoding*. An erroneous `true` = ghost turns. Testable with a mock I2C transport.
3. **🟠 Integration / wiring** — everything is isolated unit tests. Nobody verifies that `mic_src` calls the gate, that the decimator receives the correct slot, or that the pre-roll empties on handoff. A refactor that breaks the seams passes CI.
4. **🟠 The C layer** (`provisioning.c`, `token_http.c`) — the `sebastian.config.v1` parser (external input via serial!), the NVS, WiFi bring-up. Without network (the Zig `token_core` parser is covered, but not the production C).
5. **🟡 The wake word decision** — the decimator that feeds it is heavily covered, but the threshold (`0.80`) / debounce is not.
6. **🟡 Golden vectors = change detectors, not correctness** — they encode "what it does today". If the FIR coefficients were wrong, the golden would lock in the error.

---

## What we WANT to have

### Short term — close the host test holes
- Extract `SessionLoopState` → `core/session_core.zig` and test timeout/keepalive/hangover with simulated clock. **(Covers the field bug.)**
- Extract the AEC fail-closed logic behind a mock transport.
- Port the provisioning parser to Zig (like `token_core`) to be able to test it.

### Medium term — Level 3: HIL smoke test (highest ROI)
A **dedicated board** (ESP32-S3+XVF) attached to a runner (`herschel`, or a Pi with the board). On every build: **flash → boot → run `probe_aec_on_boot` → assert on telemetry**:
- `converged == 1` and `converged_at` ≤ N s
- AEC filter peak in expected range
- `FAR_END_DSP_ENABLE == 1` (would have caught the feedback regression of this session)
- healthy boot (no reboot-loop, `BOOT OK`, IP assigned)
- healthy mic levels (`pcm peak` in range, not 0, not saturated)

Reuses **everything** that already exists (probes + `bridge.py`). It is the difference between "it compiles" and "the AEC actually converges on the board".

### Medium term — Level 2: on-target unit tests
Compile+run a subset of tests on the S3 itself (Zig test / Unity). Catches specific target behavior that the host does not reproduce.

### Long term — Level 4: acoustic rig
Bench with speaker + mic + the device in a controlled space, playing **labeled golden audio**, measuring:
- echo cancellation level (suppression dB)
- wake accuracy on labeled recordings (recall / false positives)
- double-talk behavior (does path B eat your voice?)

Semi-automatable: the stimuli and some metrics yes; the final judgment, not entirely.

---

## Digital twin / simulation (transversal, without board)

"Digital twin" **is not one thing, it's a stack** — feasibility and value change a lot by layer. Golden rule: **a twin models what you program; it does not discover emergent physical behavior.** That is why it is excellent for **regression** and useless for **discovery** of the physical/analog.

### The layers

1. **CPU + firmware — QEMU (high feasibility).** Espressif's QEMU fork boots ESP-IDF apps on an emulated ESP32-S3 (`idf.py qemu`) and runs the **real binary**: boot, state machine, logic in target context (would catch things like the `@min` overflow, which manifested on target). Caveat: I2S/I2C/WiFi are barely emulated → they must be stubbed.
2. **XVF3800 — software model (medium feasibility, high punctual value).** The proprietary DSP is not emulated, but **its `device_control` protocol is**: an I2C mock that returns registers (`AECCONVERGED`, config-drift…) and produces I2S frames. Makes the firmware↔XVF interaction testable: `applyConfig`, the AEC **fail-closed**, DFU. This is where the twin shines.
3. **Acoustics — low fidelity right where it matters.** Convolving TTS with a room impulse response works for the audio pipeline and wake, but **echo/AEC/double-talk** (non-stationary due to the beam, non-linear due to speaker THD) is what a simple model gets wrong. Paradox: if the echo model were good, you wouldn't need the real AEC.

### The pragmatic version: twin by traces (record-and-replay)

Instead of simulating XVF+acoustics, **record real traces once** (the I2S output of the device during a session: echo, voice, noise) and **replay them offline** in the pipeline on CI. Twin fed by reality: cheap, deterministic, catches pipeline regressions (**wake, decimator, gate**) against real hardware data. **High ROI, low effort** — start here. We already have `bridge.py` to capture.

### What it DOES and DOES NOT give you

| The twin DOES give you | The twin DOES NOT give you |
|---|---|
| Target firmware/logic regressions (QEMU) | If the AEC **really** converges in your room |
| Correct handling of the XVF protocol (fail-closed, DFU, drift) | Real echo cancellation quality |
| Audio pipeline on recorded/labeled audio (wake) | **Double-talk** (does path B eat your voice?) |
| Determinism, runs on every commit, without board | Real far-field ASR, the exact SCTP storm |

**Does not replace HIL or the acoustic rig — it complements them**: the twin runs on every commit and pushes the coverage of levels 1-3 towards realism without a board; level 4 (acoustic) still needs reality or a physics model that is itself a research project.

### Recommended order (by ROI)

1. **Record-and-replay** of the audio pipeline (cheap, reuses the capture bridge).
2. **XVF Mock** to test the AEC fail-closed and DFU.
3. **QEMU-S3** for boot + target state machine.

---

## Honest limitations (what CANNOT be guaranteed just with tests)

- **Double-talk**: if the non-linear suppressor on path B eats your voice when you talk over it, it is only validated with real voice in the room. A rig helps, but "sounds natural" is subjective. → A human guinea pig will always be needed.
- **Far-field ASR quality**: "does it understand you well at 4 m with noise?" depends on the remote model + room acoustics; there is no binary assert.
- **SCTP storm / transport drops**: non-deterministic network conditions; reproducing them reliably in CI is very hard (the bug is upstream, `esp-webrtc-solution#186`).
- **Real-world wake accuracy**: approximated with labeled recordings, but the tail of false positives appears in real use, not in the dataset.
- **HIL cost**: requires a dedicated board + a runner, and the native S3 USB is fragile (hangs, re-enumerates) — the harness itself will need retries and health-checks.
- **Golden vectors**: any legitimate FIR re-tuning requires regenerating them by hand; they are change-detectors, not correctness proofs.

---

## How to run (quick reference)

```bash
# Host tests (local) — from firmware/
cd firmware && ../tools/zig.sh test core_test.zig

# Host tests (CI): automatic on push/PR touching firmware/**
#   .github/workflows/firmware-tests.yml

# Manual HIL (today): enable a probe and read the telemetry
#   1) set config.probe_aec_on_boot = true (or the relevant probe)
#   2) idf.py -p <port> flash
#   3) read the serial / Grafana: converged_at, filter, far_end_dsp
#   4) revert the flag to false before committing
```

---

*See also: `ROADMAP.md` §7 (XVF DSP exploitation, where the probes live)
and §"DevEx / observability" (the telemetry stack that makes HIL possible).*
