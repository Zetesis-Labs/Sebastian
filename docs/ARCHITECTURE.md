# Arquitectura de Sebastian

Referencia: código hasta `84f278b6`, 2026-09-19. Consultar
[STATUS.md](STATUS.md) para distinguir implementación y validación.

## Componentes y responsabilidades

| Componente | Responsabilidad | Fuente principal |
|---|---|---|
| ESP32-S3 + XVF3800 | Audio, activación local, sesiones, USB, LEDs y configuración NVS. | [app.zig](../firmware/main/app.zig) |
| LiveKit | Transporte de audio WebRTC y mensajes entre dispositivo y agente. | [chart](../helm/sebastian/) |
| Agente Python | Conversación y herramientas, o captura dedicada según el modo de la sesión. | [agent.py](../agent/agent.py) |
| API Go | Flota, sesiones, reuniones y coordinación con LiveKit. | [cmd/api](../server/cmd/api/) |
| PostgreSQL | Estado actual, transcripciones, auditoría y outbox transaccional. | [schema.sql](../server/db/schema.sql) |
| Worker outbox + NATS | Publicación durable de eventos CloudEvents. | [cmd/outbox](../server/cmd/outbox/) |
| Dashboard | Interfaz SSR y llamadas administrativas al servidor. | [dashboard](../dashboard/) |
| Instalador | Firmware de fábrica y configuración por WebSerial. | [web-installer](../web-installer/) |

El backend admite LiveKit propio o Cloud. El chart incluye el SFU propio.
El alojamiento propio del transporte no elimina las llamadas a proveedores de
modelos para conversación, verificación de activación o transcripción.

## Hardware y recorrido de audio

El XVF3800 procesa cuatro micrófonos con beamforming, AEC y postprocesado. Es el
maestro I2S a **48 kHz, estéreo y slots de 32 bits**. El ESP32-S3 usa dos
periféricos esclavos independientes para evitar los fallos de DMA observados con
un único canal dúplex.

| Bus/señal | Conexión |
|---|---|
| I2C | SDA GPIO5, SCL GPIO6; XVF `0x2C`, AIC3104 `0x18`. |
| I2S compartido | WS GPIO7, BCLK GPIO8; MCLK no utilizado. |
| Micrófono | RX `I2S_NUM_1`, DIN GPIO43. |
| Altavoz | TX `I2S_NUM_0`, DOUT GPIO44, AIC3104 como DAC. |

La captura extrae **LEFT/comms**, convierte a PCM de 16 bits y publica Opus mono
48 kHz. `mic_src.read_frame()` lee I2S al ritmo del consumidor; no hay un productor
libre con un anillo intermedio que derive respecto al reloj. El anillo de pre-roll
es una estructura distinta, usada en la activación.

La reproducción pasa por `av_render` y `esp_codec_dev` hacia el AIC3104. El volumen
base configurado en `board.zig` es 100. La antigua cifra 35 correspondía a una
etapa en la que el callback de volumen era inefectivo.

`xvf_aec.applyConfig()` escribe y comprueba la configuración de referencia y haz.
El firmware permite full-duplex si se cumplen sus precondiciones y fuerza
half-duplex si fallan. Como esa rebaja ocurre en ejecución, el modo *efectivo*
solo lo conoce la placa: lo declara al agente en la cabecera del pre-roll. En
half-duplex, el agente recibe silencio durante la reproducción; la detección de
interrupción conserva acceso al audio local y la vía para cortar al agente es la
palabra de activación oída sobre su propia voz. El detector de voz superpuesta
del agente solo se arma cuando la placa declara full-duplex.
Los ajustes y la calidad acústica requieren pruebas con la placa.

El ESP32 también ejecuta conversión de PCM, filtrado/decimación para activación,
inferencia microWakeWord y arbitraje. El procesamiento pesado de eco y ruido vive
en el XVF. La fuente de parámetros es [config.zig](../firmware/main/config.zig),
con overrides NVS y de perfil.

