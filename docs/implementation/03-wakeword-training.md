> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# wakeword-training — Wake word "Sebastián" (es) — entrenamiento microWakeWord hasta .tflite embebible con el trainer del repo

**Veredicto:** viable — el trainer vendorizado ya cubre el pipeline completo (TTS Piper → augmentation → MixedNet → calibración FAPH → .tflite int8 streaming + manifest v2); solo faltan completar 2 datasets de ruido, regenerar positivos con el target correcto e inyectar negativos adversariales, para lo cual ya existe el mecanismo (negative_samples/).

**Esfuerzo:** M — 3 a 5 días de una persona: 0.5 d auditoría+adversarios, 1 d primer entrenamiento completo (mayormente desatendido), 1–1.5 d evaluación (corpus 10 h + recall en vivo), 1–2 d para 1–2 iteraciones hard-negative/ajuste de pesos.

## Hallazgos
- El pipeline end-to-end ya está montado: train_microwakeword_macos.sh hace TTS → features aumentadas → entrenamiento MixedNet (40k steps, lr 0.001, batch 128, negative_class_weight 20) → export tflite streaming int8 cuantizado → calibración cutoff/window → paquete .tflite+.json  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh:539-628_
- 9 voces Piper es ya descargadas (es_ES: carlfm, davefx, mls_10246, mls_9972, sharvard; es_MX: ald×2, claude; es_AR: daniela) — con --language es el script las usa todas automáticamente, con length_scales 0.85–1.15  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/piper-sample-generator/voices/ y train_microwakeword_macos.sh:276-289,423_
- Datasets: fma_16k=7997 wavs, audioset_16k=6000, mit_rirs=270 RIRs listos; wham_16k está VACÍO (zip de 1.6 GB descargado sin convertir) y chime_16k no existe — make_features.py los exige y fallaría; prepare_datasets.py (paso C del script) los completa solo  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/make_features.py:25-31_
- generated_samples tiene 18100 wavs de TTS con cache stamp ':manual' — al no coincidir con MAX_TTS_SAMPLES el script los borra y regenera; no son reutilizables tal cual  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/generated_samples/.cache_stamp y train_microwakeword_macos.sh:405-419_
- personal_samples: 120 clips reales (39 sebastian_*, 42 mix_*) que entran TODOS como positivos con sampling_weight 3.0; hay que auditarlos antes de entrenar  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/write_training_yaml.py:32-34_
- El mecanismo para negativos adversariales ya existe: cualquier wav en negative_samples/ se convierte en reviewed_negative_features con truth=False, sampling_weight 8.0 y penalty_weight 1.25 — basta generar ahí las frases confusables con Piper  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/write_training_yaml.py:37-40 y make_features.py:64-76_
- calibrate_detector.py elige probability_cutoff y sliding_window_size automáticamente sobre validación con target FAPH configurable vía MWW_CALIBRATION_TARGET_FAPH (default 1.0); el manifest v2 sale con feature_step_size=10 y tensor_arena_size=30000  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/calibrate_detector.py:22 y train_microwakeword_macos.sh:594-627_
- Recomendaciones actuales de la comunidad: 25k–50k positivos TTS, cutoff típico 0.80–0.90 según la tabla al final del training, negative_class_weight es la palanca principal contra falsos accepts, y hard-negatives confusables dan mejora medible (modelo de referencia: 0.103 FA/h con 97.58% recall usando 13 frases confusables)  
  _https://github.com/malonestar/custom-micro-wake-word-model_
- 'Sebastián' /se.βas.ˈtjan/: 3 sílabas con acento final — en el límite bajo recomendado, pero doble /s/ fricativa + oclusiva /t/ + nasal final le dan estructura discriminable. Como el modelo streaming detecta el patrón como sufijo del audio, 'oye sebastián' dispara el MISMO modelo: no hace falta un segundo modelo, solo verificar recall con prefijo  
  _https://github.com/kahrendt/microWakeWord (streaming inference)_
