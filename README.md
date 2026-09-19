# Sebastian

Altavoz de voz sobre **Seeed ReSpeaker XVF3800 + XIAO ESP32-S3**, con tres usos:
asistente conversacional, micrófono USB y grabador de reuniones. Incluye firmware,
agente de voz, servidor de administración, dashboard e instalador web.

Repositorio: `github.com/Zetesis-Labs/Sebastian`.

## Estado actual

Referencia documental: **2026-09-19**, código hasta `84f278b6` en
`feat/meetings-b`. Esta referencia incluye cambios de reuniones aún separados de
`main`; no certifica qué versión está desplegada.

La conversación, el micrófono USB y la gestión de flota tienen implementación.
La rama añade el recorrido de reuniones: captura, subida reanudable, transcripción
por hablantes, resumen y consulta desde el panel. Las notas de sesión registran
pruebas en placa de captura y en navegador del panel; quedan pendientes la prueba
en placa de las órdenes por voz y la reproducción manual del audio.

[Estado y pendientes](docs/STATUS.md) distingue código, evidencia de pruebas y
trabajo abierto. [ROADMAP.md](ROADMAP.md) fija las prioridades de estabilización.

## Componentes

| Directorio | Responsabilidad |
|---|---|
| [firmware/](firmware/) | Zig sobre ESP-IDF y SDK LiveKit C: hardware, audio, activación local, sesiones, USB y configuración persistente. |
| [agent/](agent/) | Python con LiveKit Agents: conversación Gemini/OpenAI, Home Assistant por MCP y captura de reuniones. |
| [server/](server/) | Go/OpenAPI: flota, sesiones, reuniones, transcripción, PostgreSQL y outbox hacia NATS JetStream. |
| [dashboard/](dashboard/) | React/TanStack Start: administración SSR de dispositivos, grabaciones y reuniones. |
| [web-installer/](web-installer/) | Instalación y configuración por WebSerial; disponible también dentro del dashboard. |
| [helm/sebastian/](helm/sebastian/) | Chart del backend; configuración de despliegue en Mileto. |

## Cómo funciona

En conversación, **«Okay Nabu» se detecta localmente**. El dispositivo abre una
sesión autenticada en Go; el servidor crea una sala LiveKit nueva y despacha el
agente. La placa entrega el audio previo a la activación y publica el micrófono.
El agente responde por la misma sala. Al cerrar, la placa vuelve a la escucha local.

El XVF3800 procesa los cuatro micrófonos y genera el reloj I2S a 48 kHz. El ESP32
usa puertos separados para captura y reproducción. El canal actual es
**LEFT/comms**, con procesamiento del XVF. LiveKit puede alojarse en el entorno
propio; BVC se activa por defecto solo cuando el agente detecta LiveKit Cloud.
Gemini es el proveedor conversacional por defecto y OpenAI es seleccionable.

En modo **micrófono USB**, la placa entrega PCM mono al ordenador. La interfaz USB
convive con el perfil agente mediante un árbitro que evita lectores simultáneos
del micrófono. Las reuniones se inician desde el perfil agente y usan una sesión
de captura dedicada, sin conversación con el modelo.

## Desarrollo

Compilación, lint y pruebas se ejecutan **dentro del devcontainer**. En macOS, el
flasheo y la lectura del USB se realizan desde el host con los artefactos generados
en el contenedor.

1. Preparar las variables y abrir el entorno según [DEVCONTAINER.md](docs/DEVCONTAINER.md).
2. Dentro del contenedor, aplicar migraciones con `make server-migrate` y arrancar
   `make server-run`, `make server-outbox`, `make agent` y `make dashboard-dev` en
   terminales separados.
3. Compilar con `make fw-build`; desde el host, flashear con `make flash`.
4. Configurar/adoptar la unidad desde el instalador y el panel. Las sesiones usan
   `POST /v1/sessions` con identidad y secreto del dispositivo.

La [guía de ejecución](docs/BUILD_AND_RUN.md) detalla los prerrequisitos, el
aprovisionamiento y la verificación. No se guardan credenciales reales en Git.

## Documentación

| Documento | Uso |
|---|---|
| [docs/STATUS.md](docs/STATUS.md) | Estado actual y evidencia disponible. |
| [docs/RECENT_CHANGES.md](docs/RECENT_CHANGES.md) | PRs, commits, integración y publicación desde julio. |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Componentes, flujos y límites del sistema implementado. |
| [ROADMAP.md](ROADMAP.md) | Prioridades actuales y propuestas históricas identificadas como tales. |
| [MILESTONE.md](MILESTONE.md) | Criterios pendientes para operación autónoma y recuperación. |
| [TESTING.md](TESTING.md) | Pruebas existentes, comandos y validación pendiente en hardware. |
| [docs/FIRMWARE.md](docs/FIRMWARE.md) | Módulos, ABI y restricciones de firmware. |
| [docs/HARDWARE.md](docs/HARDWARE.md) | Placa, buses y conexiones. |
| [docs/PROFILES.md](docs/PROFILES.md) / [docs/USB_MIC.md](docs/USB_MIC.md) | Perfiles locales y convivencia del micrófono USB. |
| [Flota](docs/implementation/12-fleet-adoption-functional-spec.md) | Requisitos de adopción y administración. |
| [Reuniones](docs/implementation/13-meeting-recordings-functional-spec.md) / [diseño](docs/implementation/14-meeting-recordings-technical-design.md) | Requisitos y diseño de reuniones; consultar STATUS para su ejecución real. |
| [SESSION.md](SESSION.md) | Registro cronológico de pruebas e incidencias, con su contexto de fecha. |
