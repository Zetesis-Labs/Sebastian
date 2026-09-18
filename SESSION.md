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

## Re-provisioning desde la web sin reteclear (mañana del 18)

Rubén: "lo ideal sería enchufar el dispositivo a la web, darle a *load* y que
los datos del device salgan en la interfaz". Hecho y probado en la unidad `68:ee`:

- Firmware: `sebastian.config.get` → `sebastian.config.dump {json}` con lo que
  hay en NVS (sin la contraseña: `wifi.passwordSet`), y **la ventana USB se
  mantiene abierta 120 s** tras cada `get` (`HOLD_AFTER_GET_US`,
  `sebastian_provisioning_hold()` consultado en `app.zig` antes de ceder el PHY
  a TinyUSB). Una config sin `wifi.password` conserva la guardada.
- Web (`web-installer`): botón **Load from device** en el paso 3, el puerto se
  reutiliza entre carga y envío (cuenta atrás visible), el campo de contraseña
  queda **bloqueado** ("Guardada en la placa") y editarla es pulsar **Cambiar**.
  Campos nuevos de syslog (IP/puerto): el firmware ya los guardaba, el
  formulario no los enviaba.

Dos trampas que costaron una vuelta cada una:

- **`usb_serial_jtag_write_bytes` no llega al host en esta placa** mientras la
  consola secundaria (polling) usa el mismo FIFO; el ack `sebastian.config.ok`
  solo llegaba porque el `ESP_LOGI` lo repetía. Las respuestas salen ahora por
  `stdout` (`printf` + `fflush`), que es lo que demostrablemente llega.
- La respuesta llega **troceada por USB**: una regex sobre el buffer acepta la
  primera `}` (fin del bloque `wifi`) como fin del JSON. Solo vale una línea
  completa terminada en salto de línea que parsee.

## Flota: adopción desde el control room (tarde del 18) — rama `feat/fleet-adoption`

Diseño en `docs/implementation/11-…` y especificación funcional en `12-…`;
implementado entero el mismo día (firmware, server, dashboard, instalador).
Probado en Pizarro con tres placas:

- `68ee8f4d8dd4` con el firmware nuevo: se anuncia por mDNS (`dns-sd -B
  _sebastian._tcp`), contesta al `hello` de adopción, y el control room local
  (`casa-mac`, `SEBASTIAN_PUBLIC_API_URL=http://10.0.0.188:8787`) la lista con
  IP, firmware y `cr`.
- `e072a1f96ef0` (tercera placa, provisionada contra cortes) reflasheada sin
  tocar la NVS: aparece como **gestionada por otro control room**
  (`http://10.0.100.10:8787`) con su IP.
- La adopción de una unidad sin secreto de organización exige MUTE en 30 s
  (anillo ámbar); sin pulsación caduca con `consent_timeout`, como manda
  RF-34. Hay que pulsar el botón *mientras* parpadea.

Trampas nuevas:
- La tarea del listener UDP va con pila en RAM interna: escribe en NVS y las
  operaciones de flash deshabilitan la caché de la PSRAM.
- `dnssd` (Go) no reemite los cambios de TXT: el browser se reinicia cada 45 s
  y las entradas caducan a los 90 s; por eso "desaparecer" tarda hasta 90 s.
- En `vite dev` `/installer/` redirige a `/installer` y no hay índice de
  directorio: el instalador embebido se sirve desde una ruta de servidor y se
  construye con `--base /installer/`.
- Los server functions de TanStack Start rechazan `unknown` en el payload: el
  documento de configuración viaja tipado como JSON.

### Nace adoptada (RF-03/RF-51), noche del 18

- Una placa provisionada desde el instalador embebido lleva `org_secret` y no
  `dev_secret`. `control.zig` (`enrollIfNeeded`, antes del primer poll y en
  cada poll hasta que salga) hace `GET /v1/devices/{id}/enroll` → nonce,
  `POST …/enroll {nonce, mac}` con `mac = HMAC(orgSecret, nonce + "." + id)`
  (`sebastian_enroll` en `session_http.c`, HMAC compartido con `adopt.c`), y
  guarda el `deviceSecret` que recibe con `sebastian_provisioning_apply`.