- El nombre safe del paquete elimina la tilde: TARGET_WORD='sebastián' produce trained_wake_words/sebastin.tflite + sebastin.json (regex [^a-z0-9_]); inocuo pero conviene saberlo para el firmware  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh:582_
- El firmware Zig aún no consume el modelo (es el spike P0 del ROADMAP: microWakeWord + LiveKit conviviendo en el S3); el formato a integrar es el streaming stream_state_internal_quant.tflite (int8, ~50–200 KB) + manifest JSON v2, con esp-tflite-micro y frontend micro_speech (features de 10 ms)  
  _/Users/ruben/Developer/Sebastian/ROADMAP.md:136-140_

## Diseño

## Pipeline "Sebastián" — ejecutable con lo que ya hay en `wakeword/trainer/`

### 0. Auditoría previa (30 min)
1. Escuchar y limpiar `personal_samples/`: los 42 `mix_*` entran como POSITIVOS (peso 3.0). Si algún clip no es "Sebastián" claro, moverlo fuera (o a `negative_samples/` si es confusable real).
2. Aceptar que `generated_samples/` (18100 wavs, caché `:manual` stale) se regenerará. Liberar disco si hace falta (~50 GB libres recomendados: 50k wavs + features mmap).
3. Versionar `download_es_voices.py` y `split_wakeword.py` (punto 6 del ROADMAP) — el resto de `wakeword/` sigue gitignoreado.

### 1. Negativos adversariales (nuevo, ~1 h de generación)
No hay paso en el script para esto; se inyectan por `negative_samples/` (ya soportado: truth=False, sampling 8.0, penalty 1.25). Generar con Piper 300–400 clips por frase repartidos entre las 9 voces es (ver snippet 1).

**Lista adversarial es** (confusables fonéticos de /se.βas.ˈtjan/):
`sebastiana`, `sebastianes`, `san sebastián`, `bastián`, `bastión`, `un bastión`, `sebas`, `el sebas`, `sebo`, `se bastan`, `se bastan solos`, `es bastante`, `está bastante bien`, `la estación`, `secuestran`
(≈15 frases × 350 ≈ 5.000 clips; el trainer las augmenta igual que los positivos).

### 2. Positivos y grabaciones reales
- TTS: `TARGET_WORD="sebastián"` **con tilde** (Piper fonemiza con espeak-ng es; la tilde garantiza acento agudo). 50.000 muestras (25.000 si el disco aprieta), 9 voces × 5 length_scales — ya lo hace el script.
- NO entrenar modelo aparte para "oye sebastián": el modelo streaming dispara con el sufijo. Solo se mide en evaluación; si el recall con prefijo flojea, v2 con 20% de positivos "oye sebastián" (generación manual + `.cache_key` manual, patrón ya usado).
- Reales: ampliar `personal_samples/` a ≥3 hablantes × 30 tomas a 1 m y 3 m. Flujo: `./run.sh` → UI en :8789 (`/api/start_session`, `/api/upload_take`) o grabar sesión larga y trocear con `python split_wakeword.py sesion.wav sebastian`.

### 3. Entrenamiento (una orden; ~8–14 h total en M-series)
Ver snippet 2. El script encadena: TTS (2–4 h) → `prepare_datasets.py` completa `wham_16k` y descarga `chime_16k` (archive.org, puede tardar) → features mmap (2–5 h) → 40k steps MixedNet Metal (3–6 h) → export int8 streaming → `calibrate_detector.py` (poner `MWW_CALIBRATION_TARGET_FAPH=0.5`) → paquete.
- Parámetros ya correctos en el script (defaults del notebook oficial + stride 2): no tocar en v1.
- Si la tabla final muestra FAPH alto: subir `negative_class_weight` 20→25-30 en `write_training_yaml.py` y relanzar (features cacheadas → solo re-entrena, ~4 h).
- SNR de augmentation del repo es 5–10 dB (conservador); si el recall con ruido flojea, bajar `background_min_snr_db` a -5 (como el notebook oficial) en `make_features.py:92` — invalida caché de features.

