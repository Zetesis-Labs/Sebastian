# Proyecto AEC → full-duplex — plan de implementación

> Síntesis de la exploración multi-agente del 2026-07-03 (4 agentes: causa raíz,
> change-map, validación, riesgos). Objetivo: AEC del XVF3800 convergiendo en
> sesión → retirar el gate half-duplex → full-duplex natural (hablar encima del
> agente con cualquier frase, volumen alto, sin eco).

## El hallazgo que reordena el proyecto

**El "fix" de FAR_EXTGAIN fue casi con certeza un placebo.** La doc oficial XMOS
(Control Command Appendix v3.2.1) define `AEC_FAR_EXTGAIN` en **dB**, no lineal:
el 0.0 de fábrica significa **0 dB = ganancia unidad**, no "altavoz mudo". La
narrativa de `docs/AEC.md` ("0.0 dejaba el AEC inerte") es un misdiagnóstico: el
experimento de verificación cambió a la vez la config Y el estímulo (de voz
full-scale a un tono continuo de 5 s al 15 % FS) — lo más probable es que el AEC
siempre hubiera convergido con ese tono. Test de refutación en 2 min: sonda con
`FAR_EXTGAIN=0.0` tras power-cycle → si converge igual, placebo confirmado.

Dos datos duros más de la doc XMOS:
- **`AECCONVERGED` (33/3) es latched de por vida** ("once set to 1, never
  reset"). Que la telemetría lea 0 en toda sesión significa que el AEC **jamás
  ha convergido desde el boot** en producción. No es un problema de muestreo.
- **`SPENERGY` (33/80) es energía near-end (lado micro), no far-end.** La
  inferencia "el AEC ve señal de referencia" era incorrecta: hoy NO existe
  telemetría del nivel de la referencia. Es el hueco nº1 a instrumentar.

## Hipótesis de causa raíz (rankeadas)

| # | Hipótesis | Prob. | Diagnóstico | Palanca |
|---|---|---|---|---|
| H1 | **La referencia clippea dentro del XVF**: `REF_GAIN=8.0` (build ReSpeaker; default XMOS 1.5) × playback a 0 dBFS satura la referencia interna → señal recortada que no correlaciona → el filtro lineal nunca converge. La sonda escapó (tono a −16.5 dBFS). Explica TODO el histórico (el sw-vol fue siempre un no-op → siempre full-scale). Corolario: `MIC_GAIN=90` (default 10) viola el headroom XMOS. | Alta | Escribir `REF_GAIN=1.0` (35/1) y correr una sesión; medir linealidad con tono a amplitudes crecientes vía `MUX_FAR_END_W_GAIN` | `REF_GAIN≈1.0` en `applyConfig()`; revisar `MIC_GAIN`/`SHIFT` |
| H2 | **Camino de eco no lineal a plena escala**: el altavocito distorsiona (ya medido: ~6 dB de cancelación por armónicos a vol 60) y el AEC lineal no lo modela. | Media-alta | Sesión a vol 50-60 → ¿converged=1? | Capar volumen de sesión; recuperar sonoridad con supresión residual (`PP_ECHOONOFF` 17/23) |
| H3 | **El mono del agente no llega al slot LEFT** (la referencia AEC es el slot 0 del I2S-in del XVF; la sonda escribía AMBOS slots y nunca lo discriminó; que av_render duplique el mono es una suposición sin verificar). | Media | Sonda por-slot (tono solo en slot 0, luego solo en 1, midiendo `MUX_FAR_END`); sonda hot en sesión | Duplicar explícitamente al render si LEFT va vacío |
| H4 | TTS intermitente: nunca acumula los ~30 s de contenido far-end que pide la convergencia. | Baja-media | Monólogo forzado de 60 s con converged muestreado a 1 s | — |
| H5/H6 | Delay distinto en sesión / XVF pierde estado | Baja (descartadas casi del todo) | Leer filtro real (33/91-93); logState ampliado | — |

## Resultados de la ejecución remota (2026-07-03)

Fase 0 + Fase 1 ejecutadas **en remoto** vía un auto-test de arranque
(`config.probe_aec_on_boot`) que reproduce audio por el altavoz y reporta
convergencia sin sesión ni humano, leído por Grafana tras un reset.

**Sospechosos ELIMINADOS sistemáticamente:**
- `REF_GAIN=8.0` (default XMOS 1.5): clippeaba la referencia a full-scale.
  Confirmado en boot config, corregido a 1.0. **Necesario, no suficiente.**
- `MIC_GAIN=90` (default 10): probado 90→10; regla de headroom XMOS.
- `FAR_EXTGAIN`: es dB; el correcto es 0.0 (factory). El 1.0 era placebo.
- `AECSILENCELEVEL` (33/2): leído = **1e-9 (default)**, no lo subió Seeed.
- **Excitación**: la sonda usaba un tono puro a 0.85 FS (una banda + THD del
  altavoz) → falso `converged=0`. Rehecha con **ruido blanco a −15 dBFS** (el
  estímulo XMOS real).
- **"Routing problem" del primer intento: REFUTADO por docs XMOS.** El agente
  se apoyó en una lectura errónea de `AEC_CURRENT_IDLE_TIME` (es un contador de
  **profiling de CPU en ticks de 10ns**, no actividad far-end). El mux
  `far_end_w_gain` que la sonda midió **ES la entrada del AEC** → la referencia
  sí llega. `inthost` = build INT (far-end por I2S slot 0 nativo, sin selector
  de fuente). El proyecto ESPHome semi-oficial de ReSpeaker usa el AEC standalone
  tal cual, sin config de routing — es el diseño previsto.

**Negativo limpio**: con REF_GAIN=1.0 + MIC_GAIN=10 (el combo nunca probado
junto) + ruido blanco + FAR_EXTGAIN=0.0 + silence-level default → `converged_at=-1`
(nunca). Bajo condiciones de libro, el AEC no adapta.

**Único sospechoso mayor que queda: CAUSALIDAD / DELAY.** XMOS: la referencia
debe llegar ≤40 muestras *antes* que el eco, y "cualquier variación del delay
referencia↔micro degrada severamente"; los huecos por samples perdidos rompen la
adaptación. Nuestro TX es esclavo I2S con `auto_clear_after_cb` (cada underrun de
av_render inyecta ceros = huecos en la referencia). **Instrumento definitivo
pendiente**: leer los coeficientes reales del filtro AEC (`SPECIAL_CMD_AEC_*`
33/90-94) — filtro plano = no adapta; pico en muestra 0 = acausal; pico sano
alineado con el delay = el path funciona y el problema es puramente el delay
(ajustar `SYS_DELAY` 35/26). Y medir la correlación cruzada mic↔referencia para
el delay real. NO más barridos de ganancia.

**Instrumento definitivo ejecutado — lectura de coeficientes del filtro
(33/90-94), 90s de ruido, snapshot cada 15s:**
- El filtro NO está plano y el pico está en índice ~38-64 (NO en 0) → **el AEC
  SÍ adapta, y causalmente.** Descarta routing Y delay/causalidad.
- Pero en 90s el pico nunca pasa de ~0.003 (el eco es ~2.5% de la referencia →
  el pico convergido debería rondar 0.025, 8x más) y su índice **jitterea**
  (38↔62↔64). `ref_gaps=0`. → **NO es convergencia lenta, NI huecos.** El filtro
  **no consigue fijar un modelo estable del eco.**
- Apunta a un path de eco **no estacionario / no lineal** — lo más probable el
  **beamformer/DoA moviéndose** (el path del eco cambia continuamente) o
  distorsión del altavoz. Problema de fondo, no un knob de config.

**BREAKTHROUGH — Experimento A: congelar el beam (2026-07-03).** La hipótesis del
beamformer no era especulativa: quedó **confirmada y reproducible**. La sonda
activa beams FIJOS (`AEC_FIXEDBEAMSONOFF=1` 33/37 + `FIXEDBEAMSAZIMUTH` 33/81 al
frente) antes del ruido, y el resultado es tajante:

| Métrica | Beam adaptativo (antes) | Beam FIJO (experimento A) |
|---|---|---|
| `AECCONVERGED` | 0 (jamás, latched) | **1** |
| `converged_at` | −1 (nunca en 90s) | **1s** |
| pico del filtro | ~0.003, jitterando 38↔64 | **0.024–0.032, estable** (idx 64/39) |
| `nonzero` taps | escuálido | **345–399** (filtro poblado) |
| `path_change` | — | **0** (path estacionario) |

Dos runs idénticos. **El beamformer adaptativo ERA la causa raíz**: al rastrear
al hablante cambia continuamente el path mic→eco (target no estacionario que el
AEC nunca puede fijar). Congelado → path estacionario → el AEC converge en ~1s
con un filtro sano. No era un límite de hardware.

**Conclusión del proyecto AEC**: causa raíz encontrada y **arreglada**. Tras
eliminar exhaustivamente lo config (routing, REF_GAIN, MIC_GAIN, FAR_EXTGAIN,
silence-level, excitación, delay, huecos), el instrumento decisivo (lector de
coeficientes) señaló el beam, y el experimento A lo confirmó. **El AEC SÍ puede
converger.** Productizado: `config.fixed_beam` + `fixed_beam_azimuth_deg` cablean
el beam fijo en `applyConfig` (default off = adaptativo, sin cambio de UX). El
trade-off es tracking del hablante ↔ AEC funcional: para un altavoz de sobremesa
un beam fijo hacia la zona de uso suele bastar.

**Pendiente (requiere estar en casa):** validar full-duplex real = `fixed_beam=true`
+ **quitar el gate half-duplex** + medir en sesión (¿se oye bien al usuario desde
la dirección fija? ¿el eco queda cancelado sin turnos fantasma?). La convergencia
está probada en remoto; la calidad de sesión necesita un humano en la sala. Device
dejado en estado bueno-conocido: REF_GAIN=1.0 + FAR_EXTGAIN=0.0 + MIC_GAIN=90 +
`fixed_beam=false` + sonda off. Auto-test de ruido y lector de filtro reutilizables.

## Fases

### Fase 0 — Instrumentación (sin tocar comportamiento; 1 flasheo)

1. `logState()` ampliado: `FAR_EXTGAIN`, `REF_GAIN`, `I2S_INACTIVE`,
   `AEC_CURRENT_IDLE_TIME` releídos en sesión (cierra H6 y da visibilidad).
2. Sonda de boot mejorada tras flag (`probe_on_boot`): ERLE por **Goertzel a
   1 kHz** (el pico broadband lo contaminan los armónicos del altavoz) + RMS,
   `t_converge_ms` (poll de AECCONVERGED a 250 ms), línea única parseable
   `probe result: ... erle_1k_cdb=... hot=0/1`.
3. Bridge: gauges nuevas (`aec_path_change`, `aec_rt60_ms`, `aec_far_extgain`,
   `aec_ref_gain`, `erle_*`, `aec_converge_ms`), counter
   `aec_sessions_total{converged}` (línea `aec post-session:`), y
   `echo_gated_peak` (el pico del beam DURANTE el habla del agente, hoy
   invisible porque el gate fuerza `mic_level=0`).
4. Agente: **detector de turnos fantasma** (`phantom_turns_total{reason}`) —
   timing (durante speaking / cola ≤2,5 s) + solape de trigramas con lo que el
   agente acaba de decir + idioma no-español. Pseudocódigo completo en el
   informe de validación. Con gate ON debe ser 0 por construcción: es la
   métrica principal del "después".
5. Dashboard: fila "AEC / Eco" (converged+path_change+agent_state, ratio de
   sesiones convergidas, ERLE, gated_peak, fantasmas).
6. **Rehacer el A/B honesto de FAR_EXTGAIN** (tono + power-cycle entre corridas)
   y corregir la narrativa de `docs/AEC.md` — la historia del "bug de fábrica"
   contaminará futuros debugging si se queda.

### Fase 1 — Experimentos de causa raíz (una tarde, en orden de coste/beneficio)

1. `REF_GAIN=1.0` en `applyConfig()` + sesión de prueba (H1, candidato nº1).
2. Sesión a volumen 50-60 (H2) — combinable con (1) la misma tarde.
3. Si no laten `converged=1`: sonda hot (§T-HOT del plan de validación — el
   agente reproduce un tono de 1 kHz por el camino real LiveKit→av_render con
   el micro gateado, el device conmuta `OP_R` a `MUX_FAR_END` y mide por dentro
   sin tocar TX) → discrimina H3 y da el snapshot de config en sesión.
4. Matriz de diagnóstico si la sonda hot falla: `FAR_EXTGAIN≠1.0` en sesión →
   el fix se revierte en runtime; `path_change` incrementando → los resyncs
   I2S disparan el PCD; `IDLE_TIME` alto → el AEC se auto-resetea por idle.

**Gate de salida**: `converged=1` durante habla real del agente en sesión, ERLE
1 kHz ≥ 15 dB, `echo_gated_peak < 3000` (no dispararía el VAD local).

### Fase 2 — Convergencia estable (protocolo F1)

10 sesiones seguidas con conversación real ≥60 s: `aec_sessions_total{converged="1"}`
= 10/10, converged antes del final del primer turno largo, 0 reboots/heals.

### Fase 3 — Full-duplex por etapas (change-map)

- **Etapa 0 (plumbing, cero cambio)**: flag runtime `sebastian.duplex` por data
  channel — default **fail-safe**: cada sesión nace half-duplex y el agente la
  promociona a full (`SEBASTIAN_DUPLEX=full|half`, default half). Rollback =
  reiniciar el agente, sin reflashear. `TurnDetection` con `interrupt_response=True`
  + `create_response=True` **explícitos y comentados** (hoy van por default).
  El gate se condiciona (`if (!duplex_full and gatedByAgent())`), aún no se borra.
- **Trampas conocidas a resolver en esta etapa** (del registro de riesgos):
  - El **barge-in por wake word solo corre sobre audio gateado** — mover el feed
    al camino de captura normal (sigue vigilando durante `speaking`, el frame se
    publica Y se vigila). Se CONSERVA como red de seguridad determinista incluso
    en full-duplex (si el AEC degrada mid-sesión, "Sebastián" sigue cortando).
  - El **LINGER depende del gate** (level=0 durante el habla del agente): con el
    micro abierto, el eco refrescaría `last_voice_tick` y las sesiones no
    cerrarían por silencio. El cierre debe pasar a apoyarse en `agent_state` +
    nivel, y re-validarse con eco entrando al medidor.
  - El drop de gate-silence del agente **se queda** (es del gate de handoff del
    pre-roll, no del half-duplex).
- **Etapa 1 (canary)**: `SEBASTIAN_DUPLEX=full` + protocolo F2-F4: monólogo
  largo sin gate (fantasmas=0, sin auto-interrupciones), **barrido de volumen
  60/80/100** (aceptación: 0 fantasmas hasta 80; 100 = objetivo), interrupción
  con contenido sin wake word (p95 ≤1,5 s y responde a lo dicho, ≥4/5).
- **Etapa 2 (tripwire)**: el agente detecta la firma de auto-interrupción y
  publica `sebastian.duplex: half` en caliente (auto-degradación).
- **Etapa 3**: F5 resistencia (10 sesiones mixtas, 0 fantasmas, cierres por
  silencio/end_session, nunca por tope).

### Fase 4 — Limpieza y bake-in

Semanas estables después: borrar `gatedByAgent`/hangover de verdad, decidir el
destino del flag (palanca de ops vs comptime), actualizar `ROADMAP.md`,
`docs/STATUS.md` y reescribir `docs/AEC.md` con la causa raíz real.

## Decisiones de producto abiertas (para Rubén)

1. **Volumen objetivo**: si el AEC solo cancela bien ≤80, ¿capar, o volumen
   adaptativo (bajar durante TTS)? La curva ERLE-vs-volumen de la Fase 1 da el
   dato. (Recordar: el "35" histórico nunca existió — siempre sonó a 100.)
2. **Canal**: quedarse en RIGHT (residual crudo) vs supresión residual del PP
   (`PP_ECHOONOFF`) — riesgo medido: la supresión puede recortar el doble-talk
   que es justo el objetivo.
3. **Conservar el barge-in por nombre en full-duplex** (recomendado: sí).

## No repetir (resumen; lista completa en el informe de riesgos)

No afinar delays/ganancias sin confirmar que el AEC adapta; no puertas por
energía (eco ≈ voz en nivel: es problema de señal, no de umbral); no gatear en
"thinking"; no fiarse de escrituras I2C sin readback; no asumir que un resultado
de sonda en boot transfiere a sesión; no flashear lógica pura sin test de host
(`@min`→u9); los stashes `stash@{0}`/`stash@{1}` son el único registro de los
experimentos revertidos — extraer antes de borrar.
