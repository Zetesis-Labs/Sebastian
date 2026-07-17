# Sebastian Server

API Go contract-first para dispositivos Sebastian, administración y coordinación
con LiveKit. PostgreSQL es la fuente de verdad; Bun proporciona un acceso
SQL-first con escaneos tipados, y cada cambio que deba publicarse se registra de forma atómica
en `domain_events` y `outbox_events`. Un proceso separado entrega esos eventos
a NATS JetStream en formato CloudEvents.

## Desarrollo

Todos los comandos se ejecutan dentro del devcontainer. Desde un terminal del
contenedor:

```bash
cd /workspace/server
make generate
make migrate
make check
make run       # API
make outbox    # publicador JetStream, en otro terminal
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

## Endpoints iniciales

- `GET /healthz`: liveness del proceso.
- `GET /readyz`: disponibilidad de PostgreSQL.
- `GET /openapi.json`: contrato OpenAPI embebido generado desde el documento
  fuente.
- `GET /v1/admin/recordings`: catálogo de grabaciones recientes.
- `GET /v1/admin/recordings/summary`: duración y almacenamiento agregados.
- `GET /v1/admin/recordings/{id}`: detalle reproducible de una grabación.
- `POST /v1/admin/recordings`: registra audio ya depositado en object storage.
- `POST /v1/sessions`: autentica un dispositivo, realiza el dispatch explícito
  del agente y devuelve la conexión LiveKit en JSON.
- `GET /token`: adaptador transitorio compatible con el firmware actual.

`/token` solamente existe cuando `SEBASTIAN_LEGACY_TOKEN_ENABLED=true`. No debe
exponerse públicamente: omite autenticación para mantener la compatibilidad con
el firmware desplegado. El endpoint v1 requiere `X-Device-Id` y
`X-Device-Secret`.

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
| `SEBASTIAN_LEGACY_TOKEN_ENABLED` | no | `false` |
| `SEBASTIAN_LEGACY_DEVICE_ID` | no | `esp32-respeaker` |
| `SEBASTIAN_ADMIN_SECRET` | sí | — |

El worker de outbox utiliza estas variables independientes:

| Variable | Obligatoria | Valor por defecto |
|---|---:|---|
| `DATABASE_URL` | sí | — |
| `NATS_URL` | sí | — |
| `SEBASTIAN_NATS_STREAM` | no | `SEBASTIAN_EVENTS` |
| `SEBASTIAN_NATS_SUBJECTS` | no | `evt.sebastian.v1.>` |
| `SEBASTIAN_OUTBOX_BATCH_SIZE` | no | `50` |
| `SEBASTIAN_OUTBOX_POLL_INTERVAL` | no | `1s` |
| `SEBASTIAN_OUTBOX_PUBLISH_TIMEOUT` | no | `5s` |

El esquema deseado se define en `db/schema.sql`. Atlas lo compara con el
historial SQL y crea una migración versionada y revisable:

```bash
make migration name=add_recordings
```

Atlas mantiene `atlas.sum` para detectar cambios accidentales del historial. Las
migraciones aprobadas son forward-only, quedan embebidas en
`sebastian-migrate` y se ejecutan bajo un advisory lock de PostgreSQL. El comando
es idempotente:

```bash
go run ./cmd/migrate
```

## Entrega de eventos

El worker bloquea lotes con `FOR UPDATE SKIP LOCKED`, espera el ACK de JetStream
y solamente entonces marca el evento como publicado. Los fallos quedan
registrados con backoff persistente. El `event_id` se usa como identificador de
deduplicación, de modo que un fallo entre el ACK y el commit no duplica el evento
dentro de la ventana configurada de JetStream.

No usamos event sourcing como fuente de verdad: PostgreSQL guarda el estado
actual y `domain_events` conserva los hechos que interesa integrar o auditar.

OIDC queda pospuesto. Las rutas administrativas usan temporalmente
`X-Admin-Secret`; el dashboard lo envía exclusivamente desde sus funciones SSR,
por lo que no aparece en el navegador. La autenticación de dispositivos es un
mecanismo separado.