- Server: `device.Service.EnrollChallenge/Enroll` (nonce de un solo uso, 60 s,
  `MarkAdopted` como una adopción en red); 404 sin `SEBASTIAN_ORG_SECRET`.
- Pendiente de probar en placa: hace falta una unidad con secreto de
  organización y sin secreto de altavoz (provisionar desde `/installer` del
  dashboard, o `Olvidar` + re-provisionar por USB).

### Transiciones visibles (noche del 18)

- Estados derivados nuevos en `fleet_view.go`: `joining` (adoptado, sin poll
  desde la adopción, ≤ 3 min), `moved` (la red lo anuncia vinculado a otro tras
  nuestro último poll; acciones Olvidar / Recuperar), `leaving` (olvidado hace
  < 3 min, anuncio viejo). Un poll más reciente que el anuncio manda.
- Descubrimiento: browse cada 15 s, caducidad 60 s (antes 45/90).
- Dashboard: badge por tono, chips (ip/fw/perfil), cronología "último contacto
  · adoptado · en la red (vinculado a X)" en fila y ficha; refresco cada 5 s.
- Prueba de secretos distintos: `server2.env` (casa-2) lleva
  `otra-organizacion-2026`; el campo de secreto de altavoz está en "Adoptar
  aquí" y "Adoptar por IP".

### El secreto nace con el altavoz (noche del 18)

- `sebastian_ensure_device_secret` lo genera en el primer arranque; adoptar no
  lo cambia: la placa lo entrega en `{"t":"ok","dev":…}` y en el enrol
  (`POST …/enroll {nonce, mac, deviceSecret}`); el server guarda el hash.
  `adoptionConfig` ya no lleva `deviceSecret`; solo lo lleva una rotación
  (RF-53, opcional) o un forget (vacío → borrar; regenera al arrancar).
- `cr_bound` en NVS recuerda con qué control room está dado de alta; un token
  URL distinto o un 401 en el poll fuerzan re-enrol.
- `sebastian.config.get` devuelve `adoption.deviceSecret` (placa en mano); el
  instalador lo enseña en el campo avanzado "Device secret".
- Placas con firmware anterior: la adopción falla con `no_secret` → reflashear.

### RF-42: la placa reporta lo que ejecuta (noche del 18)

- `sebastian_report_running_config` (session_http.c) hace PUT del volcado
  sin secretos (`config_dump(false)`) una vez por arranque, reintentando en
  cada poll; el server lo guarda en `devices.running_config(_at)` (migración
  `20260918145510_running_config`). La ficha parte de la deseada o, si no
  hay, de la reportada, y marca en ámbar los campos que difieren.

### Cierre de la funcional de flota (tarde-noche del 18) — rama `feat/fleet-pending`

PR #46 mergeado a main por rebase; la spec 13 (grabaciones de reuniones) está
en main. Lo que quedaba de la spec 12, todo hecho salvo hardware:

- RF-67 (reset físico) descartado: el rescate es el USB.
- Eventos de la placa (RF-36/42): nueva clave TXT `ev` y parámetro `ev=` en el
  poll (`adopt-denied:<ip>`, `cfg-rejected:<why>`, `wifi-rollback`); antes el
  `err` del anuncio lo pisaba el `ok` del siguiente poll. El server lo guarda
  en `devices.last_event(_at)` (migración `20260918160146_last_event`; la
  hora es el primer contacto que lo trajo) y lo funde con la LAN (gana el más
  nuevo). Fila y ficha lo cuentan con `eventMessage`; el `cfg-rejected`
  desaparece cuando la placa ejecuta la versión deseada.
- RF-64: `sebastian_adopt_accepted()` → anillo ámbar fijo 2 s antes del
  reinicio (`ACCEPTED_SHOW_MS`).
- Olvidar en la ficha (RF-37): panel "Devolver el altavoz a fábrica" con
  confirmación tecleando la MAC, sin diálogo del navegador.
- RF-52: la línea del trabajo de adopción abre `SecretDialog` (copiar + QR).
- RF-04: el instalador enseña "Firmware <versión>" del manifest; el workflow
  `dashboard-publish` estampa `project_version` del build en el manifest.
