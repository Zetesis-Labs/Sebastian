> **✅ RESOLVED 2026-07-02 — see [`docs/AEC.md`](../AEC.md).** Test 0 (mux far-end passthrough) confirmed the live reference; the root cause was factory `AEC_FAR_EXTGAIN=0.0` (AEC never adapted). Fix on boot with readback (`xvf_aec.applyConfig`), `AECCONVERGED=1` verified, software volume rebuilt (the noop set_vol nullified it, as this annex already pointed out).

> **Annex of the implementation report** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-checking). Where this annex contradicts the **Frozen decisions** of the main report, the report prevails.

# aec-verification — Verification and closure of the XVF3800 AEC reference (ROADMAP finding #2: volume capped at 35/100, unreliable barge-in)

**Verdict:** viable with risks — The architecture is confirmed by official XMOS doc: the AEC far-end reference enters through the LEFT channel (slot 0) of the XVF I2S input, that is, through the same GPIO44 line that the ESP already uses for the speaker; and the control protocol exposes metrics (AEC_AECCONVERGED) and routing (AUDIO_MGR_OP_*) sufficient to diagnose and close the loop. The remaining risk is hardware: there is no public schematic of the Seeed board confirming that GPIO44 electrically reaches the XVF I2S-in pin in addition to the AIC3104 — but the protocol itself allows verifying it in 10 minutes without opening the board (mux far-end passthrough).

**Effort:** M — 2-3 person-days (0.5 d I2C instrumentation, 0.5 d measurement playbook, 1-2 d closure: dynamic mux switch + AIC3104 volume + retune and barge-in validation)

## Findings
- CONFIRMED (official XMOS doc): the AEC reference is the LEFT (0) channel of the XVF3800 I2S input — 'A far-end AEC reference signal must be provided on the left (0) channel of the I2S or USB input signal' — and in the reference design that same signal goes to the DAC ('the DAC is configured to play the left input channel on both the right and left outputs'). The audio that the ESP sends through GPIO44 IS the reference, if the line reaches the XVF.  
  _https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/datasheet/03_audio_pipeline.html_
- The pipeline is AEC (linear, adaptive) → beamformer → post-processing; RESIDUAL/non-linear echo suppression (PP_ECHOONOFF, PP_NLATTENONOFF, PP_GAMMA_E*) lives ONLY in the comms channel post-processing (LEFT). The ASR beam published by the firmware (RIGHT slot, mux category 7) is 'AEC residual / ASR data': post-linear-AEC but without residual suppression → explains the residual echo that forces capping the volume to 35, even with a working reference.  
  _Table 26 in https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/user_guide/03_using_the_host_application.html + firmware/main/config.zig:6-17_
- The control protocol exposes everything needed, with resid/cmd verified against the official ReSpeaker driver (same write/read scheme as readBeamLed, which already works on inthost 1.0.7 with resid 33/75): AEC_AECCONVERGED (33,3 ro int32), AEC_AECPATHCHANGE (33,0), AEC_NUM_FARENDS (33,72), AUDIO_MGR_REF_GAIN (35,1 rw float, default 1.5), AUDIO_MGR_SYS_DELAY (35,26 rw int32), AUDIO_MGR_OP_L/OP_R (35,15/35,19 rw 2×uint8 <category,source>), PP_ECHOONOFF (17,23), PP_GAMMA_E/ETAIL/ENL (17,24/25/26), PP_NLATTENONOFF (17,27), SHF_BYPASS (33,70). There is no direct ERLE command: replaced by AECCONVERGED + A/B dB measurement.  
  _https://raw.githubusercontent.com/respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY/master/python_control/xvf_host.py (PARAMETERS, lines 28-135)_
- There is no public schematic of the board (Seeed only publishes STEP 3D). Indirect evidence that GPIO44 reaches the XVF: the XVF is the bus master (generates BCLK/WS of that line), CNX/Seeed describe bidirectional I2S XVF↔ESP32 and XVF↔AIC3104, and Seeed's own official yaml comments 'Try mono for XVF AEC' in the speaker pipeline. The category 4/5 mux ('Far end data received over I2S') allows verifying it electrically by software: route the far-end to the slot we publish and check if the TTS appears.  
  _homeassistant/respeaker.yaml:1326 + https://wiki.seeedstudio.com/respeaker_xvf3800_introduction/_