### 4. Evaluación y criterios de aceptación
FAPH sin DipCo-es:
- **Automático**: el reporte final del training ya da FAPH sobre `dinner_party_eval` (inglés, sirve de proxy de habla solapada). Criterio: **< 0.5 FA/h**.
- **Corpus casero es**: ≥10 h de TV/podcast en español (informativos RTVE, tertulia radio, serie con diálogo denso) reproducidos por altavoz a ~1 m del ReSpeaker con el detector corriendo. Criterio: **≤ 0.5 activaciones/h** (≤5 en 10 h). Añadir 2 h de ruido doméstico sin habla (cocina, música): **0 activaciones**.
- **Adversarial en vivo**: cada frase de la lista dicha 10× a 2 m: **≤1 disparo total en las ~150 locuciones**.

Recall (en vivo, hardware real con beam ASR del XVF):
- 3 hablantes × 20 invocaciones por condición: **≥95% @1 m silencio, ≥90% @3 m silencio, ≥80% @3 m con TV a volumen conversacional, ≥70% @5 m silencio**; "oye sebastián" **≥85% @3 m**.
- Bucle de mejora: los falsos del dispositivo se suben vía UI (`/api/upload_captured_audio` → `mark_negative`) → `negative_samples/` → reentrenar. 1–2 iteraciones esperables.

### 5. Artefacto final para el firmware
`trained_wake_words/sebastin.tflite` (streaming int8 `stream_state_internal_quant`, ~50–200 KB) + `sebastin.json` manifest v2 (snippet 3). Consumo en el S3 (spike P0 del ROADMAP): esp-tflite-micro + frontend micro_speech generando features cada `feature_step_size`=10 ms sobre los 16 kHz ya disponibles en `mic_src.zig`; arena ≈30 KB (probar en SRAM interna; PSRAM octal como fallback); disparo = media móvil de `sliding_window_size` probabilidades > `probability_cutoff`.

### Orden de ejecución resumido
```
cd wakeword && python download_es_voices.py            # idempotente, ya hecho
cd trainer && <snippet 1>                              # adversarios → negative_samples/
./run.sh                                               # (opcional) UI para tomas personales
MWW_LANGUAGE=es MWW_CALIBRATION_TARGET_FAPH=0.5 \
  ./train_microwakeword_macos.sh "sebastián" 50000 100 --language es
# evaluar → mark_negative → reentrenar si FAPH/recall fuera de criterio
```

## Código
**/Users/ruben/Developer/Sebastian/wakeword/trainer/ (cwd)** — Snippet 1 — generar negativos adversariales es con las voces Piper del repo hacia negative_samples/ (mecanismo reviewed-negatives ya soportado por make_features.py y write_training_yaml.py)

```bash
cd /Users/ruben/Developer/Sebastian/wakeword/trainer
source .venv/bin/activate
mkdir -p negative_samples
PHRASES=("sebastiana" "sebastianes" "san sebastián" "bastián" "bastión" \
  "un bastión" "sebas" "el sebas" "sebo" "se bastan" "se bastan solos" \
  "es bastante" "está bastante bien" "la estación" "secuestran")
for p in "${PHRASES[@]}"; do
  slug=$(echo "$p" | tr ' áéíóú' '_aeiou')
  tmp="negative_raw/$slug"; mkdir -p "$tmp"
  MODELS=(); for v in piper-sample-generator/voices/es_*.onnx; do MODELS+=(--model "$v"); done
  python piper-sample-generator/generate_samples.py "$p" \
    --max-samples 350 --batch-size 50 --output-dir "$tmp" \
    --length-scales 0.9 1.0 1.1 "${MODELS[@]}"
  i=0; for f in "$tmp"/*.wav; do cp "$f" "negative_samples/${slug}_$((i++)).wav"; done
done
```

**/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh** — Snippet 2 — orden de entrenamiento completa (una vez auditados personal_samples y poblado negative_samples); el script encadena TTS, datasets, features, training MixedNet, calibración y empaquetado

```bash
cd /Users/ruben/Developer/Sebastian/wakeword/trainer
# pre-completar datasets pesados por separado (wham_16k vacío, chime_16k ausente)
.venv/bin/python scripts_macos/prepare_datasets.py

MWW_LANGUAGE=es \
MWW_CALIBRATION_TARGET_FAPH=0.5 \
./train_microwakeword_macos.sh "sebastián" 50000 100 --language es
# salida: trained_wake_words/sebastin.tflite + sebastin.json
# si FAPH alto: negative_class_weight 20→25-30 en scripts_macos/write_training_yaml.py y relanzar
```

