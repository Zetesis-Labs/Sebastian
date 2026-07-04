> **✅ RESUELTO 2026-07-02 — ver [`docs/AEC.md`](../AEC.md).** El Test 0 (mux far-end passthrough) confirmó la referencia viva; la causa raíz fue `AEC_FAR_EXTGAIN=0.0` de fábrica (AEC nunca adaptaba). Fix en boot con readback (`xvf_aec.applyConfig`), `AECCONVERGED=1` verificado, volumen software reconstruido (el noop set_vol lo anulaba, como este anexo ya señalaba).

> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# aec-verification — Verificación y cierre de la referencia AEC del XVF3800 (ROADMAP hallazgo #2: volumen capado a 35/100, barge-in no fiable)

**Veredicto:** viable con riesgos — La arquitectura está confirmada por doc oficial XMOS: la referencia far-end del AEC entra por el canal LEFT (slot 0) de la entrada I2S del XVF, es decir, por la misma línea GPIO44 que el ESP ya usa para el altavoz; y el protocolo de control expone métricas (AEC_AECCONVERGED) y routing (AUDIO_MGR_OP_*) suficientes para diagnosticar y cerrar el lazo. El riesgo restante es hardware: no existe esquemático público de la placa Seeed que confirme que GPIO44 llega eléctricamente al pin I2S-in del XVF además de al AIC3104 — pero el propio protocolo permite verificarlo en 10 minutos sin abrir la placa (mux far-end passthrough).

**Esfuerzo:** M — 2-3 días de una persona (0.5 d instrumentación I2C, 0.5 d playbook de medición, 1-2 d cierre: switch dinámico de mux + volumen AIC3104 + retune y validación de barge-in)

## Hallazgos
- CONFIRMADO (doc oficial XMOS): la referencia AEC es el canal LEFT (0) de la entrada I2S del XVF3800 — 'A far-end AEC reference signal must be provided on the left (0) channel of the I2S or USB input signal' — y en el diseño de referencia esa misma señal pasa al DAC ('the DAC is configured to play the left input channel on both the right and left outputs'). El audio que el ESP manda por GPIO44 ES la referencia, si la línea llega al XVF.  
  _https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/datasheet/03_audio_pipeline.html_
- El pipeline es AEC (lineal, adaptativo) → beamformer → post-procesado; la supresión de eco RESIDUAL/no-lineal (PP_ECHOONOFF, PP_NLATTENONOFF, PP_GAMMA_E*) vive SOLO en el post-procesado del canal comms (LEFT). El beam ASR que publica el firmware (slot RIGHT, mux categoría 7) es 'AEC residual / ASR data': post-AEC lineal pero sin supresión residual → explica el eco residual que obliga a capar el volumen a 35, aun con la referencia funcionando.  
  _Tabla 26 en https://www.xmos.com/documentation/XM-014888-PC/html/modules/fwk_xvf/doc/user_guide/03_using_the_host_application.html + firmware/main/config.zig:6-17_
- El protocolo de control expone todo lo necesario, con resid/cmd verificados contra el driver oficial de ReSpeaker (mismo esquema write/read que readBeamLed, que ya funciona en inthost 1.0.7 con resid 33/75): AEC_AECCONVERGED (33,3 ro int32), AEC_AECPATHCHANGE (33,0), AEC_NUM_FARENDS (33,72), AUDIO_MGR_REF_GAIN (35,1 rw float, default 1.5), AUDIO_MGR_SYS_DELAY (35,26 rw int32), AUDIO_MGR_OP_L/OP_R (35,15/35,19 rw 2×uint8 <categoría,fuente>), PP_ECHOONOFF (17,23), PP_GAMMA_E/ETAIL/ENL (17,24/25/26), PP_NLATTENONOFF (17,27), SHF_BYPASS (33,70). No hay comando ERLE directo: se sustituye con AECCONVERGED + medición A/B de dB.  
  _https://raw.githubusercontent.com/respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY/master/python_control/xvf_host.py (PARAMETERS, líneas 28-135)_
