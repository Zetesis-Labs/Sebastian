> **Implementation report annex** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 parallel agents + cross-checking). Where this annex contradicts the **Frozen Decisions** of the main report, the main report prevails.

# wakeword-training — Wake word "Sebastián" (es) — microWakeWord training up to embeddable .tflite with the repo's trainer

**Verdict:** viable — the vendored trainer already covers the complete pipeline (TTS Piper → augmentation → MixedNet → FAPH calibration → .tflite int8 streaming + manifest v2); we only need to complete 2 noise datasets, regenerate positives with the correct target and inject adversarial negatives, for which the mechanism already exists (negative_samples/).

**Effort:** M — 3 to 5 person-days: 0.5 d audit+adversaries, 1 d first full training (mostly unattended), 1–1.5 d evaluation (10 h corpus + live recall), 1–2 d for 1–2 hard-negative/weight adjustment iterations.

## Findings
- The end-to-end pipeline is already set up: train_microwakeword_macos.sh does TTS → augmented features → MixedNet training (40k steps, lr 0.001, batch 128, negative_class_weight 20) → streaming int8 quantized tflite export → cutoff/window calibration → .tflite+.json package  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh:539-628_
- 9 Piper es voices are already downloaded (es_ES: carlfm, davefx, mls_10246, mls_9972, sharvard; es_MX: ald×2, claude; es_AR: daniela) — with --language es the script automatically uses all of them, with length_scales 0.85–1.15  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/piper-sample-generator/voices/ and train_microwakeword_macos.sh:276-289,423_
- Datasets: fma_16k=7997 wavs, audioset_16k=6000, mit_rirs=270 RIRs ready; wham_16k is EMPTY (1.6 GB zip downloaded without converting) and chime_16k does not exist — make_features.py requires them and would fail; prepare_datasets.py (step C of the script) completes them alone  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/make_features.py:25-31_
- generated_samples has 18100 TTS wavs with cache stamp ':manual' — since they do not match MAX_TTS_SAMPLES the script deletes and regenerates them; they are not reusable as is  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/generated_samples/.cache_stamp and train_microwakeword_macos.sh:405-419_
- personal_samples: 120 real clips (39 sebastian_*, 42 mix_*) that ALL enter as positives with sampling_weight 3.0; they must be audited before training  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/write_training_yaml.py:32-34_
- The mechanism for adversarial negatives already exists: any wav in negative_samples/ becomes reviewed_negative_features with truth=False, sampling_weight 8.0 and penalty_weight 1.25 — it's enough to generate the confusable phrases there with Piper  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/write_training_yaml.py:37-40 and make_features.py:64-76_
- calibrate_detector.py automatically chooses probability_cutoff and sliding_window_size on validation with FAPH target configurable via MWW_CALIBRATION_TARGET_FAPH (default 1.0); the v2 manifest comes out with feature_step_size=10 and tensor_arena_size=30000  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/scripts_macos/calibrate_detector.py:22 and train_microwakeword_macos.sh:594-627_
- Current community recommendations: 25k–50k TTS positives, typical cutoff 0.80–0.90 according to the table at the end of the training, negative_class_weight is the main lever against false accepts, and confusable hard-negatives give measurable improvement (reference model: 0.103 FA/h with 97.58% recall using 13 confusable phrases)  
  _https://github.com/malonestar/custom-micro-wake-word-model_
- 'Sebastián' /se.βas.ˈtjan/: 3 syllables with final stress — at the recommended low limit, but double fricative /s/ + plosive /t/ + final nasal give it discriminable structure. As the streaming model detects the pattern as an audio suffix, 'oye sebastián' triggers the SAME model: no need for a second model, just verify recall with prefix  
  _https://github.com/kahrendt/microWakeWord (streaming inference)_
- The safe name of the package removes the tilde: TARGET_WORD='sebastián' produces trained_wake_words/sebastin.tflite + sebastin.json (regex [^a-z0-9_]); harmless but good to know for the firmware  
  _/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh:582_
- The Zig firmware does not consume the model yet (it is the P0 spike of the ROADMAP: microWakeWord + LiveKit coexisting on the S3); the format to integrate is the streaming stream_state_internal_quant.tflite (int8, ~50–200 KB) + manifest JSON v2, with esp-tflite-micro and micro_speech frontend (10 ms features)  
  _/Users/ruben/Developer/Sebastian/ROADMAP.md:136-140_

## Design

## "Sebastián" Pipeline — executable with what's already in `wakeword/trainer/`