## Conversación

1. En reposo, `wakeword.zig` alimenta el modelo **Okay Nabu** con audio a 16 kHz
   decimado desde 48 kHz mediante FIR. No mantiene una sala conversacional abierta.
2. Al detectar activación, `token.zig` abre `POST /v1/sessions` con MAC normalizada
   y secreto de la unidad. Go persiste la sesión y su evento, genera credenciales
   temporales y solicita el despacho explícito del agente en una sala nueva.
3. La placa espera conexión y presencia del agente, detiene el lector de
   activación y transfiere la captura al recorrido LiveKit. Envía primero el
   pre-roll por byte stream fiable con cabecera `SBPR`: PCM mono de 16 kHz,
   limitado a 2,5 s aunque el anillo retenga hasta 12 s. La cabecera lleva los
   indicadores del modo dúplex en vigor; un firmware anterior no declara nada y
   el agente se abstiene.
4. El agente antepone ese audio a la entrada en directo, descarta el silencio
   inicial de la compuerta y adapta los frames para el modelo.
5. El agente publica estado de escucha/habla/interrupción. Una interrupción
   también vacía el audio pendiente de reproducción del dispositivo.
6. El agente decide el cierre conversacional por inactividad o herramienta de
   despedida. Envía el cierre al dispositivo y limpia la sala. El firmware añade
   límites y watchdog propios para recuperar sesiones atascadas.

La lógica de sesión separa eventos y efectos mediante
[session_reducer.zig](../firmware/main/core/session_reducer.zig).
Borrar una sala desde el servidor no garantiza por sí solo que el SDK del
ESP32 haya abandonado su estado de conexión.

## Agente de voz

`SEBASTIAN_MODEL_PROVIDER` selecciona `gemini` (por defecto) u `openai`.
Los modelos y voces son configurables; los valores concretos viven en
[agent.py](../agent/agent.py) y [.env.example](../agent/.env.example).
La selección es explícita, no un failover automático entre proveedores.

Home Assistant se conecta opcionalmente por MCP. La entrada personalizada añade
pre-roll, VAD para interrupciones y verificación remota de la activación. BVC se
elige automáticamente para LiveKit Cloud salvo override; no está disponible por
defecto en el SFU propio. La verificación remota y la grabación de diagnóstico
son funcionalidades separadas de las reuniones.

`control_plane.py` mantiene la operación `announce`: busca una sala activa con
unidad y agente, publica el texto y el agente espera un periodo de reposo para
hablar. Este recorrido necesita una sesión existente.

## Perfiles y USB

Los perfiles **locales del firmware** guardan modo y ajustes en NVS. Los perfiles
base son `agente` y `micro-usb`; existen selección al arrancar y protección contra
bucles de arranque. Los **perfiles del agente** administrados por Go son otra
entidad, asociada al despacho y configuración del backend.

La interfaz TinyUSB UAC2 entrega PCM mono de 48 kHz y 16 bits. En perfil agente,
el árbitro concede el micrófono cuando el host realmente empieza a capturar;
puede cerrar una sesión para cederlo y reanuda escucha local cuando el host para.
El perfil `micro-usb` funciona sin conversación y permite red para administración.

TinyUSB comparte el periférico USB con la consola de aprovisionamiento: al inicio
hay una ventana serie de 5 s, extensible a 120 s al leer configuración desde el
instalador. Ver [USB_MIC.md](USB_MIC.md) y [PROFILES.md](PROFILES.md).

## Flota y configuración

- La MAC normalizada identifica la unidad. El dispositivo genera su propio
  secreto; el servidor conserva su hash para autenticación.
- mDNS anuncia `_sebastian._tcp`. La vista de flota combina descubrimiento,
  inventario y contactos recientes; presencia y adopción son estados distintos.
- La adopción usa un intercambio UDP firmado. Según las credenciales presentes,
  requiere secreto de organización, secreto de unidad o consentimiento físico.