**trained_wake_words/sebastin.json (generado)** — Snippet 3 — manifest JSON v2 que produce el trainer y que consumirá el firmware (valores cutoff/window sustituidos por los calibrados; formato idéntico al de ESPHome micro_wake_word)

```json
{
  "type": "micro",
  "wake_word": "sebastián",
  "model": "sebastin.tflite",
  "trained_languages": ["es"],
  "version": 2,
  "micro": {
    "probability_cutoff": 0.85,
    "sliding_window_size": 5,
    "feature_step_size": 10,
    "tensor_arena_size": 30000,
    "minimum_esphome_version": "2024.7.0"
  }
}
```

## Riesgos
- **Los 42 clips 'mix_*' de personal_samples entran como positivos con peso 3.0; si contienen otra cosa que 'Sebastián' envenenan el recall/precisión** → Auditoría de escucha previa (paso 0); mover lo dudoso fuera o a negative_samples/
- **Descargas de datasets frágiles en el paso C (chime_home.tar.gz de archive.org es lento; el bucket S3 de WHAM puede caducar) — el training aborta si make_features no encuentra wham_16k/chime_16k** → Lanzar 'python scripts_macos/prepare_datasets.py' por separado antes del training y verificar wavs en wham_16k/ y chime_16k/; si WHAM falla, existe mirror en Hugging Face
- **Stack ML pinado (tensorflow-macos 2.16.2 + Metal + Python 3.11) se rompe con drift de brew/pip — el script hace hard-fail por diseño** → No actualizar el .venv; ante drift, rm -rf .venv y relanzar (el script reinstala pinado)
- **FAPH real en español peor que la métrica del training (negativos precomputados de kahrendt son de habla inglesa): nombres/palabras es cercanas no vistas** → Lista adversarial es + corpus casero de 10 h de TV/podcast es como gate de aceptación + bucle mark_negative→retrain del trainer
- **'Sebastián' es nombre común: la TV/las conversaciones que lo mencionen dispararán legítimamente (no es un fallo del modelo)** → Mitigar en la máquina de estados del ROADMAP (ARMED/ATTENDING + re-verificación en el agente con el pre-roll), no bajando el recall del modelo
- **El primer entrenamiento puede salir con cutoff calibrado muy alto (>0.95) y recall pobre a 3-5 m** → Usar la tabla cutoff/FAPH del final del training para elegir el punto 0.80–0.90; si no alcanza, subir negative_class_weight y reentrenar en vez de forzar el cutoff
- **Integración en firmware Zig sin precedente (tflite-micro + LiveKit en el mismo S3) — riesgo ya identificado como spike P0 del ROADMAP, fuera del alcance de esta misión** → Validar el spike con el modelo okay_nabu oficial antes de tener el modelo propio; el formato de salida es idéntico

## Preguntas abiertas
- ¿Qué contienen exactamente los clips 'mix_*' de personal_samples?
- ¿Los 18100 generated_samples actuales se generaron con qué texto/voces? Si fueron 'sebastián' con las 9 voces es, se puede forzar su reutilización escribiendo .cache_key, ahorrando 2-4 h
- ¿espeak-ng fonemiza 'sebastian' sin tilde con el acento correcto en todas las voces? (recomendado pasar 'sebastián' con tilde; verificar escuchando 10 muestras antes del run largo)
- ¿El presupuesto de CPU/SRAM del S3 con LiveKit activo permite arena de 30 KB + frontend de features, o hay que mover la arena a PSRAM? (spike P0)

## Fuentes
- https://github.com/kahrendt/microWakeWord
- https://raw.githubusercontent.com/OHF-Voice/micro-wake-word/main/notebooks/basic_training_notebook.ipynb
- https://esphome.io/components/micro_wake_word/
- https://github.com/malonestar/custom-micro-wake-word-model
- https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon
- https://github.com/esphome/micro-wake-word-models
- https://www.kevinahrendt.com/micro-wake-word
- https://microwakeword.com/
- https://www.home-assistant.io/voice_control/create_wake_word/