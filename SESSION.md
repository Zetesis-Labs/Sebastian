# SESSION — 2026-09-17 (dos jornadas: oficina y casa)

Sesión de campo con hardware. Empezó con una unidad que se reiniciaba en cada
sesión y terminó con la unidad **46 min encendida, dos sesiones seguidas en el
mismo arranque, respuestas del agente a fondo de escala y heap sano** tras cada
cierre. Dos bugs distintos, los dos con causa raíz demostrada, ninguno de los
sospechosos del principio.

## Lo que había de verdad (y lo que no era)

### Bug 1 — driver I2C de ESP-IDF v5.4.0 (pánico `StoreProhibited`)

Coredump capturado por syslog (`coredump: task=wakeword exc_cause=29
exc_vaddr=0x00000000 pc=0x4037df5e`) y decodificado con `addr2line`:

```
i2c_ll_read_rxfifo            hal/i2c_ll.h:703
↳ i2c_isr_receive_handler     esp_driver_i2c/i2c_master.c:670
↳ i2c_master_isr_handler_default
```

La ISR del driver escribe los bytes recibidos en un puntero **nulo**. Mecanismo:
una lectura al XVF hace timeout (>1 s sin contestar), el driver pone
`trans_idx=0` antes de marcar `TIMEOUT` y no limpia `contains_read`; la
siguiente transacción que completa (una escritura pura como `setLeds` 80 ms
después) entra por la rama `else` de la ISR y lee `ops[read_buf_pos+1].data`,
un slot vacío del calloc. Issues de Espressif #15444 y #19050; **corregido en
v5.4.1** (`a4ecb6b15c6`) y reforzado en v5.4.4 con un guard del segundo camino.

Se disparaba siempre que el XVF tardaba en contestar: altavoz a tope
(`live_echo=32765`, `render_peak≈8 M`) o **el botón RESET de la placa
ReSpeaker, que resetea el XVF, no el ESP32**. Cuatro pánicos reproducidos con
un timeout o NACK de I2C justo antes.

**Arreglo:** ESP-IDF del host subido a **v5.4.4** (`~/esp/esp-idf`, toolchain
`esp-14.2.0_20260121`) y los tres pins `esp_idf_version: v5.4` del CI subidos a
`v5.4.4` (`firmware-tests.yml`, `pages.yml`). El devcontainer ya usaba
`espressif/idf:release-v5.4`, que va por delante.

### Bug 2 — pre-roll de 140-198 KB contra una caché SCTP de 100 KB (heap roto)

Tras una sesión con pre-roll de 168 KB, la siguiente arrancaba con
`heap[pre-connect] largest=393216` (384 KiB "libres" en RAM interna: imposible,
cabecera de bloque corrompida) y moría en pánico sin poder ni escribir el
coredump. El data channel del publisher tiene `send_cache_size = 100*1024` en
`components/livekit/core/peer.c`; la ventana del pre-roll no tenía tope y un
connect lento la hacía crecer a 2 s de lead + 3-6 s de conexión.

**Arreglo:** `MAX_WINDOW_SAMPLES = 2,5 s` (80 KB) en
`firmware/main/core/pre_roll_core.zig`, conservando las muestras más recientes
(el comando dicho durante el connect). Tests de host actualizados (77/77).
Verificado: `sent pre-roll ... bytes=80016` y `heap[post-session] largest=24576`
estable en dos sesiones seguidas.

### Lo que NO era

Ni alimentación externa, ni memoria (heap sano al abrir), ni el handoff en
`detection task done` (el bucle seguía vivo; el pánico llegaba al primer
timeout de I2C), ni el agente, ni el SDK de SCTP como tal.

## Instrumentación que queda en el firmware

- `reset reason rst:0x.. (NOMBRE)` por syslog al arrancar (`logResetReason`,
  `app.zig`). El `rst:` del ROM y el backtrace del pánico solo salen por la
  consola, que en esta placa nadie lee (UART0 = pines I2S, USB-JTAG en manos de
  TinyUSB).
- **Coredump a flash** (partición `coredump` 64 KB en `0x410000`,
  `CONFIG_ESP_COREDUMP_ENABLE_TO_FLASH`, formato ELF) leído y borrado en el
  arranque por `main/coredump_report.c`: tarea, `exc_cause`, `pc` y backtrace.
  Decodificar en el host:
  `xtensa-esp32s3-elf-addr2line -pfiaC -e build/sebastian.elf <pc> <bt...>`.
- `heap_caps_check_integrity_all` + `heap[post-session]` tras cada cierre de
  sesión (`teardownActiveSession`).
- Aviso `pre-roll N bytes exceeds the SCTP send cache` (ya no debería saltar).
- El sink de syslog espera 5 ms por el buffer en vez de descartar la línea:
  antes se perdía justo la línea previa a un `esp_restart()`.
- Del día anterior: degradación del anillo LED ante fallos de I2C
  (`xvf_ui.zig`, `xvf_dfu.zig`) y el error real del store en `session.go`.
  El volumen 100 → 60 de `board.zig` se descartó: `render_peak` salía a fondo
  de escala con los dos valores, el knob no actúa.

`firmware/dependencies.lock` queda con la ruta absoluta del override de livekit:
**no commitear** (ver Makefile). `firmware/.cache/` es del component manager.

## Trampas del procedimiento (costaron horas)

- **Tras un pánico o `esp_restart`, el puerto serie NO vuelve**: el S3 entrega
  el PHY USB a TinyUSB y solo un **corte de alimentación (reenchufar el USB del
  XIAO)** lo devuelve. Para flashear o provisionar: reenchufar, nunca RESET.
