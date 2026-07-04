"""Split a recorded wake-word session into individual clips for personal_samples.

Detects non-silent segments (each spoken utterance), pads them, resamples to
16 kHz mono, and writes numbered clips. Feed it a session WAV where you said the
wake word many times with short pauses.

    python split_wakeword.py /tmp/session.wav sebastian
"""

import sys
from pathlib import Path

import librosa
import numpy as np
import soundfile as sf

SESSION = sys.argv[1]
LABEL = sys.argv[2] if len(sys.argv) > 2 else "clip"
OUT = Path(sys.argv[3]) if len(sys.argv) > 3 else Path("trainer/personal_samples")

TOP_DB = 30            # silence threshold (lower = stricter silence)
MIN_DUR = 0.35         # drop segments shorter than this (noise/clicks)
MAX_DUR = 2.0          # cap long segments
PAD = 0.15             # seconds of context added on each side


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    y, sr = librosa.load(SESSION, sr=16000, mono=True)   # resample to 16k mono
    intervals = librosa.effects.split(y, top_db=TOP_DB)
    pad = int(PAD * sr)
    kept = 0
    for start, end in intervals:
        dur = (end - start) / sr
        if dur < MIN_DUR:
            continue
        s = max(0, start - pad)
        e = min(len(y), end + pad)
        clip = y[s:e]
        if len(clip) > MAX_DUR * sr:
            clip = clip[: int(MAX_DUR * sr)]
        # normalize peak to a safe level so quiet takes aren't lost
        peak = np.max(np.abs(clip)) or 1.0
        clip = (clip / peak * 0.9).astype(np.float32)
        sf.write(OUT / f"{LABEL}_{kept:04d}.wav", clip, sr, subtype="PCM_16")
        kept += 1
    total_speech = sum((e - s) for s, e in intervals) / sr
    print(f"session: {len(y)/sr:.1f}s, {len(intervals)} segments, {total_speech:.1f}s speech")
    print(f"kept {kept} clips (>= {MIN_DUR}s) -> {OUT}")


if __name__ == "__main__":
    main()