- The ESP already meets the format requirement: av_render sends 48 kHz / 2 channels / 32-bit (frame.channel = 2, the agent's mono is duplicated to both slots), so the LEFT slot carries signal. Current weak point: the 'volume 35' is applied via esp_codec_dev_set_out_vol over a noop codec_if (noopSetVol does nothing in hardware), and the real AIC3104 gain is fixed at 0 dB in registers — the actual volume control is diffused and needs to be rebuilt for the 'gradual volume + limiter'.  
  _firmware/main/app.zig:130-133 + firmware/main/board.zig:87-92,153-192_
- For barge-in with residual echo there is a dynamic I2C switch without reflashing: AUDIO_MGR_OP_R (35,19) allows switching the RIGHT slot between (7,3) raw ASR beam and (6,3) post-processed beam with residual echo suppression only while the agent is speaking (~1 I2C write of 5 bytes). Complements: AEC_ASROUTONOFF/GAIN (33,35/36) and PP_LIMITONOFF (17,19, comms channel only).  
  _Table 26 XMOS + xvf_host.py lines 79-84_

## Design

# Closure of the XVF3800 AEC reference

## 1. Confirmed architecture (primary citation)

- XMOS (datasheet 3.2.1, audio pipeline): **"A far-end AEC reference signal must be
  provided on the left (0) channel of the I2S or USB input signal"**, and the reference
  design DAC plays that same input channel. That is: **the line that
  the host writes (GPIO44) is both speaker signal and AEC reference**; the XVF
  (master) clocks that line and samples its LEFT slot as far-end.
- Pipeline: `Linear AEC → beamformer → post-proc`. The **residual/non-linear echo
  suppression only exists in the comms channel post-proc** (`PP_ECHOONOFF`, `PP_NLATTENONOFF`,
  `PP_GAMMA_E/ETAIL/ENL`). The RIGHT slot we publish (mux cat. 7 = "AEC residual /
  ASR data") is post-AEC **without** that stage → residual echo at high volume is expected
  even with a live reference.
- `app.zig` already sends 48k/stereo/32-bit (duplicated mono) → the LEFT slot carries the TTS. ✔
- Only gap: there is no Seeed schematic confirming that GPIO44 enters the XVF I2S-in pin
  (in addition to the AIC3104). To be verified by software (test 0).

## 2. I2C commands (add to xvf_dfu.zig, same pattern as readBeamLed)

| Name | resid | cmd | type | r/w | Usage |
|---|---|---|---|---|---|
| AEC_AECCONVERGED | 33 | 3 | int32 | ro | **key metric**: 1 = filter converged |
| AEC_AECPATHCHANGE | 33 | 0 | int32 | ro | acoustic path change detection |
| AEC_NUM_FARENDS | 33 | 72 | int32 | ro | must read ≥1 (far-end input present) |
| AEC_RT60 | 33 | 9 | float | ro | negative = invalid estimate (clue of dead ref) |
| SHF_BYPASS | 33 | 70 | uint8 | rw | AEC bypass (to measure E_without_aec) |
| AEC_ASROUTONOFF / GAIN | 33 | 35/36 | int32/float | rw | ASR beam mode and gain |
| AUDIO_MGR_REF_GAIN | 35 | 1 | float | rw | reference pre-gain (def. 1.5) |
| AUDIO_MGR_SYS_DELAY | 35 | 26 | int32 | rw | ref↔mic alignment (-64..256 samples) |
| AUDIO_MGR_OP_L / OP_R | 35 | 15/19 | 2×uint8 | rw | `<category,source>` mux per slot |
| PP_ECHOONOFF | 17 | 23 | int32 | rw | residual echo suppression (comms only) |
| PP_GAMMA_E / ETAIL / ENL | 17 | 24/25/26 | float | rw | suppression aggressiveness (0-2 / 0-2 / 0-5) |
| PP_NLATTENONOFF | 17 | 27 | int32 | rw | non-linear echo attenuation |
| PP_LIMITONOFF | 17 | 19 | int32 | rw | comms channel limiter |

Format (identical to `readBeamLed`): read `write{resid, cmd|0x80, N+1}` + `read{status,
N bytes LE}`; write `write{resid, cmd, N, payload…}` (int32/float = 4 bytes LE).
Useful mux categories: **4/5** far-end (reference passthrough!), **6,3** auto-select
post-processed beam, **7,3** ASR beam (current RIGHT), **3,n** amplified mic, **0** silence.

## 3. Test playbook (30-60 min, with record.py)

**Test 0 — does the reference arrive electrically? (10 min, decisive)**
`AUDIO_MGR_OP_R ← (5,0)` (far-end with delay) via I2C, play TTS/noise, record with
`agent/record.py`. **The TTS is heard in the recording → the reference enters the XVF (ref electrically OK). Silence → dead reference** (GPIO44 does not reach the XVF I2S-in or empty slot).
Restore `OP_R ← (7,3)`.

**Test A — suppression with pink noise (nobody speaking).** Publish pink noise to the room
(`lk room join --identity noise --publish pink.ogg sebastian`), record 20 s of the mic
track in three RIGHT mux configurations: (3,0) raw mic → E_raw; (7,3) ASR → E_asr;
(6,3) comms → E_comms. Suppression = `20·log10(RMS_raw/RMS_x)`. Floor baseline: same
recording with the speaker muted.

**Test B — I2C metrics during playback.** Log every 500 ms:
`AECCONVERGED`, `PATHCHANGE`, `RT60`, `NUM_FARENDS`.

**Interpretation (numerical criteria):**
- **Ref OK**: CONVERGED=1 stable in <5 s, ASR suppression **≥ 18-25 dB**, comms ≥ 30-40 dB.
- **Dead ref**: CONVERGED=0 always, ASR suppression **≤ 3 dB**, invalid RT60, Test 0 in silence.
- **Only non-linear residual** (expected case): CONVERGED=1, ASR 15-25 dB but comms ≫ ASR
  (the ASR↔comms gap IS the residual suppression that the RIGHT slot lacks).

**Test C — LEFT vs RIGHT**: repeat A with `config.zig mic_channel = .left` (or without
reflashing, already covered by the (6,3) vs (7,3) mux from test A).

## 4. Decision tree

1. **Dead ref** → a) confirm that av_render writes LEFT≠0 (TX dump);
   b) `AUDIO_MGR_REF_GAIN` to 1.5-4 and `AEC_FAR_EXTGAIN` (33,5); c) if Test 0 gives pure
   silence: GPIO44 does not reach the XVF → physical rewiring (DOUT→XVF I2S-in bridge) or assume
   permanent half-duplex (`mic_src.setMuted` during TTS).