- No hay esquemático público de la placa (Seeed solo publica STEP 3D). La evidencia indirecta de que GPIO44 llega al XVF: el XVF es master del bus (genera BCLK/WS de esa línea), CNX/Seeed describen I2S bidireccional XVF↔ESP32 y XVF↔AIC3104, y el propio yaml oficial de Seeed comenta 'Try mono for XVF AEC' en el pipeline del altavoz. El mux categoría 4/5 ('Far end data received over I2S') permite verificarlo eléctricamente por software: rutar el far-end al slot que publicamos y comprobar si aparece el TTS.  
  _homeassistant/respeaker.yaml:1326 + https://wiki.seeedstudio.com/respeaker_xvf3800_introduction/_
- El ESP ya cumple el requisito de formato: av_render manda 48 kHz / 2 canales / 32-bit (frame.channel = 2, el mono del agente se duplica a ambos slots), así que el slot LEFT lleva señal. Punto débil actual: el 'volumen 35' se aplica vía esp_codec_dev_set_out_vol sobre un codec_if noop (noopSetVol no hace nada en hardware), y la ganancia real del AIC3104 está fijada a 0 dB en registros — el control de volumen real está difuso y hay que reconstruirlo para el 'volumen gradual + limitador'.  
  _firmware/main/app.zig:130-133 + firmware/main/board.zig:87-92,153-192_
- Para el barge-in con eco residual hay un switch dinámico por I2C sin reflashear: AUDIO_MGR_OP_R (35,19) permite conmutar el slot RIGHT entre (7,3) beam ASR crudo y (6,3) beam post-procesado con supresión de eco residual solo mientras el agente habla (~1 escritura I2C de 5 bytes). Complementos: AEC_ASROUTONOFF/GAIN (33,35/36) y PP_LIMITONOFF (17,19, solo canal comms).  
  _Tabla 26 XMOS + xvf_host.py líneas 79-84_

## Diseño

# Cierre de la referencia AEC del XVF3800

## 1. Arquitectura confirmada (cita primaria)

- XMOS (datasheet 3.2.1, audio pipeline): **"A far-end AEC reference signal must be
  provided on the left (0) channel of the I2S or USB input signal"**, y el DAC del
  diseño de referencia reproduce ese mismo canal de entrada. Es decir: **la línea que
  el host escribe (GPIO44) es a la vez señal de altavoz y referencia AEC**; el XVF
  (master) reloja esa línea y muestrea su slot LEFT como far-end.