- RF-12: `/token` legado retirado en server (contrato, handler, config
  `SEBASTIAN_LEGACY_*`, helm, devcontainer, README) y firmware (`token.zig`
  solo `POST /v1/sessions`; borrados `token_http.c/.h` y `token_core.zig`).
- Outbox: `appendEvent` en `postgres/store.go` unifica sesión/grabación y
  añade `device.adopted`, `device.forgotten`, `device.secret_regenerated`,
  `device.config_desired` (test de integración `TestDeviceLifecycle…`).
- mDNS: el fin de ciclo (`DeadlineExceeded`) ya no se loguea como fallo ni
  añade 10 s de pausa.

Probado en `e072a1f96ef0` (flasheada 18:15; el puerto solo asoma al
reenchufar el USB del XIAO, el flasher se arma antes): `adopt-denied:10.0.0.188`
desde casa-2 y `cfg-rejected:schema` (desired-config con `sebastian.config.v2`)
llegaron por el poll con su hora y se leen en fila y ficha; el rechazo se
oculta al retirar la deseada. Sin probar a ojo: el ámbar fijo 2 s (nadie
miraba el anillo). `68ee8f4d8dd4` y `e072a1f895f4` siguen con firmware anterior.

### Noche del 18: `68ee` adoptada, agente reparado — rama `fix/consent-born-secret` (PR #48)

- Consentimiento (RF-34): una unidad con secreto propio pero sin secreto de
  organización ni control room pedía nada y rechazaba a todos; ahora pide
  MUTE. Probado: `waiting_consent` → MUTE → `adopted`.
- Los dos servers Go llevaban el día a 550 % de CPU: `dnssd` solo para sus
  lectores con `context.Canceled`, y el ciclo de browse caducaba con
  `DeadlineExceeded` → dos goroutines girando por ciclo. Ahora el ciclo se
  cancela desde un timer; test de regresión cuenta goroutines.
- El agente Python esperaba al participante `esp32-respeaker` (cuatro módulos);
  con las unidades entrando por su MAC no las veía y cerraba la sesión a los
  4 s. Ahora toma `device_id` de los metadatos del job
  (`device_identity.py`, puro, con tests). "Okay Nabu" vuelve a funcionar en
  `68ee`. Un `uv run agent.py dev` lanzado por una sesión anterior se había
  quedado zombi (sin conexión a LiveKit): matar y relanzar.
- Pruebas de hardware con `68ee` (21:10-21:16): **RF-45** WiFi falsa → a los
  ~70 s vuelve a `Pizarro` y reporta `wifi-rollback` (la versión en ejecución
  coincide con la deseada aunque la WiFi no; la ficha lo enseña campo a
  campo). **RF-53** regenerar → la placa coge el secreto nuevo en su siguiente
  config, reinicia y se autentica con él (pendiente promovido en DB).
  **RF-38** casa-2 la adopta por IP con ese secreto sin secreto de organización
  → `adopted` allí, `moved` aquí; «Recuperar» desde casa-mac con el mismo
  secreto la devuelve. Queda sin ver a ojo el ámbar fijo (RF-64).

## Grabación de reuniones (spec 13, diseño 14) — bloque A en `feat/meetings-a`

- Doc 14 en main: seis bloques (A server, B placa, C agente, D transcripción,
  E control room, F voz), 48 kbit/s con troceo para OpenAI (25 MB/petición,
  Ogg → WebM con `-c copy`), audio en el volumen del server.
- Bloque A hecho con suite antes de código: `internal/meeting/core.go`
  (máquina de estados pura, T-A1), `meeting.go` (cáscara: orden UDP `cmd`
  firmada con el secreto de organización o cabecera `X-Meeting` del poll,
  confirmación `PUT /v1/devices/{id}/meeting`, audio en streaming reanudable
  por `Content-Range`, ticks: 30 s sin confirmar → se borra; 30 s sin audio →
  `cut`; máximo → `closing`), store Postgres (`meetings`, migración
  `20260918195057_meetings`) con eventos `meeting.*` al outbox, contrato
  OpenAPI, handlers y dos rutas crudas fuera del router generado (subida
  con plazo de lectura desactivado y descarga con `Range`). Sesiones aceptan
  `{kind:"meeting", meetingId}` → `mode:"meeting"` en los metadatos del agente.