### 0. Prior audit (30 min)
1. Listen and clean `personal_samples/`: the 42 `mix_*` enter as POSITIVES (weight 3.0). If any clip is not a clear "Sebastián", move it out (or to `negative_samples/` if it is a real confusable).
2. Accept that `generated_samples/` (18100 wavs, stale `:manual` cache) will be regenerated. Free disk space if necessary (~50 GB free recommended: 50k wavs + mmap features).
3. Version `download_es_voices.py` and `split_wakeword.py` (point 6 of the ROADMAP) — the rest of `wakeword/` remains gitignored.

### 1. Adversarial negatives (new, ~1 h generation)
There is no step in the script for this; they are injected via `negative_samples/` (already supported: truth=False, sampling 8.0, penalty 1.25). Generate with Piper 300–400 clips per phrase distributed among the 9 es voices (see snippet 1).

**Adversarial es list** (phonetic confusables of /se.βas.ˈtjan/):
`sebastiana`, `sebastianes`, `san sebastián`, `bastián`, `bastión`, `un bastión`, `sebas`, `el sebas`, `sebo`, `se bastan`, `se bastan solos`, `es bastante`, `está bastante bien`, `la estación`, `secuestran`
(≈15 phrases × 350 ≈ 5,000 clips; the trainer augments them same as positives).

### 2. Positives and real recordings
- TTS: `TARGET_WORD="sebastián"` **with tilde** (Piper phonemizes with espeak-ng es; the tilde guarantees acute stress). 50,000 samples (25,000 if disk is tight), 9 voices × 5 length_scales — the script already does this.
- DO NOT train a separate model for "oye sebastián": the streaming model triggers with the suffix. It is only measured in evaluation; if recall with prefix lags, v2 with 20% "oye sebastián" positives (manual generation + manual `.cache_key`, pattern already used).
- Real: expand `personal_samples/` to ≥3 speakers × 30 takes at 1 m and 3 m. Flow: `./run.sh` → UI at :8789 (`/api/start_session`, `/api/upload_take`) or record long session and chop with `python split_wakeword.py sesion.wav sebastian`.

### 3. Training (one command; ~8–14 h total on M-series)
See snippet 2. The script chains: TTS (2–4 h) → `prepare_datasets.py` completes `wham_16k` and downloads `chime_16k` (archive.org, might take a while) → mmap features (2–5 h) → 40k steps MixedNet Metal (3–6 h) → int8 streaming export → `calibrate_detector.py` (set `MWW_CALIBRATION_TARGET_FAPH=0.5`) → package.
- Parameters already correct in the script (official notebook defaults + stride 2): do not touch in v1.
- If the final table shows high FAPH: raise `negative_class_weight` 20→25-30 in `write_training_yaml.py` and relaunch (features cached → only retrains, ~4 h).
- Repo's augmentation SNR is 5–10 dB (conservative); if recall with noise lags, lower `background_min_snr_db` to -5 (like the official notebook) in `make_features.py:92` — invalidates features cache.

### 4. Evaluation and acceptance criteria
FAPH without DipCo-es:
- **Automatic**: the final training report already gives FAPH on `dinner_party_eval` (English, serves as a proxy for overlapping speech). Criterion: **< 0.5 FA/h**.
- **Homebrew es corpus**: ≥10 h of Spanish TV/podcast (RTVE news, radio talk show, series with dense dialogue) played through speaker at ~1 m from the ReSpeaker with the detector running. Criterion: **≤ 0.5 activations/h** (≤5 in 10 h). Add 2 h of domestic noise without speech (kitchen, music): **0 activations**.
- **Live adversarial**: each phrase of the list spoken 10× at 2 m: **≤1 total trigger in the ~150 utterances**.

Recall (live, real hardware with XVF ASR beam):
- 3 speakers × 20 invocations per condition: **≥95% @1 m silence, ≥90% @3 m silence, ≥80% @3 m with TV at conversational volume, ≥70% @5 m silence**; "oye sebastián" **≥85% @3 m**.
- Improvement loop: false accepts from the device are uploaded via UI (`/api/upload_captured_audio` → `mark_negative`) → `negative_samples/` → retrain. 1–2 iterations expected.

### 5. Final artifact for the firmware
`trained_wake_words/sebastin.tflite` (streaming int8 `stream_state_internal_quant`, ~50–200 KB) + `sebastin.json` manifest v2 (snippet 3). Consumption on the S3 (P0 spike of the ROADMAP): esp-tflite-micro + micro_speech frontend generating features every `feature_step_size`=10 ms over the 16 kHz already available in `mic_src.zig`; arena ≈30 KB (test in internal SRAM; octal PSRAM as fallback); trigger = moving average of `sliding_window_size` probabilities > `probability_cutoff`.

