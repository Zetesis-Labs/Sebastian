# 15 — Agentes: con qué modelo habla cada altavoz

Estado: implementado (rama `feat/phonic-provider`, 2026-11-03).

## Qué resuelve

Cada altavoz habla a través de un **agente** (`agent_profiles`): proveedor
realtime (`gemini`, `openai` o `phonic`), modelo, voz e idioma. El agente de
LiveKit es uno solo (`sebastian`) y en cada sesión construye el modelo que le
indica el perfil del altavoz.

No confundir con los perfiles del firmware (`agente`, `micro-usb`, NVS), que
deciden qué es la placa; el agente decide con qué modelo conversa.

## Decisiones (Rubén, 2026-11-03)

1. **Perfiles de agente, N, no un selector por altavoz.** El altavoz apunta a
   un perfil (`devices.agent_profile_id`, ya existía) y su `config` viaja al
   agente en los metadatos del despacho, como antes.
2. **Modelo mixto.** Los perfiles declarados en el fichero
   `SEBASTIAN_AGENT_PROFILES_FILE` (un ConfigMap: `server.agentProfiles` del
   chart) son de Kubernetes y el panel no los edita. El panel crea los suyos.
   Cada perfil tiene un solo dueño, así que Git y producción no divergen.
3. **Una clave por perfil, desde el panel.** Se guarda cifrada en Postgres
   (AES-256-GCM con `SEBASTIAN_SECRETS_KEY`; el id del perfil como dato
   asociado, para que un cifrado copiado a otra fila no abra). Vale también
   para los perfiles de Kubernetes: la clave es lo único que el panel puede
   tocar en ellos. Nunca va en el fichero ni en los metadatos de LiveKit: el
   agente la pide con `GET /v1/agent-profiles/{id}/credentials` y
   `X-Agent-Secret`.
4. **Sin clave en el panel, la del entorno del agente** (`GOOGLE_API_KEY`,
   `OPENAI_API_KEY`, `PHONIC_API_KEY`). **La sesión se rechaza** (error en el
   log y `sebastian_agent_model_unavailable_total` por proveedor y motivo)
   si no hay ninguna clave, si el proveedor es desconocido, si la clave
   guardada no se puede leer, si es de otro proveedor que el de la sesión o si
   al agente le falta `SEBASTIAN_AGENT_SECRET` para pedirla. Nunca se arranca
   con un modelo ni una clave supuestos.
5. **Un perfil sin proveedor** (el `default` sembrado, `config {}`) habla con
   el proveedor del entorno del agente (`SEBASTIAN_MODEL_PROVIDER`), que el
   servidor no conoce: el panel lo muestra así y no le deja guardar clave
   hasta que se le elija un proveedor.

## Sincronización con el fichero (al arrancar el servidor)

`agentprofile.PlanSync` decide y la tienda aplica en una transacción:

| Situación | Resultado |
|-----------|-----------|
| Nombre nuevo en el fichero | se crea, de Kubernetes |
| Nombre que ya existe (aunque lo creara el panel) | se actualiza y pasa a Kubernetes |
| Perfil de Kubernetes que el fichero ya no nombra, sin altavoces | se borra |
| Ídem, con altavoces que lo usan | pasa al panel (aviso en el log) |

Un fichero inválido (JSON roto, campo desconocido, nombre repetido,
proveedor desconocido) detiene el arranque. El chart genera siempre el
fichero, aunque la lista esté vacía, para que vaciarla retire los perfiles.
El borrado es lógico (`deleted_at`): las sesiones conservan el perfil con el
que se hicieron, pero la clave guardada se borra con él. Borrar y asignar
bloquean la fila del perfil, así que un altavoz nunca queda apuntando a uno
borrado.

## Contrato

- `GET|POST /v1/admin/agent-profiles`, `PATCH|DELETE /v1/admin/agent-profiles/{id}`
  (409 si es de Kubernetes, si el nombre está cogido o si lo usan altavoces).
- `PUT|DELETE /v1/admin/agent-profiles/{id}/api-key`: solo escritura; la lista
  dice `apiKeySet` y `secretsKeyConfigured`.
- `PATCH /v1/admin/devices/{id}` con `agentProfileId` (422 si no existe).
- `GET /v1/agent-profiles/{id}/credentials` (agente): `{provider, apiKey?}`.

## Phonic

Plugin `livekit-plugins-phonic`, que exige `livekit-agents` 1.8. Igual que
Gemini, no recorta el historial al interrumpirle (`message_truncation=False`)
y deja la interrupción a su servidor. Transcribe al usuario palabra a palabra
y el plugin guarda cada palabra como un mensaje: `turns.user_turns` junta los
mensajes seguidos del usuario en un turno, que es lo que mira
`voice_start_asked`. Voces en castellano: `eduardo`, `roberto` (México) y
`valeria` (Colombia); ninguna de España.

## Pendiente

- Un altavoz adoptado recibe el perfil más antiguo; no hay «perfil por
  defecto» configurable.
- El panel no sabe si el agente tiene en su entorno la clave de un proveedor;
  solo lo dice el log del agente al rechazar la sesión.
