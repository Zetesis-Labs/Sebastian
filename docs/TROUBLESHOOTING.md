# Sebastian — Playbook de depuración

Referencia **síntoma → causa raíz → solución** destilada de una sesión de depuración larga y real del altavoz de voz Sebastian (ReSpeaker XVF3800 + XIAO ESP32-S3, firmware en Zig, agente LiveKit + OpenAI Realtime).

## Regla de oro

Cuando algo falle, **prioriza el datasheet y la medición directa sobre las conjeturas**. Graba el audio real, mira el RMS y el espectro, y sobre todo **ESCÚCHALO**. Los números (niveles RMS, picos) describen *nivel*, no *inteligibilidad*: un audio puede tener buen RMS y sonar a lata. Y no reimplementes en software lo que el XVF3800 ya hace en hardware (AEC, beamforming, supresión de ruido): duplicar esas etapas casi siempre empeora el resultado en vez de mejorarlo. La mayoría de los callejones sin salida de esta sesión vinieron de adivinar en lugar de medir, y de "ayudar" al XVF por software.

---

## 1. El micro entrega todo ceros (el agente nunca transcribe)

**Síntoma:** en el DIN del I2S se lee `0x00000000` de forma constante; el agente no transcribe nada de lo que dice el usuario.

**Causa raíz:** dos problemas a la vez. El XVF3800 tenía el firmware equivocado (el "non-master" / `i2s_dfu` 1.0.4, que **no** hace streaming del audio del micro). Además, el XVF **arranca muteado**.

**Solución:** hacer DFU del XVF al firmware I2S-master "inthost" (1.0.7) por I2C desde el firmware del ESP (`xvf_dfu.zig`), y quitar el mute vía I2C en la GPIO30. Verificar en el log de arranque que aparece la versión 1.0.7 del XVF y `XVF mute readback: UNMUTED`.

---

## 2. En modo I2S esclavo el ESP lee 0 bytes / la lectura retorna al instante sin datos

**Síntoma:** en modo I2S slave, la lectura del ESP devuelve inmediatamente sin datos (no hay reloj).

**Causa raíz:** el canal RX de I2S se habilitó en `board.init()` **antes** de que el XVF estuviera generando reloj, así que nunca se enganchó al clock.

**Solución:** deshabilitar y volver a habilitar el canal RX **después** de que el XVF esté arriba (se hace en la primera llamada a `read_frame()`). Confirmado con una "sonda de reloj" (clock probe) que, una vez arreglado, bloqueó ~5 ms y devolvió una trama completa de datos.

---

## 3. `Guru Meditation Error ... find_desc_for_source ... LoadProhibited` y boot-loop

**Síntoma:** `Guru Meditation Error ... find_desc_for_source ... LoadProhibited` durante la asignación de interrupciones de I2S-slave o I2C; la placa entra en boot-loop.

**Causa raíz:** dos cosas. (a) Una inestabilidad intermitente en la asignación de interrupciones. (b) Compartir un único canal I2S dúplex entre la tarea de lectura del micro y el renderer del altavoz corrompía el DMA.

**Solución:** usar **dos puertos I2S separados** (RX del micro en `I2S_NUM_1`, TX del altavoz en `I2S_NUM_0`), nunca un canal dúplex compartido. Si sigue en boot-loop, hacer power-cycle.

---

## 4. Boot-loop y esptool no conecta ("Failed to connect to ESP32-S3: No serial data received")

**Síntoma:** la placa entra en boot-loop y esptool no logra conectar: `Failed to connect to ESP32-S3: No serial data received`.

**Causa raíz:** el firmware crashea o se cuelga tan rápido que el USB-JTAG no consigue sincronizar; además, un binario concreto puede desplazar la IRAM y destapar un crash latente.

**Solución:** hacer power-cycle físico (desconectar el USB 3 s y volver a conectar); o mantener pulsado el botón BOOT (el que está hundido) mientras se enchufa el USB (modo download manual); después flashear un binario conocido-bueno. **La placa no está bricked.**

---

## 5. `Guru Meditation ... vApplicationStackOverflowHook` (stack overflow)

**Síntoma:** `Guru Meditation ... vApplicationStackOverflowHook`, desbordamiento de pila.

**Causa raíz:** una tarea de FreeRTOS tenía un buffer local grande (un buffer de lectura de I2S de ~2 KB) sobre un stack demasiado pequeño (4 KB).

**Solución:** mover los buffers grandes a estáticos a nivel de módulo y/o aumentar el stack de la tarea (parámetro de tamaño de stack en `xTaskCreatePinnedToCore`).

---