- Trampa: `ReadTimeout` de 15 s del servidor HTTP mataría la subida; por eso
  la ruta cruda levanta el plazo con `http.NewResponseController`.
- Humo con `68ee` (firmware sin bloque B): 202 → `X-Meeting: start:<id>` en
  su poll → orden UDP sin respuesta → caída a los 30 s. Env locales llevan
  `SEBASTIAN_AGENT_SECRET`.
- Siguiente: bloque B (firmware): `gesture_core.zig` sobre `GPI_READ_VALUES`
  (resid 36; spike 0: qué bit es el botón), orden `cmd` en `adopt.c`,
  `X-Meeting` en `control.zig`, sesión modo reunión, anillo rojo.

## Reuniones, bloques B y C (madrugada del 19) — rama `feat/meetings-b`, todo el spec 13 en un solo PR

- B (firmware) probado en `68ee`: orden LAN `cmd` firmada, `X-Meeting` del
  poll, gesto corta+larga (POST `/meetings` desde la placa), anillo rojo
  fijo/atenuado/aviso, sesión de reunión con `mode:"meeting"`; pila del
  main 3584 → 8192 (desbordó al abrir la sesión). Spike: `GPI_READ_VALUES`
  solo responde a lecturas de 4 bytes; botón = byte 0 bit 0, activo a bajo.
- C (agente): `agent/meeting_mode.py` — sin `AgentSession` (RM-12), captura
  48 kHz → `ffmpeg` Ogg/Opus 48k → spool local → `PUT …/audio` reanudable
  (`HEAD …/audio` da byte y estado); `SilenceNet` pura + VAD silero →
  `POST …/warn` y `…/stop {silence}`. Server: rutas crudas `HEAD/stop/warn`
  con `X-Agent-Secret`, `Service.Warn` (`record-warn` solo por LAN), EOF sin
  bytes no cierra, y los eventos de la subida se aplican a la reunión actual
  (bug: la parada desde la ficha acababa en `cut`). `tasks.spawn` ya loguea
  las excepciones de tareas de fondo. Imagen del agente con `ffmpeg`; chart:
  `SEBASTIAN_API_URL` (servicio del server) y `SEBASTIAN_MEETING_SILENCE_S`.
- Env local: `agent/.env` lleva `SEBASTIAN_API_URL` y `SEBASTIAN_AGENT_SECRET`.
- Trampa: `aiohttp` reintenta un `PUT` con cuerpo vacío si el server cierra la
  conexión; `_retry_connection = False`.
- Siguiente: bloque D (transcripción por piezas WebM ≤ 20 MB con
  `gpt-4o-transcribe-diarize`, resumen, retención, borrado) y E (control room).

## Reuniones, bloque D (madrugada del 19) — misma rama

- `server/internal/transcribe`: `Pieces`/`Merge`/`KnownSpeakers` puros,
  cliente OpenAI (multipart `diarized_json`, reserva `whisper-1`, resumen
  `json_schema` con `gpt-5.4-mini`, reintentos 5xx/429, 4xx definitivo),
  `Job` de un worker con recuperación al arrancar, `FFmpeg` (corte Ogg
  `-c copy`, clips WAV para `known_speaker_references`). Dominio:
  `meeting.Transcript`/`Summary`, `Rename`, `TXT`/`SRT`, `Expired` (retención).
  Servicio: `Transcribed/TranscriptFailed/Summarized/Patch/Resummarize/Retain`.
  Store: jsonb, `q` ILIKE, `EndedBefore`. API: PATCH, transcript txt/srt,
  transcribe, summarize, `q`. Chart: `retentionDays`, `summary`,
  `summaryModel`, `OPENAI_API_KEY` opcional. Dockerfile: ffmpeg estático.
- Hallazgo: OpenAI ya acepta Ogg → sin remultiplexado a WebM.
- Probado: al relanzar el server, el job transcribió las 3 reuniones de C
  (2 hablantes, es, resúmenes). Env local `server.env` lleva `OPENAI_API_KEY`.
- Siguiente: bloque E (control room): lista/ficha de reuniones, reproductor
  + transcripción sincronizada, txt/srt, renombrar, buscar, parar/borrar,
  botones en la ficha del altavoz, `meetingSilenceMin`/`meetingMaxHours`.