- Una unidad provisionada para la organización puede incorporarse mediante
  desafío HMAC de un solo uso y entregar su secreto al servidor.
- El sondeo periódico comunica perfil/configuración y recibe versiones deseadas.
  La unidad descarga cambios y reporta lo que realmente ejecuta, sin secretos.
  Un cambio de WiFi remoto tiene prueba y reversión si no consigue conexión.

Fuentes: [device](../server/internal/device/), [adoption](../server/internal/adoption/),
[control.zig](../firmware/main/control.zig) y
[especificación funcional](implementation/12-fleet-adoption-functional-spec.md).

## Reuniones

El servidor gobierna una entidad `meetings`, separada de `recordings`.
El inicio llega desde panel, gesto o herramienta de voz. La orden se envía por
UDP firmado y puede recuperarse mediante la cabecera `X-Meeting` del sondeo.
La placa abre una sesión con `mode=meeting`; Python entra en `meeting_mode.py`
sin crear el `AgentSession` conversacional.

La captura recibe PCM de LiveKit y lo codifica mediante ffmpeg a **Ogg/Opus,
48 kbit/s**. Mantiene spool local y sube por HTTP al servidor; `HEAD` consulta el
offset y `Content-Range` permite reanudar. El audio se guarda en su directorio de
reuniones; la base de datos conserva estado, metadatos, transcripción y resumen.

Recorrido normal: `requested → recording → closing → transcribing → ready`.
Hay estados `cut` por pérdida de audio y `no_transcript` por fallo de transcripción.
Los límites de duración y silencio son configurables. Las órdenes por voz tienen
un recorrido específico: iniciar desde conversación; durante reunión, detectar
activación, transcribir una ventana corta y reconocer la intención de parada.

El worker Go transcribe por fragmentos acotados, combina marcas temporales y
hablantes, y genera el resumen. La retención por defecto es 90 días, con exclusión
para reuniones marcadas para conservar. El dashboard ofrece búsqueda,
reproductor, transcripción sincronizada, nombres de hablantes y exportación TXT/SRT.
Actualmente carga el audio mediante `fetch` y `blob:`; el servidor también admite
`Range`. Queda pendiente la validación manual del reproductor.

Fuentes: [meeting](../server/internal/meeting/),
[transcribe](../server/internal/transcribe/),
[meeting_mode.py](../agent/meeting_mode.py) y
[diseño y diferencias respecto al plan](implementation/14-meeting-recordings-technical-design.md).

## Persistencia, acceso y despliegue

PostgreSQL es la fuente de verdad. Cada operación que publica hechos guarda su
outbox en la misma transacción. El publicador espera ACK de JetStream antes de
marcar entrega; reintenta con backoff y deduplica por identificador del evento.
NATS no sustituye el estado persistido ni transporta el audio de reuniones.

El contrato fuente es [openapi.yaml](../server/api/openapi.yaml), del que se generan
los tipos Go y TypeScript. Atlas genera migraciones desde el esquema SQL; las
aplica un proceso separado con bloqueo de base de datos.

La API distingue secretos de administración, unidad y agente. El dashboard envía
el secreto administrativo desde SSR, pero **todavía no tiene login propio**:
el acceso al panel depende de la protección de su red/ingress. El instalador
embebido entrega datos de aprovisionamiento al navegador y comparte ese límite
de confianza.

El chart incluye LiveKit, NATS, API/outbox, dashboard, agente y control plane.
Mileto configura ArgoCD, secretos e imágenes. La configuración versionada no
acredita que esta rama esté desplegada. El audio de reuniones requiere su volumen
persistente además de PostgreSQL; respaldar solo la base no conserva el audio.

La observabilidad combina OTel del agente, syslog UDP del dispositivo y lectura
serie en desarrollo. El firmware guarda información de pánicos en flash y la
reporta al arrancar. UDP puede perder logs; las métricas acústicas y las pruebas
de larga duración siguen necesitando evidencia específica.