- Pipeline: `AEC lineal → beamformer → post-proc`. La **supresión de eco residual/no
  lineal solo existe en el post-proc del canal comms** (`PP_ECHOONOFF`, `PP_NLATTENONOFF`,
  `PP_GAMMA_E/ETAIL/ENL`). El slot RIGHT que publicamos (mux cat. 7 = "AEC residual /
  ASR data") es post-AEC **sin** esa etapa → el eco residual a volumen alto es esperado
  incluso con referencia viva.
- `app.zig` ya manda 48k/estéreo/32-bit (mono duplicado) → el slot LEFT lleva el TTS. ✔
- Único hueco: no hay esquemático Seeed que confirme que GPIO44 entra al pin I2S-in del
  XVF (además de al AIC3104). Se verifica por software (test 0).

## 2. Comandos I2C (añadir a xvf_dfu.zig, mismo patrón que readBeamLed)

| Nombre | resid | cmd | tipo | r/w | Uso |
|---|---|---|---|---|---|
| AEC_AECCONVERGED | 33 | 3 | int32 | ro | **métrica clave**: 1 = filtro convergido |
| AEC_AECPATHCHANGE | 33 | 0 | int32 | ro | detección de cambio de camino acústico |
| AEC_NUM_FARENDS | 33 | 72 | int32 | ro | debe leer ≥1 (hay entrada far-end) |
| AEC_RT60 | 33 | 9 | float | ro | negativo = estimación inválida (pista de ref. muerta) |
| SHF_BYPASS | 33 | 70 | uint8 | rw | bypass del AEC (para medir E_sin_aec) |
| AEC_ASROUTONOFF / GAIN | 33 | 35/36 | int32/float | rw | modo y ganancia del beam ASR |
| AUDIO_MGR_REF_GAIN | 35 | 1 | float | rw | pre-ganancia de la referencia (def. 1.5) |
| AUDIO_MGR_SYS_DELAY | 35 | 26 | int32 | rw | alineado ref↔mic (-64..256 muestras) |
| AUDIO_MGR_OP_L / OP_R | 35 | 15/19 | 2×uint8 | rw | mux `<categoría,fuente>` por slot |
| PP_ECHOONOFF | 17 | 23 | int32 | rw | supresión eco residual (solo comms) |
| PP_GAMMA_E / ETAIL / ENL | 17 | 24/25/26 | float | rw | agresividad supresión (0-2 / 0-2 / 0-5) |
| PP_NLATTENONOFF | 17 | 27 | int32 | rw | atenuación eco no lineal |
| PP_LIMITONOFF | 17 | 19 | int32 | rw | limitador del canal comms |

Formato (idéntico a `readBeamLed`): lectura `write{resid, cmd|0x80, N+1}` + `read{status,
N bytes LE}`; escritura `write{resid, cmd, N, payload…}` (int32/float = 4 bytes LE).
Mux categorías útiles: **4/5** far-end (¡passthrough de la referencia!), **6,3** beam
post-procesado auto-select, **7,3** beam ASR (actual RIGHT), **3,n** mic amplificado, **0** silencio.

## 3. Playbook de test (30-60 min, con record.py)

**Test 0 — ¿la referencia llega eléctricamente? (10 min, decisivo)**
`AUDIO_MGR_OP_R ← (5,0)` (far-end con delay) por I2C, reproducir TTS/ruido, grabar con
`agent/record.py`. **Se oye el TTS en la grabación → la referencia entra al XVF (ref OK
eléctrica). Silencio → referencia muerta** (GPIO44 no llega al I2S-in del XVF o slot vacío).
Restaurar `OP_R ← (7,3)`.

**Test A — supresión con ruido rosa (nadie habla).** Publicar ruido rosa a la sala
(`lk room join --identity noise --publish pink.ogg sebastian`), grabar 20 s el track del
mic en tres configuraciones del mux RIGHT: (3,0) mic crudo → E_raw; (7,3) ASR → E_asr;
(6,3) comms → E_comms. Supresión = `20·log10(RMS_raw/RMS_x)`. Baseline de suelo: misma
grabación con el altavoz en silencio.

**Test B — métricas I2C durante la reproducción.** Loguear cada 500 ms:
`AECCONVERGED`, `PATHCHANGE`, `RT60`, `NUM_FARENDS`.

**Interpretación (criterios numéricos):**
- **Referencia OK**: CONVERGED=1 estable en <5 s, supresión ASR **≥ 18-25 dB**, comms ≥ 30-40 dB.
- **Referencia muerta**: CONVERGED=0 siempre, supresión ASR **≤ 3 dB**, RT60 inválido, Test 0 en silencio.
- **Solo residuo no-lineal** (caso esperado): CONVERGED=1, ASR 15-25 dB pero comms ≫ ASR
  (el gap ASR↔comms ES la supresión residual que el slot RIGHT no tiene).

**Test C — LEFT vs RIGHT**: repetir A con `config.zig mic_channel = .left` (o sin
reflashear, ya cubierto por el mux (6,3) vs (7,3) del test A).

## 4. Árbol de decisión

1. **Ref. muerta** → a) confirmar que av_render escribe LEFT≠0 (volcado del TX);
   b) `AUDIO_MGR_REF_GAIN` a 1.5-4 y `AEC_FAR_EXTGAIN` (33,5); c) si Test 0 da silencio
   puro: GPIO44 no llega al XVF → recableado físico (puente DOUT→I2S-in del XVF) o asumir
   half-duplex permanente (`mic_src.setMuted` durante TTS).
