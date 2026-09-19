# Agente de Sebastian

Worker Python sobre LiveKit Agents 1.6 que entra en la sala despachada por la API
Go. La identidad de la unidad procede de `device_id` en los metadatos; no se
presupone un participante fijo.

## Modos

- **Conversación:** Gemini por defecto u OpenAI seleccionable mediante
  `SEBASTIAN_MODEL_PROVIDER`, Home Assistant opcional por MCP, audio previo a la
  activación, interrupciones y cierre de sesión.
- **Reunión:** `mode=meeting` en los metadatos activa `meeting_mode.py`, sin
  `AgentSession` conversacional. Captura PCM, codifica Ogg/Opus a 48 kbit/s y lo
  sube al servidor con reanudación. VAD controla el límite de silencio.

Las órdenes por voz de reuniones están implementadas en `84f278b6`: inicio desde
la conversación y parada mediante activación, ventana de voz, transcripción e
intención. Su aceptación en placa sigue pendiente según SESSION.

## Requisitos y ejecución

Usar el devcontainer con Python 3.13 o posterior. Configurar `.env` desde
[.env.example](.env.example):

- `LIVEKIT_URL`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` del mismo SFU que usa Go.
  Se admite SFU propio; no es necesaria una cuenta Cloud.
- `GOOGLE_API_KEY` para conversación Gemini u `OPENAI_API_KEY` para OpenAI.
- `SEBASTIAN_API_URL` y `SEBASTIAN_AGENT_SECRET` compartido con Go para reuniones.
- ffmpeg en PATH para reuniones. La imagen del agente lo incluye; el Dockerfile
  de desarrollo todavía no lo instala.
- `OPENAI_API_KEY` también para las órdenes por voz de reunión. La transcripción
  completa y el resumen se ejecutan en Go y necesitan la clave en ese proceso.

Dentro de `/workspace/agent`:

```bash
uv sync
uv run agent.py dev
```

`uv run agent.py start` ejecuta el worker de producción. El servidor Go solicita
el dispatch explícito al abrir cada sesión. Los ejemplos Cloud de `.env.example`
deben adaptarse al entorno si se usa LiveKit local.

BVC se habilita automáticamente para Cloud y se deshabilita para SFU propio;
`SEBASTIAN_BVC` permite override. El dispositivo usa LEFT/comms procesado por el
XVF. Elegir OpenAI como proveedor es configuración explícita, no failover automático.

## Grabaciones y diagnóstico

`SEBASTIAN_RECORD` tiene valor por defecto `1` en Python; el chart lo fija a `0`.
Al habilitarlo, `SEBASTIAN_RECORD_DIR` (por defecto `recordings`) recibe
`_model.wav` y `_agent.wav`; `SEBASTIAN_RECORD_TRACK=1` añade `_mic.wav`.
El audio de entrada al modelo incluye pre-roll; la pista cruda empieza después.
Estos archivos no se registran automáticamente en el catálogo ni reciben la
retención de reuniones.

OTel exporta logs y métricas si se configura `OTEL_EXPORTER_OTLP_ENDPOINT`.
`control_plane.py` ofrece anuncios sobre una sala activa; `token_server.py`
permanece como utilidad antigua y no es el servidor de sesiones vigente.

## Pruebas y despliegue

Dentro del devcontainer:

```bash
uv run pytest
uvx ruff==0.14.0 check .
```

El workflow de Python actual ejecuta Ruff; pytest requiere ejecución explícita.
Las pruebas de reuniones necesitan ffmpeg. El [plan de pruebas](../TESTING.md)
cubre las comprobaciones de hardware y recuperación.

El chart [helm/sebastian](../helm/sebastian/) despliega el worker y el control
plane; la API Go es otra imagen. El endpoint permanente de
[#44](https://github.com/Zetesis-Labs/Sebastian/pull/44) sigue en una rama separada.
