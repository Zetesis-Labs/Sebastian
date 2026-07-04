# TESTING — Sebastian

Estrategia de test del altavoz Sebastian (ESP32-S3 + XVF3800, firmware Zig +
agente Python). Documento honesto: **qué probamos hoy, qué queremos probar, y qué
no se puede automatizar.**

## Tesis (léela antes que nada)

Los tests que tenemos hoy son **host tests**: lógica pura compilada y ejecutada en
la máquina de CI, sin hardware. Son buenos y necesarios, pero cubren **la capa
donde casi no tuvimos bugs**. Casi todos los fallos reales del proyecto vivieron en
hardware/integración/acústica — la capa que un host test **no toca**:

| Bug real (histórico) | ¿Lo caza un host test? | Capa |
|---|---|---|
| Helicóptero/warble del I2S (drift de reloj) | ❌ | timing HW |
| El AEC no convergía (beam adaptativo) | ❌ | físico/acústico |
| `FAR_END_DSP_ENABLE` revierte en power-cycle → acople | ❌ | estado del XVF |
| Tormenta SCTP tumbando la sesión | ❌ | transporte/red |
| Corte de sesión (AEC cancela tan bien → micro "silencioso" → timeout) | ❌ | integración |
| USB-serial-JTAG se cuelga | ❌ | hardware/USB |
| `@min` estrecha el tipo → overflow | ✅ (lógica pura) | — |

**Conclusión honesta:** un CI verde da una falsa sensación de cobertura. Garantizar
calidad exige subir por la pirámide hasta el hardware.

## La pirámide de test

```
        ┌───────────────────────────────┐
   4    │  Acústico / end-to-end        │  ← eco real, double-talk, wake far-field
        │  (rig + humano, NO automatiz.)│     LO MÁS IMPORTANTE, lo menos cubierto
        ├───────────────────────────────┤
   3    │  Hardware-in-the-loop (HIL)   │  ← placa en runner: flash → probe →
        │  (probes + telemetría)        │     asertar telemetría. YA TENEMOS piezas
        ├───────────────────────────────┤
   2    │  On-target unit tests         │  ← corre en el S3 (aún no montado)
        ├───────────────────────────────┤
   1    │  Host tests (lógica pura)     │  ← LO QUE TENEMOS. CI verde ≠ funciona
        └───────────────────────────────┘
```

Regla: cada capa **necesaria pero no suficiente**. La 1 es barata y cubre poco
riesgo; la 4 es cara y cubre el riesgo real.

**Transversal — Gemelo digital / simulación**: se sitúa entre los niveles 2 y 3
(realismo de target y de protocolo **sin placa**), pero con techo en la acústica.
Ver la sección "Gemelo digital" más abajo.

---

## Lo que TENEMOS hoy

### Nivel 1 — Host tests (`firmware/core_test.zig`)

**Cómo corren:**
- **CI**: `.github/workflows/firmware-tests.yml` — descarga el fork Zig fijado
  (kassane/zig-espressif-bootstrap `0.16.0-xtensa`, el mismo de
  `firmware/cmake/zig.cmake`) y ejecuta `zig test core_test.zig` en el runner
  `herschel-runners`. No usa ESP-IDF ni `idf.py`.
- **Local**: `cd firmware && ../tools/zig.sh test core_test.zig` (usa el zig del
  build en `firmware/build/zig-relsafe-*` o el del PATH).

**El patrón**: la lógica testeable se **extrae a `firmware/main/core/`** (sin deps
de ESP/`extern`) para poder compilarla en host. Módulos cubiertos: `aec_core`,
`decimator`, `mic_gate`, `pre_roll_core`, `token_core`, `xvf_pcm`.

**Qué cubren bien** (39 tests, con técnicas serias — no son triviales):
- **Decimador (DSP)**: vectores *golden* con salida exacta (respuesta al impulso,
  warm-up del FIR con ganancia unidad), **tests diferenciales** (chunked == full
  en particiones distintas + patrones generados), preservación de fase, saturación
  acotada, "nunca escribe más allá del buffer del caller".
- **Pre-roll (ring buffer)**: matriz de invariantes 6×6×5 + **modelo de referencia**
  comparado paso a paso en sesiones scripteadas; wrap/anchor bien cazado.
- **Mic gate (FSM)**: tabla de eventos + modelo de referencia sobre scripts
  generados; hangover finito y su interacción con full-duplex/mute.
- **Token parser**: rechaza bytes de control (`\x00 \t \x7f`), fuerza `ws/wss`, y
  **no toca los buffers si falla** (sin copias parciales).
- **PCM / AEC encoding**: shift+clip, softclip, little-endian, NaN/inf en la
  telemetría escalada.

**Valoración honesta**: profundidad alta, pero solo sobre **la lógica fácil de
desmontar**. Ver los agujeros abajo.

### Nivel 3 (manual) — Probes de hardware + telemetría

Esto es lo que **no se suele contar como testing, pero lo es**: los self-tests que
el propio device ejecuta, leídos por la telemetría. Hoy son **manuales**.

