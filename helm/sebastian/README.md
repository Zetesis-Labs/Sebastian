# Chart de Sebastian

Chart del backend para Cortes/ArgoCD. La presencia del chart en una rama no
acredita su despliegue. Estado de integración y release:
[RECENT_CHANGES.md](../../docs/RECENT_CHANGES.md).

## Componentes

| Componente | Función | Red |
|---|---|---|
| `livekit` | SFU propio | `hostNetwork`; señalización 7880, medios UDP 7882 y fallback TCP 7881. |
| `nats` | JetStream para eventos | Servicio interno. |
| `server` | API Go, migraciones y contenedor outbox | Servicio 8787; `hostPort` configurable para la LAN. |
| `dashboard` | Panel SSR e instalador | Servicio 3001 e ingress configurable. |
| `agent` | Worker de conversación o captura | Conecta al SFU; sin servicio HTTP propio. |
| `control-plane` | Anuncios sobre salas activas | Servicio 8790 e ingress configurable. |

La configuración de producción vive en
`Mileto-Infra-GitOps/px-platon/cortes/helm/sebastian-values*.yaml` y las versiones
en `px-platon/cortes/sebastian/envs/prod/env.json` del mismo repositorio.

## Secretos y configuración

- LiveKit: `LIVEKIT_KEYS` en `existingSecret`.
- Servidor: `DATABASE_URL`, `LIVEKIT_URL`, `LIVEKIT_API_KEY`,
  `LIVEKIT_API_SECRET`, `SEBASTIAN_ADMIN_SECRET`.
- Adopción: `SEBASTIAN_ORG_SECRET` en el secreto del servidor y
  `server.fleet.publicApiURL` alcanzable por la placa; nombre y syslog opcionales.
- Reuniones: `SEBASTIAN_AGENT_SECRET` compartido entre servidor y agente;
  `OPENAI_API_KEY` en servidor para transcripción/resumen y en agente para voz.
- Agente: credenciales LiveKit, clave del proveedor conversacional y, si se usa,
  `SEBASTIAN_HA_MCP_URL`/`SEBASTIAN_HA_TOKEN`. El chart configura la URL interna
  del servidor para reuniones.
- Dashboard: `SEBASTIAN_ADMIN_SECRET`, por defecto desde el secreto del servidor.
- Control plane: credenciales LiveKit.
- Outbox: usa NATS del chart o `server.outbox.natsURL`; si ambos faltan con NATS
  desactivado, requiere `NATS_URL` en el secreto del servidor.

No guardar valores reales en `values.yaml`. El panel no tiene login propio;
proteger su acceso y el del instalador desde la red/ingress.

## Persistencia de reuniones

`server.meetings` configura directorio (`/data/meetings`), duración máxima,
retención (90 días), resumen y PVC (5 GiB por defecto). Ajustar la StorageClass
al cluster. La metadata está en PostgreSQL; el audio está en ese volumen.

Agente y servidor necesitan imágenes que incluyan ffmpeg y el contrato de
reuniones compatible. Las reuniones marcadas para conservar quedan fuera de la
limpieza. Verificar persistencia, capacidad y restauración conjunta de audio y
base de datos antes de dar por aceptado el despliegue.

## Red de dispositivos

La placa necesita alcanzar la API y LiveKit por sus direcciones de LAN. El campo
NVS `tokenServerUrl` conserva el nombre histórico; el firmware toma su origen y
abre `POST /v1/sessions` autenticado. El servidor Go ya no expone `/token`.

mDNS necesita conectividad multicast con las unidades. El chart deja
`server.fleet.discoveryEnabled=false` por defecto; habilitarlo sin una red que
transporte esos anuncios no permite descubrirlas. Adoptar por IP es el recorrido
alternativo. Las órdenes de reunión usan LAN firmada y sondeo como respaldo.

## Publicación

Release-please agrupa cambios por componente. Los workflows publican imágenes y
chart y preparan el cambio de versiones en Mileto. A 2026-09-19, #45 está abierto
como propuesta de release y #50 sigue abierto con reuniones B–F. Comprobar los
artefactos/versiones resultantes antes de aplicar configuración de despliegue.
