> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# model-cost-matrix — Matriz de decisión del modelo de voz (S2S vs pipeline) para Sebastian: costes reales de uso doméstico y compatibilidad con livekit-agents 1.6.x

**Veredicto:** viable — Hay al menos dos rutas S2S baratas (Gemini 2.5 native audio y Nova 2 Sonic, ~10-38 $/mes) totalmente soportadas en livekit-agents 1.6.4, y la abstracción para cambiar de modelo por config es un cambio pequeño sobre el `agent.py` actual. El único candidato problemático hoy es gpt-realtime-2 (issues abiertas en el plugin de LiveKit) y el pipeline no es la opción más barata como se asumía: el TTS domina su coste.

**Esfuerzo:** S — 1,5-2 días de una persona: 0,5 d factory + config + upgrade deps; 0,5 d probar los 3 plugins S2S contra el dispositivo real; 0,5-1 d telemetría de usage y tabla $/min real. (El gate DDSD y la máquina de estados son trabajo aparte, ya planificado en P1.)

## Hallazgos
- Precios vigentes (jul-2026) por 1M tokens de audio: gpt-realtime-2 $32 in / $64 out (cached in $0.40); gpt-realtime-mini $10 in / $20 out (texto $0.60/$2.40); Gemini 2.5 flash native audio y gemini-3.1-flash-live-preview $3 in / $12 out; Nova 2 Sonic $3 in / $12 out. Metering: OpenAI 1 tok/100 ms in y 1 tok/50 ms out (600/1200 tok por min); Gemini y Nova ~25 tok/s (≈1500 tok/min).  
  _https://developers.openai.com/api/docs/pricing , https://ai.google.dev/gemini-api/docs/pricing , https://aws.amazon.com/nova/pricing/_
- En OpenAI Realtime cada Response re-envía TODO el historial como input y se factura acumulativamente; el caching automático (audio cacheado a $0.40/1M, −98,75%) lo hace tolerable pero exige historial estático y truncado (token_limits.post_instructions / retention_ratio). Gemini Live y Nova mantienen la sesión server-side y facturan el audio streameado.  
  _https://developers.openai.com/api/docs/guides/realtime-costs_
- Coste mensual calculado (solo tokens de audio de conversación): moderado (337,5 min in / 450 min out): gpt-realtime-2 $41, gpt-realtime-mini $12,8, Gemini $9,6, Nova 2 Sonic $9,6, pipeline $15-26. Intenso (1200/1800 min): $161 / $50 / $38 / $38 / $58-101. El pipeline NO es la vía barata: el TTS (Cartesia $50/1M chars ≈ $0,045/min) cuesta 2,5× el audio-out de Gemini.  
  _https://livekit.com/pricing/inference_
- LINGER (8 s post-respuesta, solo audio-in): moderado +$0,81-3,46/mes sin gate, +$0,16-0,69 con gate DDSD (80% cortado); intenso +$2,9-12,3 sin gate, +$0,58-2,46 con gate. El gate DDSD solo es económicamente relevante con gpt-realtime-2; con Gemini/Nova/mini el sobrecoste sin gate ya es <$4/mes — el gate se justifica por UX/privacidad, no por coste.  
  _cálculo propio sobre precios verificados (script en el informe)_
- Proactive Audio (proactivity=True en el plugin google de livekit) solo funciona en gemini-2.5-flash-native-audio-preview-12-2025; NO está soportado en los modelos Gemini 3.1. Además ahorra el audio-OUT espurio (el modelo decide no responder) pero el audio-IN del LINGER se factura igual: es complementario al gate DDSD, no lo sustituye.  
  _https://docs.livekit.io/agents/models/realtime/plugins/gemini/_