### Summarized execution order
```
cd wakeword && python download_es_voices.py            # idempotent, already done
cd trainer && <snippet 1>                              # adversaries → negative_samples/
./run.sh                                               # (optional) UI for personal takes
MWW_LANGUAGE=es MWW_CALIBRATION_TARGET_FAPH=0.5 \
  ./train_microwakeword_macos.sh "sebastián" 50000 100 --language es
# evaluate → mark_negative → retrain if FAPH/recall out of criteria
```

## Code
**/Users/ruben/Developer/Sebastian/wakeword/trainer/ (cwd)** — Snippet 1 — generate adversarial es negatives with the repo's Piper voices towards negative_samples/ (reviewed-negatives mechanism already supported by make_features.py and write_training_yaml.py)

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

**/Users/ruben/Developer/Sebastian/wakeword/trainer/train_microwakeword_macos.sh** — Snippet 2 — full training order (once personal_samples are audited and negative_samples populated); the script chains TTS, datasets, features, MixedNet training, calibration and packaging

```bash
cd /Users/ruben/Developer/Sebastian/wakeword/trainer
# pre-complete heavy datasets separately (wham_16k empty, chime_16k absent)
.venv/bin/python scripts_macos/prepare_datasets.py

MWW_LANGUAGE=es \
MWW_CALIBRATION_TARGET_FAPH=0.5 \
./train_microwakeword_macos.sh "sebastián" 50000 100 --language es
# output: trained_wake_words/sebastin.tflite + sebastin.json
# if FAPH high: negative_class_weight 20→25-30 in scripts_macos/write_training_yaml.py and relaunch
```

**trained_wake_words/sebastin.json (generated)** — Snippet 3 — JSON v2 manifest produced by the trainer and to be consumed by the firmware (cutoff/window values replaced by calibrated ones; identical format to ESPHome micro_wake_word)

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

## Risks
- **The 42 'mix_*' clips from personal_samples enter as positives with weight 3.0; if they contain anything other than 'Sebastián' they poison recall/precision** → Prior listening audit (step 0); move doubtful ones out or to negative_samples/
- **Fragile dataset downloads in step C (archive.org's chime_home.tar.gz is slow; WHAM S3 bucket might expire) — training aborts if make_features doesn't find wham_16k/chime_16k** → Launch 'python scripts_macos/prepare_datasets.py' separately before training and verify wavs in wham_16k/ and chime_16k/; if WHAM fails, there is a mirror on Hugging Face
- **Pinned ML stack (tensorflow-macos 2.16.2 + Metal + Python 3.11) breaks with brew/pip drift — the script hard-fails by design** → Do not update the .venv; upon drift, rm -rf .venv and relaunch (script reinstalls pinned)
- **Real FAPH in Spanish worse than training metric (kahrendt's precomputed negatives are English speech): unseen close es names/words** → Adversarial es list + homebrew 10 h es TV/podcast corpus as acceptance gate + mark_negative→retrain trainer loop
- **'Sebastián' is a common name: TV/conversations mentioning it will trigger legitimately (not a model failure)** → Mitigate in ROADMAP state machine (ARMED/ATTENDING + re-verification in agent with pre-roll), not by lowering model recall
- **The first training might result in a very high calibrated cutoff (>0.95) and poor recall at 3-5 m** → Use cutoff/FAPH table at the end of training to choose the 0.80–0.90 point; if it's not enough, raise negative_class_weight and retrain instead of forcing the cutoff
- **Unprecedented Zig firmware integration (tflite-micro + LiveKit on same S3) — risk already identified as P0 spike in ROADMAP, out of scope for this mission** → Validate spike with official okay_nabu model before having our own model; output format is identical

## Open questions
- What exactly do the 'mix_*' clips in personal_samples contain?
- What text/voices were used to generate the current 18100 generated_samples? If they were 'sebastián' with the 9 es voices, their reuse can be forced by writing .cache_key, saving 2-4 h
- Does espeak-ng phonemize 'sebastian' without tilde with the correct stress in all voices? (recommended to pass 'sebastián' with tilde; verify by listening to 10 samples before the long run)
- Does the S3 CPU/SRAM budget with active LiveKit allow 30 KB arena + features frontend, or does the arena need to be moved to PSRAM? (P0 spike)

## Sources
- https://github.com/kahrendt/microWakeWord
- https://raw.githubusercontent.com/OHF-Voice/micro-wake-word/main/notebooks/basic_training_notebook.ipynb
- https://esphome.io/components/micro_wake_word/
- https://github.com/malonestar/custom-micro-wake-word-model
- https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon
- https://github.com/esphome/micro-wake-word-models
- https://www.kevinahrendt.com/micro-wake-word
- https://microwakeword.com/
- https://www.home-assistant.io/voice_control/create_wake_word/