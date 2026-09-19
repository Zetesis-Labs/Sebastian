# Firmware de Sebastian

Referencia: `84f278b6`, 2026-09-19. La aplicación usa Zig sobre ESP-IDF y el SDK
LiveKit C. El CI fija ESP-IDF **5.4.4** y el fork Zig **0.16.0-xtensa**.
C/C++ conserva los adaptadores a IDF y el motor TFLite/microWakeWord.

## ABI y compilación

`cmake/zig.cmake` descarga el compilador verificado por SHA256, compila
`main/app.zig` a un objeto y lo enlaza en el componente `main` de ESP-IDF.
Los bindings de [csdk.zig](../firmware/main/csdk.zig) son `extern` escritos a
mano: no se generan con `@cImport`/translate-c.

[abi_check.c](../firmware/main/abi_check.c) contrasta tamaños/offsets contra los
headers C; Zig añade comprobaciones en compilación. Al cambiar IDF o una
estructura compartida, revisar ambos lados. El build completo detecta errores
que una prueba de un módulo puro no puede detectar.

## Módulos

| Módulo | Responsabilidad |
|---|---|
| `app.zig` | Arranque, perfiles, conversación, reuniones, handoff de audio y watchdog. |
| `board.zig` | I2C, AIC3104, I2S RX/TX separados y dispositivos codec. |
| `xvf_dfu.zig` | Control I2C serializado, versión/DFU del XVF, mute y LEDs. |
| `xvf_aec.zig` | Configuración con lectura de comprobación y probes acústicos. |
| `mic_src.zig` / `xvf_pcm.zig` | Captura acompasada al consumidor y conversión de muestras. |
| `wakeword.zig` / `components/mww` | Modelo Okay Nabu, frontend, FIR y detección local. |
| `pre_roll.zig` | Audio previo a la activación en PSRAM y envío SBPR limitado. |
| `profile.zig` / `profiles.c` | Perfiles NVS, selector y contabilidad de fallos de arranque. |
| `usb_mic.zig` | Micrófono TinyUSB UAC2 y convivencia con el agente. |
| `token.zig` / `session_http.c` | Sesiones autenticadas e intercambios HTTP con Go. |
| `control.zig` | Sondeo, configuración deseada, reporte de configuración y órdenes de reunión. |
| `adopt.c` / `announce.c` | Adopción firmada UDP y anuncio mDNS. |
| `provisioning.c` | Lectura/escritura de configuración, NVS y conexión/reversión WiFi. |
| `xvf_ui.zig` | Anillo, consentimiento, estados y gestos del botón. |
| `log.zig` / `syslog_sink.c` / `coredump_report.c` | Logs, syslog y diagnóstico de pánicos. |
| `core/` | Lógica pura de sesión, compuerta, perfiles, arbitraje USB, gestos y DSP. |

## Arranque y modos

El arranque inicia aprovisionamiento y perfiles, prepara la placa/XVF, aplica
configuración y abre la ventana serie antes de que TinyUSB reclame el USB.
Una lectura de configuración prolonga esa ventana. Después ejecuta el perfil
`agente` o `micro-usb`.

En agente se construye el recorrido de audio, se conecta WiFi, se activa syslog
si está configurado y se inicia el control por red. El modelo y el anillo de
pre-roll quedan preparados para el ciclo de activación. Una orden de reunión
abre su recorrido específico. Un fallo de arranque del agente permite conservar
el servicio de micrófono USB.

La contabilidad de arranques puede seleccionar un perfil agente de recuperación
en memoria tras tres fallos; no sobrescribe la preferencia guardada. Esto no es
un sistema OTA ni garantiza recuperación de cada fallo de hardware.

## Audio y memoria

- XVF como maestro I2S a 48 kHz/32 bits/estéreo; ESP esclavo con RX en
  `I2S_NUM_1` y TX en `I2S_NUM_0`.
- Canal de micrófono compilado: **LEFT/comms**. Modo dúplex y haz se pueden
  sobrescribir desde NVS/perfil; el canal no cambia sin recompilar.
- Captura directa desde el consumidor para evitar deriva entre relojes.
- Reproducción mediante `av_render`, con colas acotadas. Volumen base 100.
- Arena de inferencia de 40 KB en **PSRAM**, junto a otros buffers que no
  necesitan DMA interno. La convivencia TinyUSB/LiveKit depende de ese margen.
- Anillo de pre-roll de 12 s; ventana enviada de hasta 2,5 s/80 KB. Una conexión
  lenta no debe convertir toda la espera en un envío mayor que la caché SCTP.
- Probes de AEC apagados mediante constantes de compilación: convertirlos en
  flags runtime mantiene buffers internos que pueden impedir el transporte.

Las operaciones de control XVF mantienen un lock durante solicitud y respuesta.
Si el chip deja de contestar, la tarea del anillo reduce consultas para no
monopolizar el bus. El CI 5.4.4 incorpora la corrección del driver I2C que motivó
los pánicos investigados en septiembre.

## Configuración y credenciales

El instalador envía `sebastian.config.v1`; el firmware valida, guarda en NVS y
reinicia. `sebastian.config.get` devuelve la configuración existente sin la
contraseña WiFi. La unidad genera su secreto y lo comparte con el control room
al adoptar; no lleva claves API de LiveKit ni de modelos.

`tokenServerUrl` conserva su nombre histórico. Se deriva su origen para llamar a
`POST /v1/sessions` con `X-Device-Id` y `X-Device-Secret`. El recorrido legado
`token_http.c` se retiró. Ver [protocolo de aprovisionamiento](../web-installer/public/PROVISIONING.md).

## Pruebas, build y flasheo

Dentro del devcontainer, desde `/workspace`:

```bash
make fw-test
make fw-build
```

En el host macOS, desde la raíz del worktree:

```bash
make flash
```

[BUILD_AND_RUN.md](BUILD_AND_RUN.md) cubre recuperación USB y conversión de
familia del firmware XVF. [TESTING.md](../TESTING.md) distingue pruebas lógicas,
compilación y comprobaciones en placa.
