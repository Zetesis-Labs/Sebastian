# Sebastian Server

API Go contract-first para dispositivos Sebastian, administración y coordinación
con LiveKit. PostgreSQL es la fuente de verdad; Bun proporciona un acceso
SQL-first con escaneos tipados, y cada cambio de estado deja su evento de forma
atómica en `domain_events`, el rastro de auditoría.

## Desarrollo

Todos los comandos se ejecutan dentro del devcontainer. Desde un terminal del
contenedor:

```bash
cd /workspace/server
make generate
make migrate
make check
make run       # API
```

El Compose del devcontainer proporciona `DATABASE_URL`, LiveKit y PostgreSQL. La
API escucha en `:8787` y el puerto ya está publicado al host.

## Contrato OpenAPI

[`api/openapi.yaml`](api/openapi.yaml) es el contrato fuente. No se editan los
tipos ni el router a mano:

```bash
make generate
```

Ese comando genera `internal/api/openapi.gen.go` mediante `oapi-codegen`. La
persistencia usa Bun con consultas SQL-first escritas en
`internal/postgres/store.go`; no hay código ORM generado que versionar.

## Endpoints principales

- `GET /healthz`: liveness del proceso.
- `GET /readyz`: disponibilidad de PostgreSQL.
- `GET /openapi.json`: contrato OpenAPI embebido generado desde el documento
  fuente.
- `POST /v1/sessions`: autentica un dispositivo, realiza el dispatch explícito
  del agente y devuelve la conexión LiveKit en JSON.
- `GET /v1/devices/{id}/desired-profile?current=&cfg=&fw=`: poll de
  reconciliación del firmware; la cabecera `X-Desired-Config` lleva la versión
  de la configuración deseada. `GET /v1/devices/{id}/config` (secreto de
  dispositivo) la devuelve.
- `PUT /v1/devices/{id}/running-config` (secreto de dispositivo): la unidad
  reporta la configuración que ejecuta al arrancar (sin secretos); la ficha la
  muestra frente a la deseada.
- `GET`/`POST /v1/devices/{id}/enroll`: alta de una unidad cuyo control room
  aún no tiene su secreto (provisionada desde el instalador embebido, o tras un
  401): el `GET` da un nonce de un solo uso (60 s) y el `POST {nonce, mac,
  deviceSecret}` con `mac = HMAC-SHA256(secreto de organización, nonce + "." +
  id)` la adopta con el secreto que ella misma entrega (nace con la placa; la
  adopción por red lo recibe igual en la respuesta `ok`). 404 si no hay
  `SEBASTIAN_ORG_SECRET`.
- Flota ([diseño](../docs/implementation/11-fleet-adoption-control-room.md)):
  `GET /v1/admin/devices` (vista con lo visto por mDNS), `GET/PATCH
  /v1/admin/devices/{id}`, `POST …/adopt` y `POST …/forget` (202 + job en
  `GET /v1/admin/adoptions/{jobId}`), `PUT/DELETE …/desired-config`,
  `POST …/secret`, `GET /v1/admin/control-room`.
- Reuniones ([diseño](../docs/implementation/14-meeting-recordings-technical-design.md)):
  `POST /v1/admin/devices/{id}/meetings`, `GET /v1/admin/meetings`,
  `GET/DELETE /v1/admin/meetings/{id}`, `POST …/stop`,
  `GET …/audio` (con `Range`); la placa confirma con
  `PUT /v1/devices/{id}/meeting` y recibe la orden por UDP (`cmd`) o en la
  cabecera `X-Meeting` del poll; el agente sube el audio con
  `PUT /v1/meetings/{id}/audio` (chunked, reanudable con `Content-Range`).

Reuniones añade también `PATCH` (título, hablantes, conservar), búsqueda `q`,
exportación `…/transcript` TXT/SRT, reintentos `…/transcribe` y `…/summarize`.
El agente usa `HEAD …/audio` para recuperar el offset y `…/stop`/`…/warn` con
`X-Agent-Secret`; puede solicitar inicio por voz en `POST /v1/meetings`.

El worker de transcripción vive en el proceso API. Recupera reuniones en
`transcribing` al arrancar, corta Ogg con ffmpeg y llama al proveedor. El audio
reside en `SEBASTIAN_MEETINGS_DIR`, no en PostgreSQL ni en object storage.
La recuperación de una reunión `cut` requiere validación específica: el worker
solo procesa `transcribing`; el panel permite solicitar otra transcripción.