- **Probes** (en `firmware/main/xvf_aec.zig`, flags en `config.zig`, todos `false`
  en producción):
  - `probeReference()` (`probe_aec_on_boot`) — reproduce ruido y reporta si el AEC
    converge (`converged_at`, pico del filtro, `ref_gaps`).
  - `probeDualChannel()` (`probe_dual_channel_on_boot`) — camino B: eco residual
    comms vs ASR crudo, con beam fijo y adaptativo.
  - `probeOutputGain()` (`probe_output_gain_on_boot`) — mide si un knob actúa como
    volumen maestro.
  - `logPpConfig()` — vuelca la config del post-procesador en cada boot.
- **Telemetría** (`tools/telemetry/bridge.py`): serial → OTLP → stack LGTM
  (Grafana/Loki/Prometheus/Tempo). Es la **salida aseverable**.

Cuando validamos "flash → `converged_at=1s`, filtro 0.025 → ✓", eso es un **test
HIL ejecutado a mano**. Las piezas existen; falta el arnés que lo automatice.

---

## Lo que NO cubrimos (agujeros, por riesgo)

1. **🔴 La máquina de sesión (`app.zig SessionLoopState`)** — donde vivió el bug de
   campo (el `silence_timeout` cortando a mitad de frase, el keepalive por
   `render_peak`, el hangover del agente). No está en `core/`, así que **cero
   tests**. Es lógica pura y timing-dependiente: extraíble a `core/session_core.zig`
   y testeable con un reloj simulado.
