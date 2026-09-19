# Compilar, configurar y ejecutar Sebastian

Guía del código hasta `84f278b6` (2026-09-19). El estado de integración está en
[RECENT_CHANGES.md](RECENT_CHANGES.md); reuniones B–F pertenece al PR #50 abierto.

## Entorno

Usar el [devcontainer](DEVCONTAINER.md) para compilar, instalar dependencias,
ejecutar servicios y pasar pruebas. El host macOS solo necesita acceso USB y
`uv` para el flasheo/bridge. No necesita compilar ESP-IDF.

El Compose levanta PostgreSQL, base auxiliar de Atlas, NATS, LiveKit y
observabilidad. Antes de abrirlo, definir en `.devcontainer/.env` (ignorado por Git):

```dotenv
LIVEKIT_NODE_IP=<IP LAN del host accesible desde la placa>
LIVEKIT_API_SECRET=<secreto local compartido con API y agente>
```

El SFU anuncia esa IP para medios. La placa debe poder alcanzar señalización
`:7880` y medios UDP `:7882`; la API se publica en `:8787` y el panel en `:3001`.

## Configuración de servicios

El Compose proporciona las conexiones a PostgreSQL/NATS/LiveKit y secretos de
administración/agente **de desarrollo**. Para adopción, configurar además en el
proceso Go:

```dotenv
SEBASTIAN_PUBLIC_API_URL=http://<IP LAN del host>:8787
SEBASTIAN_CONTROL_ROOM_NAME=<nombre del entorno>
SEBASTIAN_ORG_SECRET=<secreto de organización de este entorno>
```

Para conversación, preparar `agent/.env` a partir de
[.env.example](../agent/.env.example), con `GOOGLE_API_KEY` si se selecciona
Gemini o `OPENAI_API_KEY` si se selecciona OpenAI. `LIVEKIT_*` debe apuntar al
mismo SFU que usa Go; el ejemplo conserva un placeholder de Cloud que debe
adaptarse si se usa el SFU local. Home Assistant por MCP es opcional.

Para reuniones:

- Agente: `SEBASTIAN_API_URL`, `SEBASTIAN_AGENT_SECRET` y ffmpeg en PATH.
- Servidor: el mismo `SEBASTIAN_AGENT_SECRET`, directorio escribible
  `SEBASTIAN_MEETINGS_DIR`, ffmpeg y `OPENAI_API_KEY` para transcripción/resumen.
- La parada por voz de reunión también usa OpenAI desde el agente.
- Sin clave de transcripción en el servidor, el audio puede guardarse y la
  reunión termina en `no_transcript`.

Las imágenes de agente y servidor incluyen ffmpeg. El Dockerfile del devcontainer
no lo instala actualmente: debe estar disponible en el entorno de desarrollo
para ese recorrido. El [README del servidor](../server/README.md) documenta las
variables y valores por defecto.

## Arranque local

Dentro del contenedor, desde `/workspace`, en terminales separados:

```bash
make server-migrate
make server-run
```

```bash
make server-outbox
```

```bash
make agent
```

```bash
make dashboard-dev
```

La migración se ejecuta antes de usar la API. NATS puede estar temporalmente
inaccesible sin perder los eventos ya guardados en el outbox. Comprobar
`/healthz` y `/readyz` de la API; readiness verifica PostgreSQL, no todo el
recorrido de audio.

Mantener identificado el worker local para evitar procesos de desarrollo
olvidados conectados al mismo entorno. El servidor hace **dispatch explícito por
sesión**: no hace falta borrar salas ni resetear la placa para cada conversación.
`make token` y `agent/token_server.py` son utilidades antiguas, no el recorrido
actual de firmware/Go.

## Compilación y flasheo

Dentro del devcontainer:

```bash
make fw-test
make fw-build
```

El CI usa ESP-IDF 5.4.4 y el fork Zig `0.16.0-xtensa`. Revisar la versión local
si se reutiliza una imagen antigua del contenedor. No volver a 5.4.0: la sesión
de septiembre documenta pánicos del driver I2C ante timeout.

En el host, desde la raíz de este mismo worktree:

```bash
make flash
```

Para elegir puerto explícitamente:

```bash
tools/flash.sh /dev/cu.usbmodemXXXX
```

El script usa `firmware/build/flash_args` generado por el contenedor y detiene
el bridge serie antes de flashear. El puerto cambia al reenumerar; no asumir un
nombre fijo. Si el USB está en modo micrófono o no sincroniza, reconectar y, si
hace falta, mantener BOOT del **XIAO** al conectarlo para entrar al bootloader.
El botón RESET de la placa ReSpeaker resetea el **XVF**, no el ESP32.

El firmware comprueba la versión XVF e incluye la imagen `inthost` 1.0.7 para
DFU por I2C. Algunas unidades llegan con la familia USB, que no expone el control
I2C esperado: requieren la conversión inicial descrita en [XVF3800.md](XVF3800.md).
Este procedimiento no es OTA del ESP32.

## Aprovisionamiento y adopción

Abrir `/installer/` desde el dashboard o usar el instalador público. WebSerial
requiere un navegador que exponga `navigator.serial` y un contexto permitido
(HTTPS o localhost); la disponibilidad concreta se comprueba en el navegador.

El instalador permite leer la configuración guardada antes de editarla. La
lectura no devuelve la contraseña WiFi; omitirla conserva la existente. La
ventana serie inicial es de 5 s; una lectura de configuración la amplía a 120 s
antes de que TinyUSB tome el periférico.

WiFi, URL del control room, perfiles y credenciales de adopción viven en NVS.
El campo mantiene el nombre `livekit.tokenServerUrl`, pero el firmware deriva su
origen y llama a **`POST /v1/sessions`** con identidad MAC y secreto propio. Una
ruta `/token` guardada por una versión anterior no implica uso del endpoint
legado, que se retiró del servidor Go.

El instalador embebido prepara la pertenencia a la organización. También puede
adoptarse por descubrimiento o IP; una unidad sin organización/control room pide
consentimiento MUTE. Verificar después en la ficha identidad, perfil, contacto y
configuración ejecutada frente a deseada. Ver
[protocolo](../web-installer/public/PROVISIONING.md).

## Verificación y diagnóstico

- Conversación: decir «Okay Nabu», comprobar unidad y agente en la sala, voz en
  ambos sentidos, interrupción y vuelta a escucha local al cerrar.
- USB: seleccionar `Sebastian Mic`, comenzar captura y comprobar cesión y retorno
  del micrófono al detenerla.
- Reuniones: seguir la matriz de [TESTING.md](../TESTING.md), incluyendo parada,
  audio, transcripción y órdenes por voz en el firmware de esta rama.
- Logs: syslog del dispositivo si está configurado, OTel del agente y logs de
  API/LiveKit. El resumen de un pánico se emite al siguiente arranque.

Para diagnóstico de conversación, `SEBASTIAN_RECORD=1` guarda en
`SEBASTIAN_RECORD_DIR` (por defecto `recordings`) los archivos `_model.wav` y
`_agent.wav`; `SEBASTIAN_RECORD_TRACK=1` añade `_mic.wav`. El primero incluye lo
que recibe el modelo, incluido pre-roll; la pista cruda comienza más tarde.
Estos WAV no son las reuniones del panel ni tienen su retención.

Escuchar las muestras además de medir niveles. Registrar commit, unidad,
configuración e IDs de sesión/reunión al documentar resultados.