2. **Ref. OK + residuo no-lineal** (lo más probable) → por orden de coste:
   a) **switch dinámico del mux por I2C**: `AUDIO_MGR_OP_R ← (6,3)` (comms, con supresión
   residual) al empezar a hablar el agente, `← (7,3)` al terminar. 5 bytes I2C, sin
   reflashear, conserva barge-in (el canal comms está diseñado para full-duplex); subir
   `PP_GAMMA_ENL` (hasta 5.0) y `PP_NLATTENONOFF=1` si aún se cuela;
   b) reconstruir el **volumen real** (hoy `set_out_vol(35)` cae en un codec noop): controlar
   los registros DAC del AIC3104 (0x2B/0x2C) y subir gradualmente midiendo E_asr;
   c) `AUDIO_MGR_SYS_DELAY` fino si PATHCHANGE oscila (ref. desalineada);
   d) **half-duplex selectivo solo a volumen alto**: gate con `mic_src.setMuted(true)`
   mientras el agente habla si volumen > umbral — sacrifica barge-in solo entonces.
3. **Validación final**: TTS a volumen 60-70, hablar encima → el agente debe transcribir
   al usuario sin auto-escucharse (barge-in) y sin lazo de feedback.

## 5. Esfuerzo

Instrumentación Zig (~80 líneas en xvf_dfu.zig) + tests: 1 día. Switch dinámico del mux
ligado al estado de reproducción + volumen AIC3104 + retune: 1-2 días.

## Código
**firmware/main/xvf_dfu.zig (añadir)** — Diagnóstico y routing AEC por I2C — mismo patrón write/read que readBeamLed, listo para pegar

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

/// 1 = AEC filter converged (la métrica clave del test B).
pub fn readAecConverged() ?bool {
    return if (readU32(RESID_AEC, 3)) |v| v != 0 else null;
}
pub fn readAecPathChange() ?bool {
    return if (readU32(RESID_AEC, 0)) |v| v != 0 else null;
}
pub fn readNumFarends() ?u32 {
    return readU32(RESID_AEC, 72); // debe ser >= 1
}
pub fn readRt60() ?f32 {
    return if (readU32(RESID_AEC, 9)) |v| @bitCast(v) else null; // <0 = inválido
}

/// Mux de salida por slot: cat 7/3=beam ASR (actual R), 6/3=comms con supresión
/// residual, 5/0=far-end passthrough (test 0: "oír" la referencia), 3/0=mic crudo.
pub fn setOutputR(category: u8, source: u8) bool {
    return write(&[_]u8{ RESID_AUDIO_MGR, 19, 2, category, source }); // AUDIO_MGR_OP_R
}
pub fn setOutputL(category: u8, source: u8) bool {
    return write(&[_]u8{ RESID_AUDIO_MGR, 15, 2, category, source }); // AUDIO_MGR_OP_L
}

/// Supresión de eco residual del canal comms y su agresividad no lineal.
pub fn setPpEcho(on: bool) bool {
    return write(&[_]u8{ RESID_PP, 23, 4, if (on) 1 else 0, 0, 0, 0 }); // PP_ECHOONOFF i32 LE
}
pub fn setPpGammaEnl(v: f32) bool { // [0.0 .. 5.0]
    const b: [4]u8 = @bitCast(v);
    return write(&[_]u8{ RESID_PP, 26, 4, b[0], b[1], b[2], b[3] }); // PP_GAMMA_ENL
}
```

**playbook de test (shell, no va al repo)** — Sesión de medición 30-60 min: ruido rosa por la sala LiveKit + captura con record.py por configuración de mux

```bash
# 0) preparar estímulo (20 s de ruido rosa, ogg/opus para lk)
ffmpeg -f lavfi -i "anoisesrc=color=pink:duration=20" -ar 48000 pink.ogg

# 1) sala fresca con SOLO la placa dentro (sin agente → sin coste OpenAI)
lk room delete sebastian && echo "resetea la placa ahora"

# 2) por cada config de mux RIGHT {(3,0) crudo, (7,3) ASR, (6,3) comms}:
#    escribir el mux (log del firmware, o botón/CLI de debug que llame setOutputR)
#    y capturar el track publicado mientras suena el ruido:
lk room join --identity noise --publish pink.ogg sebastian &
python agent/record.py --out take_asr.wav        # 20 s