## 6. La voz suena a "helicóptero" y/o "ultratumba" (fantasmal, con warble), pero el RMS no muestra huecos

**Síntoma:** la voz suena a helicóptero y/o de ultratumba (fantasmagórica, ondulante), aunque el RMS no muestra caídas ni huecos.

**Causa raíz:** tres, todas corregidas.
- (a) Una tarea productora libre (free-running) + ring buffer derivaban contra el dominio de reloj del consumidor (XVF a 48 kHz vs. consumidor de LiveKit); el ring se vaciaba periódicamente y repetía/mantenía muestras → warble periódico.
- (b) Un AGC por-muestra que escalaba cada muestra según su propia magnitud **deformaba** la forma de onda → voz robótica/fantasmal.
- (c) Una condición de carrera entre cores: tanto el productor como el consumidor escribían el índice de lectura del ring buffer.

**Solución:** eliminar el ring buffer por completo y leer el I2S **directamente** dentro de `read_frame()` (paced por el consumidor — la demanda del pipeline *es* el reloj, sin deriva); usar **ganancia fija**, nunca un AGC por-muestra; y si alguna vez se usa un ring, hacerlo estrictamente single-producer/single-consumer (solo el consumidor escribe el índice de lectura).

---

## 7. La voz está "entrecortada" (trozos de silencio)

**Síntoma:** la voz es entrecortada, con fragmentos de silencio.

**Causa raíz:** el ring buffer sufría underrun y una ruta demasiado agresiva de "reconstruir el colchón" insertaba ~256 ms de silencio en cada pequeño underrun.

**Solución:** la lectura directa paced por el consumidor (sin ring) elimina esto. Si se usa buffering, ante un underrun breve **mantener la última muestra** en vez de insertar un colchón de silencio.

---

## 8. La voz es inteligible pero suena "enlatada" (metálica, a lata)

**Síntoma:** voz inteligible pero con timbre de lata/metálico.

**Causa raíz:** **doble supresión de ruido**. La salida LEFT del XVF ya lleva la NS on-chip del XVF, y el agente aplica encima su propia cancelación de ruido BVC; dos pasadas de NS generan artefactos de musical-noise.

**Solución:** usar el slot **RIGHT** del I2S (el beam ASR crudo, sin NS on-chip) y dejar que la BVC del agente haga la **única** pasada de NS. Salvedad honesta: XMOS no lo exige explícitamente; fue una elección empírica y conviene hacer un A/B por entorno.

---

## 9. La voz está "saturada/distorsionada" (clipping)

**Síntoma:** voz saturada, con distorsión de recorte.

**Causa raíz:** la ganancia fija de 32→16 bits (right shift) era demasiado pequeña, así que el habla fuerte se recortaba (clamp) en 32768.

**Solución:** calibrar el shift a partir del RMS/pico del WAV grabado — objetivo ~-18 dBFS de RMS con picos por debajo de fondo de escala (en este proyecto `SHIFT=13` para el canal RIGHT/ASR). Nota: el nivel de voz varía ~4× entre sesiones, así que una ganancia fija es un compromiso; la solución profesional sería un **AGC de envolvente lenta** (ganancia constante dentro de un bloque, adaptada despacio). Un **AGC por-muestra NO debe usarse** (distorsiona, ver entrada 6).

---

## 10. La IA "habla sola" / ignora al usuario / responde incoherencias

**Síntoma:** la IA habla consigo misma, ignora al usuario o responde sinsentidos.

**Causa raíz:** **múltiples procesos del agente** corriendo a la vez (uv lanza un wrapper + un worker; los procesos zombies se acumulan).

**Solución:** `pkill -9 -f "agent.py"` y arrancar exactamente **uno**: `uv run agent.py dev`.

---

## 11. El agente nunca entra en la sala (la room muestra 1 participante)

**Síntoma:** el agente no se une a la sala; `lk room list` muestra 1 solo participante.

**Causa raíz:** el agente solo hace auto-dispatch a una sala **nueva**.

**Solución:** `lk room delete sebastian`, luego resetear el dispositivo para que se una a una sala fresca; confirmar que `lk room list` muestra 2 participantes.

---

## 12. No se puede saber si el audio es bueno solo con los números

**Síntoma:** imposible juzgar la calidad del audio a partir de las cifras.

**Causa raíz:** los **niveles** de audio (RMS/pico) no son **inteligibilidad**.

**Solución:** el agente graba el micro recibido en `/tmp/sebastian_rx.wav` — **ESCÚCHALO** con `afplay` y calcula un espectro aproximado (energía por frecuencia). Por ejemplo, una voz "de lata" muestra casi nada de energía por encima de ~2 kHz.