- Madurez plugins livekit-agents 1.6.4 (PyPI 24-jun-2026): openai realtime maduro para gpt-realtime/mini pero con issues abiertas para gpt-realtime-2 (#5768 multi-message generation descarta mensajes, #5808 truncado silencioso en response incomplete, #5906 reasoning.effort no expuesto); google realtime maduro incl. proactivity (3.1 con limitaciones: sin async tools, sin update_agent); aws realtime soporta Nova 2 Sonic como default (livekit-plugins-aws[realtime]) con issues de instalación (#3244) y TypeError (#3629).  
  _https://github.com/livekit/agents/issues/5768_
- Nova 2 Sonic soporta español con voces polyglot (cambio de idioma en la misma conversación) y tool calling; el mcp_servers de AgentSession está deprecado en 1.6.x a favor de MCPToolset en tools=, que funciona con cualquier modelo (realtime o pipeline) porque el bridging lo hace el agente.  
  _https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html , https://docs.livekit.io/agents/logic/tools/mcp/_
- El agent.py actual (79 líneas) ya usa AgentSession(llm=openai.realtime.RealtimeModel(...)): la abstracción por config es solo una factory que devuelve kwargs distintos (llm= vs stt=/llm=/tts=); el resto (Agent, RoomInputOptions, BVC) no cambia.  
  _agent/agent.py:62-71_

## Diseño

# Matriz de decisión del modelo de voz — Sebastian

## 1. Precios verificados (jul-2026) y metering

| Modelo | Audio in $/1M | Audio out $/1M | Metering | $/min in | $/min out |
|---|---|---|---|---|---|
| gpt-realtime-2 | 32 (cached 0,40) | 64 | 1 tok/100ms in, 1 tok/50ms out | 0,0192 | 0,0768 |
| gpt-realtime-mini | 10 (cached ~0,30) | 20 | ídem | 0,006 | 0,024 |
| gemini-2.5-flash-native-audio / 3.1-flash-live-preview | 3 | 12 | ~25 tok/s | 0,005 | 0,018 |
| Nova 2 Sonic (Bedrock) | 3 | 12 | ~25 tok/s | 0,0045 | 0,018 |
| Pipeline (LiveKit Inference) | STT Deepgram Nova-3 multi 0,0058/min | TTS Cartesia $50/1M chars ≈ 0,045/min · Inworld $25/1M ≈ 0,024/min | LLM gpt-4.1-mini $0,40/$1,60 por 1M | — | — |

Regla clave OpenAI: cada Response re-factura TODO el historial (audio del asistente incluido, como input);
el caching automático lo deja a $0,40/1M si el historial es estático → sumar ~10-20% a las cifras base y
activar truncado (`token_limits.post_instructions`, `retention_ratio≈0.8`). Gemini/Nova: sesión stateful, sin re-facturación explícita.

## 2. Escenarios de coste mensual (30 días, solo tokens de audio)

(a) moderado = 15 conv/día × 45s in + 60s out → 337,5 min in / 450 min out
(b) intenso = 40 conv/día × 60s in + 90s out → 1.200 min in / 1.800 min out

| Modelo | (a) base | (b) base | LINGER (a) sin/con gate | LINGER (b) sin/con gate |
|---|---|---|---|---|
| gpt-realtime-2 | **$41** (+15% hist ≈ $47) | **$161** (≈$185) | +$3,46 / +$0,69 | +$12,3 / +$2,46 |
| gpt-realtime-mini | **$12,8** | **$50** | +$1,08 / +$0,22 | +$3,84 / +$0,77 |
| Gemini native audio | **$9,6** | **$38** | +$0,81 / +$0,16 | +$2,88 / +$0,58 |
| Nova 2 Sonic | **$9,6** | **$38** | +$0,81 / +$0,16 | +$2,88 / +$0,58 |
| Pipeline (Deepgram+4.1-mini+Cartesia) | **$26** | **$101** | STT sólo: +$1,04 | +$3,71 |
| Pipeline con Inworld TTS | **$15,5** | **$58** | ídem | ídem |

LINGER = 8s × nº respuestas (3/conv moderado, 4/conv intenso) = 180 / 640 min-in/mes. Gate DDSD corta 80%.
**Conclusión coste**: el gate DDSD sólo es rentable con gpt-realtime-2; en el resto es tema de UX/privacidad.
Y el pipeline NO es la vía barata (el TTS domina): Gemini/Nova S2S son ya el suelo de precio.

## 3. Features que importan a Sebastian (estado plugins livekit-agents 1.6.4)

| Feature | gpt-realtime-2 | gpt-rt-mini | Gemini 2.5 NA | Gemini 3.1 live | Nova 2 Sonic | Pipeline |
|---|---|---|---|---|---|---|
| Tools + MCP (MCPToolset agente) | Sí (+MCP server-side propio) | Sí | Sí (async tools) | Sí (sin async) | Sí | Sí (total) |
| Voz es-ES | Buena (marin/cedar, acento a veces neutro) | Correcta | Muy buena (30+ voces HD, es-ES) | ídem | Buena (polyglot, es) | La mejor (ElevenLabs/Cartesia, clonable) |
| Turn detection propio | semantic_vad | semantic_vad | VAD nativo + LK turn detector | ídem | 3 niveles (HIGH/MED/LOW) | LiveKit Turn Detector v1.0 audio-nativo (es) |
| Proactive Audio | No | No | **Sí (proactivity=True)** | **No** | No | Equivalente vía gate DDSD |
| Barge-in / Adaptive Interruption | Sí | Sí | Sí | Sí | Sí | Sí |
| Transcripciones usuario | Opt-in (gpt-realtime-whisper $0,017/min extra) | ídem | Incluidas | Incluidas | Incluidas (text tokens) | Nativas (son el STT) |
| Latencia voz-a-voz típica | ~500-800 ms | ~500 ms | ~600 ms | ~500 ms | ~300-500 ms | ~700-1200 ms |
| Plugin 1.6.x | **Issues abiertas** (#5768/#5808/#5906) | Maduro | Maduro | Limitaciones doc. | Funciona, rough edges (#3244/#3629) | Lo más maduro |

## 4. Pipeline como alternativa de control

Pros: el LLM solo se invoca si el gate DDSD aprueba (LINGER cuesta solo STT, ~$1-4/mes); parciales STT
gratis para la señal léxica del gate; memoria/RAG/speaker-ID trivial de inyectar en el prompt; TTS es-ES superior.
Contras: NO es más barato que Gemini/Nova (TTS domina); +1 salto de latencia; prosodia/emoción inferior a S2S nativo.

## 5. Recomendación por fase

- **P0 (desarrollo)**: cambiar YA `gpt-realtime` → `gpt-realtime-mini` (1 línea, mismo plugin, −70% coste) y
  montar la factory de abajo. Iterar el gate/estado con **pipeline** en paralelo porque da parciales STT (señal léxica DDSD) y logs legibles.
- **P1 (producto en casa)**: **gemini-2.5-flash-native-audio-preview-12-2025 con proactivity=True** — mejor $/min real
  ($9,6-38/mes), Proactive Audio cubre el LINGER sin lógica propia, es-ES nativo, plugin maduro. Plan B: Nova 2 Sonic (mismo precio,
  menor latencia, plugin menos rodado). Evitar gpt-realtime-2 hasta que se cierren #5768/#5808 (4× coste sin ventaja clara para un hogar).
- **Siempre**: medir con `SessionUsageUpdatedEvent` y decidir con $/min real propio, no con estas estimaciones.

### Abstracción en agent.py (qué cambia en AgentSession)

Factory por env `SEBASTIAN_MODEL`; solo cambian los kwargs de `AgentSession` — `Agent`, `RoomInputOptions(BVC)` y
`MCPToolset` en `tools=` son invariantes (ver snippet). Con S2S se pasa `llm=RealtimeModel`; con pipeline se pasan
`stt=`/`llm=`/`tts=` + `turn_detection=MultilingualModel()`. Nota 1.6.x: `mcp_servers=` está deprecado → `MCPToolset` en `tools=`.

Pasos concretos:
1. `agent/pyproject.toml`: `livekit-agents[openai,google,aws,deepgram,cartesia,turn-detector]~=1.6.4` (aws con extra `[realtime]`).
2. Crear `agent/models.py` con la factory (snippet) y `agent/config.py` leyendo `SEBASTIAN_MODEL`, `SEBASTIAN_VOICE`.
3. En `entrypoint`: `session = build_session(os.environ.get("SEBASTIAN_MODEL", "gemini"))`; grabador tras env var (arreglo ya listado en ROADMAP).
4. Añadir listener `session.on("usage_updated")` que loguee tokens in/out por tipo → CSV → decidir por $/min real tras 1 semana.
5. Gate DDSD: implementarlo agnóstico al modelo (corta el audio ANTES del AgentSession vía nodo de audio custom), así sirve igual para S2S y pipeline.

## Código
**agent/models.py (nuevo)** — Factory que abstrae la elección de modelo por config: qué cambia en AgentSession en cada caso (llm=RealtimeModel vs stt/llm/tts)

```python
import os
from livekit.agents import AgentSession
from livekit.plugins import openai, google, aws, deepgram, cartesia
from livekit.plugins.turn_detector.multilingual import MultilingualModel

def build_session(kind: str | None = None, **common) -> AgentSession:
    kind = kind or os.environ.get("SEBASTIAN_MODEL", "gemini")
    voice = os.environ.get("SEBASTIAN_VOICE")
    match kind:
        case "openai":          # P0: iterar barato con el plugin ya en uso
            return AgentSession(
                llm=openai.realtime.RealtimeModel(
                    model="gpt-realtime-mini", voice=voice or "marin"),
                **common)
        case "gemini":          # P1: Proactive Audio = LINGER casi gratis
            return AgentSession(
                llm=google.beta.realtime.RealtimeModel(
                    model="gemini-2.5-flash-native-audio-preview-12-2025",
                    voice=voice or "Puck",
                    proactivity=True),   # solo modelos native-audio 2.5, NO 3.1
                **common)
        case "nova":            # plan B mismo precio que Gemini
            return AgentSession(llm=aws.realtime.RealtimeModel(), **common)
        case "pipeline":        # control total: el LLM solo corre si el gate aprueba
            return AgentSession(
                stt=deepgram.STT(model="nova-3", language="multi"),
                llm=openai.LLM(model="gpt-4.1-mini"),
                tts=cartesia.TTS(voice=voice or "<voice-id-es>"),
                turn_detection=MultilingualModel(),  # v1.0 audio-nativo, es
                preemptive_generation=True,
                **common)
        case _:
            raise ValueError(f"SEBASTIAN_MODEL desconocido: {kind}")
```

**agent/agent.py (entrypoint, cambio mínimo)** — Uso de la factory + telemetría de coste real con SessionUsageUpdatedEvent; Agent/BVC/MCP no cambian entre modos

```python
session = build_session()  # SEBASTIAN_MODEL decide S2S vs pipeline

@session.on("usage_updated")  # loguear tokens por tipo -> $/min real
def _on_usage(ev):
    log_usage_csv(ev.usage)   # audio_in/out, text, cached -> decidir con datos

await session.start(
    room=ctx.room,
    agent=Sebastian(),  # tools=[MCPToolset(...)] aqui: mcp_servers= esta deprecado en 1.6.x
    room_input_options=RoomInputOptions(noise_cancellation=noise_cancellation.BVC()),
)
```

## Riesgos
- **gpt-realtime-2 con el plugin openai de livekit pierde mensajes (multi-message generation #5768) y trunca respuestas en silencio (#5808)** → No usarlo en P0/P1; si se quiere evaluar, fijar gpt-realtime (v1) o mini y suscribirse a las issues antes de reconsiderar
- **Los tres modelos baratos son preview (gemini-2.5-flash-native-audio-preview-12-2025, gemini-3.1-flash-live-preview, Nova 2 en Bedrock reciente): precios y model-ids pueden cambiar sin aviso** → La factory por config hace el cambio de modelo un deploy de 1 variable; re-verificar precios antes de P1 (los cálculos son snapshot jul-2026)
- **Proactive Audio no existe en Gemini 3.1: si Google jubila el 2.5 native audio, se pierde el LINGER 'gratis'** → Implementar igualmente el gate DDSD propio (agnóstico al modelo) como capa primaria; proactivity queda como refuerzo, no como dependencia
- **Estimaciones de coste sensibles a supuestos (nº turnos/conv para LINGER e historial re-facturado de OpenAI; chars/min del TTS)** → Instrumentar SessionUsageUpdatedEvent desde el día 1 y recalcular con datos reales; en OpenAI activar truncado de contexto y mantener historial estático para no romper el cache
- **Calidad es-ES de las voces no verificable por specs (acento, naturalidad)** → Test ciego casero de 10 frases con las 3-4 voces finalistas antes de fijar P1
- **livekit-plugins-aws[realtime] tiene fricción de instalación/errores runtime (#3244, #3629)** → Tratarlo como plan B; smoke-test en el día de la factory y descartarlo sin coste si falla

## Preguntas abiertas
- ¿Cuántos turnos reales tiene una conversación media en casa? (determina el peso del LINGER y del historial re-facturado — medir en P0)
- ¿Se factura la transcripción de entrada de OpenAI (gpt-realtime-whisper $0,017/min) si se activa input_audio_transcription, o va incluida en algún tier? Verificar en la primera factura
- ¿AssemblyAI Universal-Streaming soporta ya español en streaming vía LiveKit Inference? (sería el STT del gate a $0,0025/min, mitad que Deepgram)
- ¿El Turn Detector v1.0 audio-nativo de LiveKit puede usarse como señal DDSD durante LINGER sin sesión STT activa en modo S2S?
- Coste de minutos de participante de LiveKit Cloud en ARMED 24/7 (fuera del alcance de esta matriz; el ROADMAP ya contempla timeout de sala o self-host en cortes)

## Fuentes
- https://developers.openai.com/api/docs/pricing
- https://developers.openai.com/api/docs/guides/realtime-costs
- https://developers.openai.com/api/docs/models/gpt-realtime-mini
- https://ai.google.dev/gemini-api/docs/pricing
- https://docs.livekit.io/agents/models/realtime/plugins/gemini/
- https://docs.livekit.io/agents/integrations/realtime/nova-sonic/
- https://docs.livekit.io/agents/logic/tools/mcp/
- https://livekit.com/pricing/inference
- https://aws.amazon.com/nova/pricing/
- https://aws.amazon.com/blogs/aws/introducing-amazon-nova-2-sonic-next-generation-speech-to-speech-model-for-conversational-ai/
- https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html
- https://github.com/livekit/agents/issues/5684
- https://github.com/livekit/agents/issues/5768
- https://github.com/livekit/agents/issues/5808
- https://github.com/livekit/agents/issues/5906
- https://github.com/livekit/agents/issues/3244
- https://github.com/livekit/agents/issues/3629
- https://pypi.org/project/livekit-agents/
- https://pypi.org/project/livekit-plugins-aws/