2. **🔴 El fail-closed del AEC (`applyConfig`)** — la decisión de seguridad ("si el
   beam no se verifica por readback → cae a half-duplex → no abras el micro sobre
   eco") no se testea; solo el *encoding*. Un `true` erróneo = turnos fantasma.
   Testeable con un transporte I2C mock.
3. **🟠 Integración / cableado** — todo son unit tests aislados. Nadie verifica que
   `mic_src` llame al gate, que el decimador reciba el slot correcto, o que el
   pre-roll se vacíe en el handoff. Un refactor que rompa las costuras pasa el CI.
4. **🟠 La capa C** (`provisioning.c`, `token_http.c`) — el parser de
   `sebastian.config.v1` (¡entrada externa por serie!), la NVS, el bring-up WiFi.
   Sin red (el parser Zig `token_core` sí está cubierto, pero no el C de producción).
5. **🟡 La decisión del wake word** — el decimador que lo alimenta está muy cubierto,
   pero el umbral (`0.80`) / debounce, no.
6. **🟡 Golden vectors = detectores de cambio, no de correctud** — codifican "lo que
   hace hoy". Si los coeficientes FIR estuvieran mal, el golden fijaría el error.

---

## Lo que QUEREMOS tener

### Corto plazo — cerrar los agujeros de host test
- Extraer `SessionLoopState` → `core/session_core.zig` y testear timeout/keepalive/
  hangover con reloj simulado. **(Cubre el bug de campo.)**
- Extraer la lógica fail-closed del AEC detrás de un transporte mock.
- Portar el parser de provisioning a Zig (como `token_core`) para poder testearlo.

### Medio plazo — Nivel 3: HIL smoke test (el mayor ROI)
Una **placa dedicada** (ESP32-S3+XVF) pegada a un runner (`herschel`, o una Pi con
el board). En cada build: **flash → arranca → corre `probe_aec_on_boot` → aserta
sobre la telemetría**:
- `converged == 1` y `converged_at` ≤ N s
- pico del filtro AEC en rango esperado
- `FAR_END_DSP_ENABLE == 1` (habría cazado la regresión del acople de esta sesión)
- boot sano (sin reboot-loop, `BOOT OK`, IP asignada)
- niveles de micro sanos (`pcm peak` en rango, no 0, no saturado)

Reutiliza **todo** lo que ya existe (probes + `bridge.py`). Es la diferencia entre
"compila" y "el AEC de verdad converge en la placa".

### Medio plazo — Nivel 2: on-target unit tests
Compilar+correr un subconjunto de tests en el propio S3 (Zig test / Unity). Caza
comportamiento específico del target que el host no reproduce.

### Largo plazo — Nivel 4: rig acústico
Banco con altavoz + micro + el device en un espacio controlado, reproduciendo
**audio golden etiquetado**, midiendo:
- nivel de cancelación de eco (dB de supresión)
- precisión del wake sobre grabaciones etiquetadas (recall / falsos positivos)
- comportamiento de double-talk (¿el camino B se come tu voz?)

Semi-automatizable: los estímulos y algunas métricas sí; el juicio final, no del todo.

---

## Gemelo digital / simulación (transversal, sin placa)

"Gemelo digital" **no es una cosa, es una pila** — la viabilidad y el valor cambian
mucho por capa. Regla de oro: **un gemelo modela lo que tú programas; no descubre
comportamiento físico emergente.** Por eso es excelente para **regresión** e inútil
para **descubrimiento** de lo físico/analógico.

### Las capas

1. **CPU + firmware — QEMU (alta viabilidad).** El fork de QEMU de Espressif arranca
   apps ESP-IDF en un ESP32-S3 emulado (`idf.py qemu`) y corre el **binario real**:
   boot, máquina de estados, lógica en contexto de target (cazaría cosas como el
   overflow de `@min`, que se manifestó en target). Caveat: I2S/I2C/WiFi apenas se
   emulan → hay que stubearlos.
2. **XVF3800 — modelo software (media viabilidad, alto valor puntual).** No se emula
   el DSP propietario, pero **sí su protocolo `device_control`**: un mock I2C que
   devuelve registros (`AECCONVERGED`, config-drift…) y produce frames I2S. Hace
   testeable la interacción firmware↔XVF: `applyConfig`, el **fail-closed** del AEC,
   el DFU. Aquí el gemelo brilla.
3. **Acústica — baja fidelidad justo donde importa.** Convolucionar TTS con una
   respuesta de impulso de sala sirve para el pipeline de audio y el wake, pero el
   **eco/AEC/double-talk** (no estacionario por el beam, no lineal por el THD del
   altavoz) es lo que un modelo sencillo hace mal. Paradoja: si el modelo del eco
   fuera bueno, no necesitarías el AEC real.

### La versión pragmática: gemelo por trazas (record-and-replay)

En vez de simular XVF+acústica, **grabar una vez trazas reales** (la salida I2S del
device durante una sesión: eco, voz, ruido) y **reproducirlas offline** en el
pipeline en CI. Gemelo alimentado por realidad: barato, determinista, caza
regresiones del pipeline (**wake, decimador, gate**) contra datos de hardware de
verdad. **ROI alto, esfuerzo bajo** — por aquí empezar. Ya tenemos el `bridge.py`
para capturar.

### Qué SÍ y qué NO te da

| El gemelo SÍ te da | El gemelo NO te da |
|---|---|
| Regresiones de firmware/lógica en target (QEMU) | Si el AEC **de verdad** converge en tu sala |
| Manejo correcto del protocolo del XVF (fail-closed, DFU, drift) | Calidad real de cancelación de eco |
| Pipeline de audio sobre audio grabado/etiquetado (wake) | **Double-talk** (¿el camino B se come tu voz?) |
| Determinismo, corre en cada commit, sin placa | ASR far-field real, la tormenta SCTP exacta |

**No sustituye al HIL ni al rig acústico — los complementa**: el gemelo corre en
cada commit y sube la cobertura de los niveles 1-3 hacia realismo sin placa; el
nivel 4 (acústico) sigue necesitando realidad o un modelo de física que es en sí
un proyecto de investigación.

### Orden recomendado (por ROI)

1. **Record-and-replay** del pipeline de audio (barato, reusa el bridge de captura).
2. **Mock del XVF** para testear el fail-closed del AEC y el DFU.
3. **QEMU-S3** para el boot + máquina de estados en target.

---

## Limitaciones honestas (lo que NO se podrá garantizar solo con tests)

- **Double-talk**: si el supresor no-lineal del camino B se come tu voz cuando
  hablas por encima, solo se valida con voz real en la sala. Un rig ayuda, pero el
  "suena natural" es subjetivo. → Siempre hará falta un humano de conejillo.
- **Calidad de ASR far-field**: "¿te entiende bien a 4 m con ruido?" depende del
  modelo remoto + acústica de la sala; no hay assert binario.
- **Tormenta SCTP / caídas de transporte**: condiciones de red no deterministas;
  reproducirlas en CI de forma fiable es muy difícil (el bug es upstream,
  `esp-webrtc-solution#186`).
- **Precisión del wake en el mundo real**: se aproxima con grabaciones etiquetadas,
  pero la cola de falsos positivos aparece en uso real, no en el dataset.
- **Coste del HIL**: exige una placa dedicada + un runner, y el USB nativo del S3 es
  frágil (se cuelga, re-enumera) — el propio arnés necesitará reintentos y health-checks.
- **Golden vectors**: cualquier re-tuning legítimo del FIR obliga a regenerarlos a
  mano; son change-detectors, no pruebas de correctud.

---

## Cómo ejecutar (referencia rápida)

```bash
# Host tests (local) — desde firmware/
cd firmware && ../tools/zig.sh test core_test.zig

# Host tests (CI): automático en push/PR que toque firmware/**
#   .github/workflows/firmware-tests.yml

# HIL manual (hoy): activar un probe y leer la telemetría
#   1) poner config.probe_aec_on_boot = true (o el probe que toque)
#   2) idf.py -p <puerto> flash
#   3) leer el serial / Grafana:  converged_at, filtro, far_end_dsp
#   4) volver el flag a false antes de commitear
```

---

*Ver también: `ROADMAP.md` §7 (explotación del DSP del XVF, donde viven los probes)
y §"DevEx / observabilidad" (el stack de telemetría que hace posible el HIL).*
