# XVF3800 AEC: diagnosis and fix

**Resolved 2026-07-02.** The XVF3800 AEC never worked in this project —
not because of delay, nor wiring, nor I2S format: **the ReSpeaker factory build
`inthost 1.0.7` comes with `AEC_FAR_EXTGAIN = 0.0`** (linear scale).
With that value, the AEC assumes the speaker is playing silence and **never
adapts**. The fix is a 4-byte I2C write on boot:
`FAR_EXTGAIN = 1.0` (`xvf_aec.applyConfig()`).

Verified on hardware: `AECCONVERGED` went from permanent 0 to 1 in <5 s of
tone; in a real session the agent no longer transcribes itself with the speaker at
full scale.

**Collateral finding (volume):** `esp_codec_dev_set_out_vol` was always a
no-op — the `noop_codec_if` implemented `set_vol`, which suppresses the esp_codec_dev
software volume (only created if `codec->set_vol == NULL`). The
historical "we lower to 35 to prevent feedback" was a placebo: the speaker always played at
full-scale. Fixed by removing `set_vol` from the noop; now `set_out_vol(100)` =
0 dB reproduces the usual loudness and the control is real (foundation for whisper
mode and volume control via RPC).

## Why the previous attempt failed

In the last session we tried to auto-calibrate `SYS_DELAY` by sweeping values at
runtime and measuring the residual. It never converged because **we were tuning the
delay of an AEC that wasn't adapting**: with FAR_EXTGAIN=0.0 there is no delay value
that works. Lesson: first verify that the canceller has a signal and
wants to work, then tune.

## The (reproducible) method

1. **Get the command table with wire IDs.** The XMOS doc lists the
   commands without numeric resid/cmd; the real map is compiled in
   `libcommand_map.dylib` from the `respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY` repo
   (`host_control/mac_arm64/`). The accessors are `extern "C"`
   (`get_num_commands`, `get_cmd_id_info`, `get_cmd_val_info`, `get_cmd_name`):
   a 30-line C++ shim with `dlopen` dumps the 147 commands →
   [`xvf3800_command_map.txt`](xvf3800_command_map.txt). The `print_enum_*`
   functions from the same dylib decode the audio mux enums.

2. **Read the AEC state via I2C** (`xvf_aec.zig`, device_control protocol:
   write `[resid, cmd|0x80, n+1]` → read `n+1` with status in byte 0;
   status `0x40` = retry, retry the entire transaction):
   - `AEC_AECCONVERGED` (33/3, i32 RO) — latched; the key data
   - `AEC_RT60` (33/9, f32 RO), `AEC_AECPATHCHANGE` (33/0)
   - `AUDIO_MGR_SYS_DELAY` (35/26), `REF_GAIN` (35/1), `MIC_GAIN` (35/0)
   - `AEC_FAR_EXTGAIN` (33/5, f32 RW) ← **the culprit**

3. **In-band reference probe** (`xvf_aec.probeReference()`, disabled in
   normal boot): the audio manager output mux (`AUDIO_MGR_OP_R`, 35/19)
   allows routing internal signals to the I2S output. Routing
   `MUX_FAR_END[4]`/`MUX_FAR_END_W_GAIN[12]` to the right slot and playing a
   tone, we measure from the ESP if the reference reaches the XVF. Result: it arrived
   perfectly (with the exact ×8 of REF_GAIN) — ruled out wiring/format and
   left only the AEC config as a suspect.

## Factory state of the ReSpeaker build (reference)

```
farends=1  sys_delay=-30  ref_gain=8.0  mic_gain=90.0  far_extgain=0.0  ← the bug
shf_bypass=0  far_end_dsp_enable=0  i2s_dac_dsp_enable=0
op_all = [USER_CHOSEN 0] [RAW_MICS 0] [RAW_MICS 2] [AEC_RESIDUALS 3] [RAW_MICS 1] [RAW_MICS 3]
```

Valuable collateral data: the **RIGHT** channel we consume for wake word and STT
is `MUX_AEC_RESIDUALS` ch3 (`OP_R=[7,3]`) — literally the AEC residual,
without residual echo suppression or NS. Consistent with what was observed in
`MIC_CHANNEL_TUNING.md`.

## Output mux categories (decoded from the dylib)

```
0 MUX_SILENCE          1 MUX_RAW_MICS        2 MUX_UNPACKED_MICS
3 MUX_MICS_W_GAIN      4 MUX_FAR_END         5 MUX_FAR_END_SYSDELAY
6 MUX_PROCESSED_MICS   7 MUX_AEC_RESIDUALS   8 MUX_USER_CHOSEN_CHANNELS
9 MUX_ALL_USER_CHANNELS  10 MUX_FAR_END_NATIVE  11 MUX_DELAYED_MICS
12 MUX_FAR_END_W_GAIN
```

## Pending / next tunings

- `SYS_DELAY`: the default -30 works (converges); measure the real delay with
  chirp + correlation if we want to squeeze ERLE.
- The residual with pure tone at vol 60 cancels ~6 dB peak — the harmonics of the
  little speaker are not linearly cancellable; with real voice the end-to-end
  behavior is good (no auto-transcription). If more is needed:
  `PP_ECHOONOFF`/residual suppression on the comms channel, or the PP NL model.
- Increase volume above 60 when there are more hours of use without feedback.
- `AEC_RT60` stays at 0 with stationary tone; with voice it should estimate. Watch out if
  it never moves.