2. **Ref OK + non-linear residual** (most likely) → in order of cost:
   a) **dynamic mux switch via I2C**: `AUDIO_MGR_OP_R ← (6,3)` (comms, with residual
   suppression) when the agent starts speaking, `← (7,3)` when finishing. 5 bytes I2C, without
   reflashing, preserves barge-in (comms channel is designed for full-duplex); raise
   `PP_GAMMA_ENL` (up to 5.0) and `PP_NLATTENONOFF=1` if it still bleeds through;
   b) rebuild the **real volume** (today `set_out_vol(35)` falls into a noop codec): control
   the AIC3104 DAC registers (0x2B/0x2C) and increase gradually measuring E_asr;
   c) fine-tune `AUDIO_MGR_SYS_DELAY` if PATHCHANGE oscillates (misaligned ref);
   d) **selective half-duplex only at high volume**: gate with `mic_src.setMuted(true)`
   while the agent speaks if volume > threshold — sacrifices barge-in only then.
3. **Final validation**: TTS at 60-70 volume, speak over it → the agent must transcribe
   the user without self-listening (barge-in) and without feedback loop.

## 5. Effort

Zig instrumentation (~80 lines in xvf_dfu.zig) + tests: 1 day. Dynamic mux switch
tied to playback state + AIC3104 volume + retune: 1-2 days.