# 3) baseline de suelo: misma captura sin publicar nada (altavoz en silencio)
python agent/record.py --out take_floor.wav

# 4) métricas → supresión = 20*log10(RMS_raw / RMS_x)
python - <<'EOF'
import wave, numpy as np
for f in ["take_raw.wav","take_asr.wav","take_comms.wav","take_floor.wav"]:
    w = wave.open(f); x = np.frombuffer(w.readframes(w.getnframes()), np.int16)
    print(f, "RMS=", np.sqrt(np.mean(x.astype(np.float64)**2)).round(1))
EOF
# Criterios: ASR >= ~18-25 dB bajo crudo -> referencia OK;
#            ASR <= ~3 dB -> referencia muerta;
#            comms >> ASR -> el gap es la supresión residual que a RIGHT le falta.
```

## Riesgos
- **Que GPIO44 no llegue eléctricamente al pin I2S-in del XVF en la placa Seeed (sin esquemático público no es verificable en papel) — la referencia estaría muerta y el AEC nunca convergerá.** → Test 0 (mux far-end passthrough, 10 min) lo resuelve antes de invertir nada más; si falla, caer a half-duplex con mic_src.setMuted (ya implementado) y evaluar puente físico.
- **El firmware Seeed inthost 1.0.7 es un build custom: algunos comandos de la tabla (PP_*, AUDIO_MGR_OP_*) podrían no estar servidos, como ya pasó con RESID_LED 0x0C (docs/MIC_CHANNEL_TUNING.md).** → Todos los reads devuelven ctrl_status: probar cada comando con lectura y validar status==0 antes de confiar en él; los resid 33 (AEC) y 20 (GPO) ya están probados en este firmware.
- **Conmutar OP_R a (6,3) durante el TTS reintroduce el timbre 'de lata' (NS del comms + BVC del agente) justo en los turnos con barge-in, y el AGC del canal comms cambia el nivel respecto a SHIFT=14.** → Aplicar el switch solo mientras el agente habla (ventana corta), ajustar PP_MIN_NS/PP_AGCONOFF por I2C para suavizar el post-proc, o compensar SHIFT dinámicamente; medir con el mismo pipeline de métricas de MIC_CHANNEL_TUNING.md.
- **El control de volumen actual es ficticio (noop codec_if): subir volumen tocando el AIC3104 puede cambiar el camino acústico y de ganancia que el AEC tenía aprendido, provocando re-convergencias audibles.** → Subidas graduales (≤3 dB por paso) monitorizando AEC_AECPATHCHANGE/AECCONVERGED; dejar AUDIO_MGR_REF_GAIN coherente con la ganancia real del DAC.
- **El barge-in con supresión residual activa atenúa el doble-talk (el usuario hablando encima del agente llega recortado al STT).** → Tunear PP_GAMMA_E/ETAIL con frases de doble-talk grabadas con record.py; si degrada, preferir la vía volumen moderado + AEC lineal (RIGHT) y gate solo a volumen alto.

## Preguntas abiertas
- ¿Está servido AUDIO_MGR_OP_R (resid 35) en el build Seeed inthost-lr48 1.0.7? (RESID_LED 0x0C no lo estaba; los resid 33 y 20 sí funcionan — primera lectura con status==0 lo confirma en 1 min).
- ¿Qué (categoría,fuente) reporta el firmware 1.0.7 de fábrica para OP_L/OP_R? (leer 35/15 y 35/19 con READ_BIT; se espera L=(8,0) y R=(7,3) o similar).
- ¿av_render duplica realmente el mono del agente en el slot LEFT del TX o solo llena RIGHT? (volcar 4 frames del buffer TX en un log de debug lo cierra; condiciona el diagnóstico 'referencia muerta').
- ¿Dónde actúa hoy de verdad el 'volumen 35' si el codec_if es noop? (¿ganancia software de esp_codec_dev, av_render, o no actúa y el nivel lo fija el 0 dB del AIC3104?).
- ¿El canal comms (6,3) con PP_ECHOONOFF degrada el doble-talk lo bastante como para romper el barge-in que motiva todo esto? (test de doble-talk pendiente de hardware).

## Fuentes
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