- **El botón RESET de la placa ReSpeaker resetea el XVF, no el ESP32.** Con el
  driver viejo eso hacía entrar en pánico al ESP32 (ráfaga de NACK de I2C).
- Tras `write-flash`, el hard reset de esptool deja al ROM en modo descarga
  (LEDs apagados, puerto vivo, sin WiFi). Hace falta un segundo ciclo
  `--before default-reset --after hard-reset read-mac` para arrancar la app
  (paso 4 de `flash_all.sh` en el scratchpad).
- `flash_args` lista la tabla de particiones **la última**; si el puerto se
  pierde al final de la app la tabla no se escribe. Flashear con las tres
  imágenes en orden explícito y exigir tres `Hash of data verified`.
- **Un doble pánico se reporta como `SW`, no como `PANIC`**: en `panic.c` el
  hint de PANIC se escribe después de `esp_core_dump_write`, y si el volcado
  falla (heap roto) la ruta "entered multiple times" reinicia sin hint. Un
  `rst:0x3 (SW)` sin línea de log de ninguno de los tres `esp_restart` nuestros
  (`wdgTask`, `control.pollOnce`, provisioning) es esto.
- **Heap poisoning ligero cuesta ~10 KB de RAM interna** en esta placa
  (`post-connect` 25 KB / bloque 18 KB) y provocó fallos de reserva en el SDK.
  Descartado; la comprobación de integridad funciona sin él.
- Provisionar y flashear exigen que el Mac y el device estén en la misma red y
  que **todas** las IPs cambien de sitio a la vez: `.devcontainer/.env`
  (`LIVEKIT_NODE_IP`), `agent/.env`, `server.env` del scratchpad y el JSON de
  provisioning. LiveKit hay que recrearlo (`--node-ip`).

## Estado de la red y los servicios (casa, 2026-09-17 noche)

Mac `10.0.0.188`, device `10.0.0.121` (`e0:72:a1:f8:95:f4`) en WiFi Pizarro,
provisionado con `tools/provisioning/pizarro.json` (half-duplex, beam fijo,
token server y syslog en `10.0.0.188`). LiveKit, server Go, agente (Gemini,
MCP de Home Assistant y wake-verify desactivados) y el listener de syslog del
scratchpad, todos en marcha. Grafana en `localhost:3010`.

## Queda abierto (no bloqueante)

1. **El volumen software apenas actúa**: `render_peak=7,3-8,4 M` (fondo de
   escala) tanto a 100 como a 60. Y `gated_peak=32764` dice que el micro satura
   con el altavoz aunque el beam esté fijo. Con el driver arreglado ya no tira
   el device, pero el XVF sigue dejando de contestar bajo altavoz a tope.
2. El coredump a flash no se escribe si el heap está roto o la RAM muy baja
   (dos de tres pánicos). Sigue siendo el mejor instrumento disponible, pero no
   es infalible.
3. Full-duplex sin validar en esta red (todo lo de hoy es half-duplex).
4. Las cinco contradicciones doc↔código del día anterior las corrige el
   commit `docs: correct stale references that contradict the current code`
   (pendiente desde julio en la rama `main` local, subido hoy). Quedan dos:
   `wakeword/okay_nabu.json` dice cutoff 0.62 / ventana 4 (el código usa
   0.95 / 5) y el `README.md` raíz dice "OpenAI Realtime" (el default es
   Gemini).
5. Un `abort()` en la tarea `xvf_ui` durante el `esp_restart()` del
   provisioning (coredump con `pc=panic_abort`, backtrace perdido): carrera
   de apagado, no afecta al uso; unir resumen y backtrace del coredump en una
   sola línea de log para que no se separen.

## Placa nueva (`68:ee:8f:4d:8d:d4`), madrugada del 18

Flasheada y provisionada en Pizarro (`10.0.0.125`) con la misma cadena, pero
sorda: el scan I2C solo veía `0x18`, `could not read XVF version`, todas las
lecturas del AEC fallaban y el anillo se degradaba (`XVF not answering on I2C`).
Y sin embargo el anillo se encendía y seguía al sonido: **el XVF estaba vivo con
el firmware USB de fábrica**. En esa familia el XVF es el maestro del bus I2C
(configura él el códec y el expansor, por eso tampoco aparece `0x21`) y pinta el
anillo con su propia animación DoA. A esa familia no se le puede llegar por
I2C, así que el DFU de nuestro firmware no aplica. La primera unidad venía con la
familia I2C (`i2s_dfu 1.0.4`) y por eso `xvf_dfu.zig` pudo subirla a 1.0.7 sola.

**Arreglo (una vez por placa):** modo seguro del XVF (sin alimentación, MUTE
pulsado, enchufar el **USB-C de la placa ReSpeaker** al Mac, LED rojo
parpadeando) → aparece como `2886:001a reSpeaker DFU Upgrade` →
`dfu-util -R -e -a 1 -D firmware/main/xvf_fw/xvf_master_1.0.7.bin` (es la
misma imagen que el firmware manda por I2C). Script en el scratchpad
(`xvf_usb_dfu.sh`). Después, arrancada por el XIAO: `0x2C ACK`, `XVF firmware
version: 1.0.7 — no DFU needed`, `AEC config applied & verified`. Diagnóstico
rápido para la próxima placa: si el anillo se enciende y sigue al sonido pero el
scan no ve `0x2C`, es esto.