## Code
**firmware/main/xvf_dfu.zig (add)** — AEC diagnostics and routing via I2C — same write/read pattern as readBeamLed, ready to paste

```zig
// --- AEC diagnostics & routing (resid 33 AEC, 35 AUDIO_MGR, 17 PP) ---------
const RESID_AUDIO_MGR: u8 = 35;
const RESID_PP: u8 = 17;

fn readU32(resid: u8, cmd: u8) ?u32 {
    const req = [_]u8{ resid, cmd | READ_BIT, 5 };
    var resp: [5]u8 = undefined;
    if (!(write(&req) and read(&resp)) or resp[0] != 0) return null;
    return @as(u32, resp[1]) | (@as(u32, resp[2]) << 8) |
        (@as(u32, resp[3]) << 16) | (@as(u32, resp[4]) << 24);
}

/// 1 = AEC filter converged (the key metric of test B).
pub fn readAecConverged() ?bool {
    return if (readU32(RESID_AEC, 3)) |v| v != 0 else null;
}
pub fn readAecPathChange() ?bool {
    return if (readU32(RESID_AEC, 0)) |v| v != 0 else null;
}
pub fn readNumFarends() ?u32 {
    return readU32(RESID_AEC, 72); // must be >= 1
}
pub fn readRt60() ?f32 {
    return if (readU32(RESID_AEC, 9)) |v| @bitCast(v) else null; // <0 = invalid
}

/// Output mux per slot: cat 7/3=ASR beam (current R), 6/3=comms with residual
/// suppression, 5/0=far-end passthrough (test 0: "hear" the reference), 3/0=raw mic.
pub fn setOutputR(category: u8, source: u8) bool {
    return write(&[_]u8{ RESID_AUDIO_MGR, 19, 2, category, source }); // AUDIO_MGR_OP_R
}
pub fn setOutputL(category: u8, source: u8) bool {
    return write(&[_]u8{ RESID_AUDIO_MGR, 15, 2, category, source }); // AUDIO_MGR_OP_L
}

/// Comms channel residual echo suppression and its non-linear aggressiveness.
pub fn setPpEcho(on: bool) bool {
    return write(&[_]u8{ RESID_PP, 23, 4, if (on) 1 else 0, 0, 0, 0 }); // PP_ECHOONOFF i32 LE
}
pub fn setPpGammaEnl(v: f32) bool { // [0.0 .. 5.0]
    const b: [4]u8 = @bitCast(v);
    return write(&[_]u8{ RESID_PP, 26, 4, b[0], b[1], b[2], b[3] }); // PP_GAMMA_ENL
}
```

**test playbook (shell, does not go to repo)** — 30-60 min measurement session: pink noise through the LiveKit room + capture with record.py per mux configuration

```bash
# 0) prepare stimulus (20 s of pink noise, ogg/opus for lk)
ffmpeg -f lavfi -i "anoisesrc=color=pink:duration=20" -ar 48000 pink.ogg

# 1) fresh room with ONLY the board in it (no agent -> no OpenAI cost)
lk room delete sebastian && echo "reset the board now"

# 2) for each RIGHT mux config {(3,0) raw, (7,3) ASR, (6,3) comms}:
#    write the mux (firmware log, or debug button/CLI that calls setOutputR)
#    and capture the published track while the noise plays:
lk room join --identity noise --publish pink.ogg sebastian &
python agent/record.py --out take_asr.wav        # 20 s

# 3) floor baseline: same capture without publishing anything (speaker muted)
python agent/record.py --out take_floor.wav

# 4) metrics -> suppression = 20*log10(RMS_raw / RMS_x)
python - <<'EOF'
import wave, numpy as np
for f in ["take_raw.wav","take_asr.wav","take_comms.wav","take_floor.wav"]:
    w = wave.open(f); x = np.frombuffer(w.readframes(w.getnframes()), np.int16)
    print(f, "RMS=", np.sqrt(np.mean(x.astype(np.float64)**2)).round(1))
EOF
# Criteria: ASR >= ~18-25 dB under raw -> ref OK;
#           ASR <= ~3 dB -> dead ref;
#           comms >> ASR -> the gap is the residual suppression that RIGHT lacks.
```