El `/token` legado sin autenticación se retiró (RF-12): las sesiones se abren
solo con `POST /v1/sessions` y requieren `X-Device-Id` y `X-Device-Secret`.

## Variables

| Variable | Obligatoria | Valor por defecto |
|---|---:|---|
| `DATABASE_URL` | sí | — |
| `LIVEKIT_URL` | sí | — |
| `LIVEKIT_API_KEY` | sí | — |
| `LIVEKIT_API_SECRET` | sí | — |
| `SEBASTIAN_SERVER_ADDRESS` | no | `:8787` |
| `SEBASTIAN_ROOM_PREFIX` | no | `sebastian` |
| `SEBASTIAN_TOKEN_TTL` | no | `1h` |
| `SEBASTIAN_ADMIN_SECRET` | sí | — |
| `SEBASTIAN_PUBLIC_API_URL` | para adoptar | — (la URL con la que los devices llegan a este control room) |
| `SEBASTIAN_CONTROL_ROOM_NAME` | no | `SEBASTIAN_PUBLIC_API_URL` |
| `SEBASTIAN_ORG_SECRET` | para adoptar | — (secreto de organización que autoriza la adopción) |
| `SEBASTIAN_AGENT_SECRET` | para grabar reuniones y dar las claves de los agentes | — (lo comparte con el agente, que sube el audio y pide la clave de su perfil con `X-Agent-Secret`) |
| `SEBASTIAN_AGENT_PROFILES_FILE` | no | — (JSON `{"profiles":[{name, provider, model?, voice?, language?}]}`; esos perfiles son de Kubernetes y el panel no los edita) |
| `SEBASTIAN_SECRETS_KEY` | para guardar claves desde el panel | — (32 bytes en hex o base64; cifra con AES-256-GCM la clave de cada perfil) |
| `SEBASTIAN_MEETINGS_DIR` | no | `meetings` (directorio del audio de reuniones) |
| `SEBASTIAN_MEETING_MAX_DURATION` | no | `3h` (RM-24) |
| `SEBASTIAN_MEETING_RETENTION_DAYS` | no | `90`; `0` desactiva limpieza |
| `SEBASTIAN_MEETING_SUMMARY` | no | `true` |
| `OPENAI_API_KEY` | para transcribir/resumir | —; sin ella se conserva audio y se informa `no_transcript` |
| `OPENAI_BASE_URL` | no | `https://api.openai.com/v1` |
| `SEBASTIAN_TRANSCRIBE_MODEL` | no | `gpt-4o-transcribe-diarize` |
| `SEBASTIAN_SUMMARY_MODEL` | no | `gpt-5.4-mini` |
| `SEBASTIAN_FFMPEG` | no | `ffmpeg` (necesario en PATH) |
| `SEBASTIAN_SYSLOG_IP` / `SEBASTIAN_SYSLOG_PORT` | no | — / `514` (escrito en los devices adoptados) |
| `SEBASTIAN_DISCOVERY_ENABLED` | no | `true` (escucha mDNS `_sebastian._tcp`; necesita red del host en Kubernetes) |

El esquema deseado se define en `db/schema.sql`. Atlas lo compara con el
historial SQL y crea una migración versionada y revisable:

```bash
make migration name=add_device_notes
```

Atlas mantiene `atlas.sum` para detectar cambios accidentales del historial. Las
migraciones aprobadas son forward-only, quedan embebidas en
`sebastian-migrate` y se ejecutan bajo un advisory lock de PostgreSQL. El comando
es idempotente:

```bash
go run ./cmd/migrate
```

## Eventos de dominio

No usamos event sourcing como fuente de verdad: PostgreSQL guarda el estado
actual y `domain_events` conserva, en la misma transacción que cada cambio, los
hechos que interesa auditar. No hay publicación a terceros.

OIDC queda pospuesto. Las rutas administrativas usan temporalmente
`X-Admin-Secret`; el dashboard lo envía exclusivamente desde sus funciones SSR,
por lo que no aparece en el navegador. La autenticación de dispositivos es un
mecanismo separado.


## Estado de integración

A 2026-09-19, la flota (#46–48) y reuniones A (#49) están integradas. El resto de
reuniones, incluido voz, está en #50. La release #45 sigue abierta. Ver
[procedencia](../docs/RECENT_CHANGES.md), [estado](../docs/STATUS.md) y
[aceptación pendiente](../TESTING.md).
