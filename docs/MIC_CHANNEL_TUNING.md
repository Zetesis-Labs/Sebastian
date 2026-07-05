# Mic Channel Tuning (LEFT vs RIGHT) and Level

Conclusions from A/B testing the two output channels of the XVF3800, done by recording the voice published to LiveKit at 48 kHz and evaluating it by metrics + listening.

## TL;DR

- **Default channel: `RIGHT` (ASR).** This is the choice for STT.
- **Gain per channel:** `SHIFT = 14` on RIGHT, `SHIFT = 15` on LEFT.
- **Soft-clip limiter** in `mic_src.zig` to tame peaks without hard clipping.
- The channel is chosen at install time in `firmware/main/config.zig` (`mic_channel`).

## The two XVF3800 channels

| | RIGHT (ASR) | LEFT (comms) |
|---|---|---|
| Origin | beam from the beamformer, **skips post-processing** | **fully processed** output |
| Processing | post-AEC, **without NS or residual echo suppression** | de-reverb + NS + residual echo suppression + limiter |
| Intended for | recognition engines (ASR/STT) | human ear (full-duplex calls) |
| Limiter in XVF | **no** | yes |

## Methodology

- Pure recorder `agent/record.py` (no agent nor OpenAI → free): joins the room, subscribes to the mic track and dumps it to a mono 48 kHz WAV.
- Same phrase, same volume/distance in each take.
- Analysis: RMS, peak, % clipping, SNR, noise floor, % of samples in the limiter zone. Blind listening with takes **normalized to the same peak** (to judge quality without volume bias).

## Measured Results (final takes, with limiter)

| Metric | RIGHT @ SHIFT=14 | LEFT @ SHIFT=15 |
|---|---|---|
| voice (RMS p80) | 7534 | 8052 |
| noise floor (RMS p20) | ~250 | ~96 |
| SNR | 30.7 dB | 38.5 dB |
| limiter engaging (≥30k) | 0.17 % | 0.20 % |
| hard clipping | ~0 (residual = Opus overshoot) | ~0 |

Listening (final judge = the ear):
- **RIGHT: more natural.** At normal distance, clean.
- **LEFT: sounds more "tinny"/processed** — it's the character of the XVF's NS/comms, intrinsic to the channel; it doesn't go away by lowering the level.

## Decisions and why

### Channel for STT → RIGHT

1. It is the **ASR channel by XMOS design**: it is tapped before post-processing because recognition engines prefer natural and minimally processed speech.
2. LEFT's processing (aggressive NS, AGC) **erases spectral cues** (formants, transitions) that STT uses → "cleaner for the ear" ≠ "better for STT".
3. Modern STTs (Whisper, OpenAI Realtime) are robust to noise → RIGHT's slightly higher floor is not annoying.
4. Avoids **double processing** (XVF's NS chained with the engine's → sounded tinny).
5. With **push-to-talk** (mic closed while agent speaks) we don't need LEFT's echo suppression, which was its only real advantage here.

LEFT would be the choice if the destination were a **human ear** (call), not a machine that transcribes.

### Level (SHIFT) per channel

The dynamic range of the voice depending on distance is wide and **we don't have our own AGC**, so the fixed `SHIFT` is a compromise:

- **RIGHT = 14.** At 13 it clipped (metallic); at 14 it sits well.
- **LEFT = 15.** It's a hotter channel (has AGC); at 14 it crushed the limiter (1.18 % of compressed samples → coloration) and was twice the level of RIGHT. At 15 it matches RIGHT (~8000) and the limiter just acts as a safety net (0.20 %).

### Soft Limiter (`softClip` in `mic_src.zig`)

Below the knee (24000) the sample passes linearly; above, peaks are softly compressed with `tanh` towards (without reaching) full scale, instead of hard clipping. Hard clipping is what sounds metallic on loud syllables.

**RIGHT Caveat:** at point-blank range it still clips **inside the XVF** (the ASR beam has no origin limiter), and that cannot be undone downstream. At normal distance from a room speaker this doesn't happen. LEFT doesn't have this problem (XVF's own limiter).

## Installation Configuration

`firmware/main/config.zig`:

```zig
pub const mic_channel: MicChannel = .right; // or .left
```

It is resolved at comptime (zero cost) and sets the I2S slot **and** the `SHIFT` per channel. Change the line and reflash to install a unit with the other channel.

## Side note: Center mute LED

Investigated in this session: the center LED **is not controllable from the host**. On each press only `gpo1` changes (=mute/GPIO30); writing GPIO30 mutes the mic but does not move the LED. The `RESID_LED` (0x0C) resource is not a functional servicer in the `inthost 1.0.7` firmware (it returns uniform status echoing the command). The LED is handled by the XVF internal firmware (button handler) or is latched in hardware. Touching it would require recompiling the XVF firmware config.
