# Wake word "Sebastián"

Wake word artifacts and scripts. Firmware integration is documented
in [`docs/WAKE_WORD.md`](../docs/WAKE_WORD.md).

## Contents

| File | What it is |
|---|---|
| `sebastian.tflite` | Final model (62 KB, int8, streaming). Embedded copy in `firmware/main/`. |
| `sebastian.json` | Model metadata (cutoff 0.62, window 4, step 10 ms, arena). |
| `download_es_voices.py` | Downloads the 9 Spanish Piper voices (AR/ES/MX) from HuggingFace to `trainer/piper-sample-generator/voices/`. |
| `split_wakeword.py` | Chops a recording session (long WAV with many repetitions) into individual 16 kHz mono clips for `personal_samples/`. |
| `trainer/` | (gitignored, ~40 GB) Clone of microWakeWord-Trainer-AppleSilicon + datasets + venv. |

## How it was trained (and how to retrain)

Hardware: Apple Silicon (M4 Pro), TensorFlow with Metal/MPS. Total duration the
first time: hours (dominated by the downloading/converting of ~20 GB of negative datasets;
the training itself takes ~30-45 min).

```bash
# 1. Clone the trainer
git clone https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon trainer
cd trainer && ./run.sh   # creates .venv and installs dependencies

# 2. Spanish voices (the trainer only brings English via CLI)
python ../download_es_voices.py

# 3. Real recordings (optional but highly recommended)
#    Record a session through the XVF itself with agent/record.py and split it:
uv run ../../agent/record.py /tmp/session.wav 120
python ../split_wakeword.py /tmp/session.wav sebastian personal_samples/

# 4. Train (18100 = num TTS samples; 100 = epochs)
./train_microwakeword_macos.sh "Sebastián" 18100 100 --language es
# → trained_wake_words/sebastin.tflite
```

Non-obvious details of the process we followed:

- **`prepare_datasets.py` line ~338**: we lowered the AudioSet tars from
  `range(10)` to `range(3)` (26 GB → 8 GB) with no noticeable loss of quality.
- The real recordings in `personal_samples/` are automatically mixed by the trainer
  with the TTS ones.

## Host validation (without flashing)

Identical pipeline to the firmware (pymicro features + quantization + stride 2 +
moving average of 4) against any WAV:

```python
# cwd: wakeword/trainer — uses its .venv
import numpy as np, tensorflow as tf, scipy.signal
from pymicro_features import MicroFrontend
from scipy.io import wavfile

interp = tf.lite.Interpreter('trained_wake_words/sebastian.tflite')
interp.allocate_tensors()
inp, out = interp.get_input_details()[0], interp.get_output_details()[0]

sr, audio = wavfile.read('personal_samples/sebastian_0000.wav')
if sr != 16000:
    audio = scipy.signal.resample_poly(audio, 16000, sr).astype(np.int16)
audio = np.concatenate([np.zeros(16000, np.int16), audio, np.zeros(16000, np.int16)])

mf = MicroFrontend()
data, feats, idx = audio.tobytes(), [], 0
while idx + 320 <= len(data):
    r = mf.process_samples(data[idx:idx+320])
    idx += r.samples_read * 2
    if r.features: feats.append(np.array(r.features, np.float32))

interp.reset_all_variables()
probs = []
for i in range(0, len(feats) - 1, 2):                      # stride: 2 frames/Invoke
    q = np.clip(np.round(np.stack(feats[i:i+2]) / 0.10196079) - 128, -128, 127)
    interp.set_tensor(inp['index'], q.astype(np.int8)[np.newaxis])
    interp.invoke()
    probs.append(interp.get_tensor(out['index'])[0][0] / 256.0)

ma = max(np.convolve(probs, np.ones(4)/4, 'valid'))
print('DETECT' if ma > 0.62 else 'no', f'(moving avg {ma:.3f})')
```

**Note**: `pymicro-features` returns the features already divided by **25.6**
(not 256). The firmware has to replicate exactly that — this is bug #3 in
`docs/WAKE_WORD.md`.

## Trained model metrics (2026-07-02)

- Recall: **99.29 %** · Environment false positives: **0.93/h**
- Cutoff 0.62, sliding window 4 (moving average)
- Positives: 18,100 TTS (9 es_AR/es_ES/es_MX voices × 2 pronunciations) + 120 real
- Negatives: AudioSet (3 tars) + FMA small + WHAM noise
