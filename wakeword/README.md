# Wake word "Sebastián"

Artefactos y scripts del wake word. La integración en firmware está documentada
en [`docs/WAKE_WORD.md`](../docs/WAKE_WORD.md).

## Contenido

| Fichero | Qué es |
|---|---|
| `sebastian.tflite` | Modelo final (62 KB, int8, streaming). Copia embebida en `firmware/main/`. |
| `sebastian.json` | Metadata del modelo (cutoff 0.62, ventana 4, step 10 ms, arena). |
| `download_es_voices.py` | Descarga las 9 voces Piper en español (AR/ES/MX) desde HuggingFace a `trainer/piper-sample-generator/voices/`. |
| `split_wakeword.py` | Trocea una sesión de grabación (WAV largo con muchas repeticiones) en clips individuales 16 kHz mono para `personal_samples/`. |
| `trainer/` | (gitignored, ~40 GB) Clon de microWakeWord-Trainer-AppleSilicon + datasets + venv. |

## Cómo se entrenó (y cómo re-entrenar)

Hardware: Apple Silicon (M4 Pro), TensorFlow con Metal/MPS. Duración total la
primera vez: horas (domina la descarga/conversión de ~20 GB de datasets
negativos; el entrenamiento en sí son ~30-45 min).

```bash
# 1. Clonar el trainer
git clone https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon trainer
cd trainer && ./run.sh   # crea .venv e instala dependencias

# 2. Voces españolas (el trainer solo trae inglés por CLI)
python ../download_es_voices.py

# 3. Grabaciones reales (opcional pero muy recomendable)
#    Grabar una sesión por el propio XVF con agent/record.py y trocear:
uv run ../../agent/record.py /tmp/session.wav 120
python ../split_wakeword.py /tmp/session.wav sebastian personal_samples/

# 4. Entrenar (18100 = nº de muestras TTS; 100 = épocas)
./train_microwakeword_macos.sh "Sebastián" 18100 100 --language es
# → trained_wake_words/sebastin.tflite
```

Detalles no obvios del proceso que hicimos:

- **`prepare_datasets.py` línea ~338**: bajamos los tars de AudioSet de
  `range(10)` a `range(3)` (26 GB → 8 GB) sin pérdida apreciable de calidad.
- Las grabaciones reales en `personal_samples/` las mezcla el trainer
  automáticamente con las TTS.

## Validación en host (sin flashear)

Pipeline idéntico al firmware (features pymicro + cuantización + stride 2 +
media móvil de 4) contra un WAV cualquiera:

```python
# cwd: wakeword/trainer — usa su .venv
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

**Ojo**: `pymicro-features` devuelve las features ya divididas entre **25.6**
(no 256). El firmware tiene que replicar exactamente eso — es el bug nº 3 de
`docs/WAKE_WORD.md`.

## Métricas del modelo entrenado (2026-07-02)

- Recall: **99.29 %** · Falsos positivos ambiente: **0.93/h**
- Cutoff 0.62, ventana deslizante 4 (media móvil)
- Positivos: 18 100 TTS (9 voces es_AR/es_ES/es_MX × 2 pronunciaciones) + 120 reales
- Negativos: AudioSet (3 tars) + FMA small + WHAM noise
