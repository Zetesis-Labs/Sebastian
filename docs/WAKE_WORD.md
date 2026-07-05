# Wake word: "Sebastián"

**On-device** wake word detection with [microWakeWord](https://github.com/kahrendt/microWakeWord)
(streaming CNN over TFLite-Micro). The device listens locally with a
62 KB model and **only opens the LiveKit session (and therefore the paid Realtime session)
when it hears its name**. At rest the cost is zero.

Responds to its name, **"Sebastián"** (es), trained on the model.

## Architecture

```
                    IDLE (always)                       ACTIVE (after wake word)
┌─────────────────────────────────────┐   ┌──────────────────────────────────────┐
│ XVF3800 ──I2S 48k──▶ wakeword.zig   │   │ XVF3800 ──I2S 48k──▶ mic_src.zig     │
│   decimation 3:1 → 16k mono         │   │   → esp_capture → Opus → LiveKit     │
│   → mww.cpp (frontend + CNN 62KB)   │   │ LiveKit ──▶ av_render → AIC3104      │
│   → prob > 0.62 (moving avg of 4)   │   │ silence → close session → IDLE       │
└─────────────────────────────────────┘   └──────────────────────────────────────┘
```

- **`firmware/components/mww/`** — ESP-IDF component in C/C++:
  - `mww.cpp` + `mww.h`: `extern "C"` shim over TFLite-Micro
    (`MicroMutableOpResolver` with the 13 ops of the model, `MicroResourceVariables`
    for streaming state, 48 KB arena in internal SRAM).
  - `microfrontend/`: the C audio-frontend of TFLM (30 ms window/10 ms step,
    40-channel mel filterbank 125–7500 Hz, noise reduction, PCAN, log-scale).
    Copied from `esphome/esp-micro-speech-features` (same code as
    `pymicro-features`, which was used in training).
- **`firmware/main/wakeword.zig`** — detection FreeRTOS task:
  - Reads direct I2S RX (48 kHz stereo 32-bit), takes the slot configured in
    `config.zig` (RIGHT/ASR by default), decimates 3:1 → 16 kHz mono int16 with the
    same `softClip` as `mic_src.zig`.
  - Embeds the model with `@embedFile("sebastian.tflite")`.
  - `detected` is an atomic that `app_main` polls; `stop()` does the clean hand-off
    of the I2S channel (disable/enable) to `mic_src`.
- **`firmware/main/app.zig`** — main loop:
  `wait for wake word → open LiveKit room → local silence (or disconnection) → close → repeat`.

## Model

| Property | Value |
|---|---|
| File | `wakeword/sebastian.tflite` (copied to `firmware/main/` to embed) |
| Size | 62 KB (int8) |
| Input | `[1, 2, 40]` int8 — 2 10 ms features frames per Invoke (20 ms stride) |
| Output | `[1, 1]` uint8 — probability (1/256 scale) |
| Input quantization | scale 0.10196079, zero-point −128 |
| Recall (validation) | 99.29 % |
| False positives | 0.93/hour in ambient noise |
| Threshold | moving average of 4 probabilities > 0.62 |
| TFLM Arena | 48 KB internal SRAM (the JSON said 30 KB but resource variables are separate) |

## Training

Trained on the M4 Pro (MPS) with
[microWakeWord-Trainer-AppleSilicon](https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon).
See `wakeword/README.md` to reproduce it.

**Positive data (18,220 samples):**
- 18,100 TTS "Sebastián" — 9 Piper voices in Spanish
  (AR/ES/MX accents), generated with piper-sample-generator.
- 120 real recordings via the XVF3800 itself (78 "Sebastián"
  from the user + 42 from a second person), chopped with `wakeword/split_wakeword.py`.

**Negative data:** AudioSet (3 tars), FMA small, WHAM noise — ~20 GB,
downloaded and converted to 16 kHz by the trainer.

## The 5 integration bugs (so as not to repeat them)

Host validation (Python, `pymicro-features` + the same `.tflite`) gave
a 0.996 prob in the 4 test clips while the device gave 0 %. Causes, in
order of discovery:

1. **Resource variables**: the streaming model uses `VAR_HANDLE` ops; the
   `MicroInterpreter` needs an explicit `MicroResourceVariables::Create(...)`
   or it fails in `AllocateTensors()`.
2. **Stride contract**: `[1,2,40]` input = the model consumes **2 fresh
   frames per Invoke** (20 ms). Invoking with an overlapping sliding window
   advances the internal state at double the speed with duplicated data and kills
   the detection.
3. **Features scale ×10**: `pymicro-features` — what the training
   saw — exposes the microfrontend's uint16 **divided by 25.6, not
   by 256**. With ÷256 the features arrive ×10 smaller (flat spectrum) and
   the probability never rises. Numerically verified by compiling the C frontend
   on the host and comparing frame by frame.
4. **CNN state not reset between armings** (the worst to diagnose): the
   streaming model's resource variables survive `FrontendReset()`. Without
   `MicroResourceVariables::ResetAll()` in `mww_reset()`, the context of the
   last detection remains "hot": upon re-arming after closing the session, the
   model shoots up to ~99 % without any sound → infinite loop of
   detection→session→close→re-detection. The symptom for the user is "it
   answers me to any word" (the session is always open) and, when the
   state gets saturated in the other direction, "it never detects me". On the
   host it is not reproduced because there `reset_all_variables()` is called per clip.

5. **Aliasing in the 48k→16k decimation** (the most expensive to find): decimating
   by taking 1 out of every 3 samples without a filter folds the >8 kHz energy of the real
   voice (sibilants, room noise) over the useful spectrum → the model drops to
   ~0 % with live voice, while the **playback of recordings/TTS continues
   detecting at 97-99 %** (it comes limited to 8 kHz from the source: there is nothing to
   alias). That asymmetric pattern misleads towards gains/levels — all the
   training audio was resampled with a filter, the firmware was not. The
   discriminator that uncovered it: the same live voice through the session path
   (LiveKit's correct resampler) gave 1.00 on the host model. Fix: 19-tap low-pass
   FIR (6.8 kHz, Q15) before decimation, with history
   between chunks.

Furthermore: detection is based on the **moving average** of the probability window
(microWakeWord/ESPHome semantics), not "all above the threshold".

## Runtime diagnostics

The task logs the PCM peak and maximum probability every 5 s:

```
wakeword: 5s window: pcm peak=32365 max prob=4%
```

- Low `pcm peak` (<3000) with a nearby voice ⇒ audio/I2S channel problem.
- High `pcm peak` and 0 % `max prob` with the wake word ⇒ features/model problem.
- Correct detection ⇒ `WAKE WORD DETECTED` and session transition.

## Host validation (without flashing)

Reproduce the exact firmware pipeline in Python against any WAV:
model + quantization + 2 frame stride + moving average. Useful before touching
the device. The script is in the "Validation" section of `wakeword/README.md`.

## Limitations and next steps

- Session closing uses a simple VAD based on `mic_src.level()`: minimum of
  20 s, close after 12 s of local silence and safety maximum of 90 s.
  LINGER/DDSD will be the semantic version later.
- The 48k→16k decimation is 3:1 without an anti-aliasing filter. For wake word it is
  sufficient (the useful energy of the voice remains below 8 kHz); do not reuse
  for STT without adding a filter.
- The XVF can hang on the I2C bus after many consecutive reflashes (probe
  timeout on the whole bus) — it recovers with a physical USB power cycle.