## Risks
- **That GPIO44 does not electrically reach the XVF I2S-in pin on the Seeed board (without a public schematic it is not verifiable on paper) — the reference would be dead and the AEC will never converge.** → Test 0 (far-end passthrough mux, 10 min) resolves this before investing anything else; if it fails, fallback to half-duplex with mic_src.setMuted (already implemented) and evaluate physical bridge.
- **The Seeed inthost 1.0.7 firmware is a custom build: some commands from the table (PP_*, AUDIO_MGR_OP_*) might not be served, as already happened with RESID_LED 0x0C (docs/MIC_CHANNEL_TUNING.md).** → All reads return ctrl_status: test each command with read and validate status==0 before trusting it; resid 33 (AEC) and 20 (GPO) are already tested in this firmware.
- **Switching OP_R to (6,3) during TTS reintroduces the 'tinny' timbre (comms NS + agent BVC) right on turns with barge-in, and the comms channel AGC changes the level with respect to SHIFT=14.** → Apply the switch only while the agent speaks (short window), adjust PP_MIN_NS/PP_AGCONOFF via I2C to soften the post-proc, or dynamically compensate SHIFT; measure with the same metrics pipeline from MIC_CHANNEL_TUNING.md.
- **The current volume control is fictitious (noop codec_if): raising volume by touching the AIC3104 can change the acoustic and gain path that the AEC had learned, causing audible re-convergences.** → Gradual increases (≤3 dB per step) monitoring AEC_AECPATHCHANGE/AECCONVERGED; leave AUDIO_MGR_REF_GAIN coherent with the real DAC gain.
- **Barge-in with active residual suppression attenuates double-talk (the user speaking over the agent reaches the STT clipped).** → Tune PP_GAMMA_E/ETAIL with double-talk phrases recorded with record.py; if it degrades, prefer the moderate volume + linear AEC (RIGHT) route and gate only at high volume.

## Open questions
- Is AUDIO_MGR_OP_R (resid 35) served in the Seeed inthost-lr48 1.0.7 build? (RESID_LED 0x0C was not; resid 33 and 20 do work — first read with status==0 confirms it in 1 min).
- What (category,source) does the factory 1.0.7 firmware report for OP_L/OP_R? (read 35/15 and 35/19 with READ_BIT; expect L=(8,0) and R=(7,3) or similar).
- Does av_render really duplicate the agent's mono to the TX LEFT slot or only fill RIGHT? (dumping 4 frames from the TX buffer into a debug log closes this; conditions the 'dead ref' diagnosis).
- Where does the 'volume 35' really act today if the codec_if is noop? (esp_codec_dev software gain, av_render, or it doesn't act and the level is fixed by the AIC3104's 0 dB?).
- Does the comms channel (6,3) with PP_ECHOONOFF degrade double-talk enough to break the barge-in that motivates all this? (double-talk test pending hardware).

## Sources
- https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/datasheet/03_audio_pipeline.html
- https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/user_guide/03_using_the_host_application.html
- https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/user_guide/AA_control_command_appendix.html
- https://raw.githubusercontent.com/respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY/master/python_control/xvf_host.py
- https://github.com/respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY
- https://wiki.seeedstudio.com/respeaker_xvf3800_xiao_getting_started/
- https://wiki.seeedstudio.com/respeaker_xvf3800_xiao_i2s/
- https://wiki.seeedstudio.com/respeaker_xvf3800_xiao_record_playback/
- https://wiki.seeedstudio.com/respeaker_xvf3800_introduction/
- https://www.cnx-software.com/2025/07/29/respeaker-xmos-xvf3800-4-mic-array-board-features-esp32-s3-module-works-over-usb/
- https://community.home-assistant.io/t/respeaker-xmos-xvf3800-esphome-integration/927241