# Sebastian — agente de voz (Python)

Cerebro conversacional del altavoz. Corre en tu Mac (o en cloud) y se une a la
misma sala de LiveKit que el dispositivo ESP32-S3.

Stack: [`livekit-agents`](https://github.com/livekit/agents) 1.6 + modelo
realtime configurable (`gemini` por defecto, `openai` como fallback) +
cancelación de ruido BVC.

## Requisitos

- Python ≥ 3.13
- [`uv`](https://docs.astral.sh/uv/) (`brew install uv`)
- Cuenta de LiveKit Cloud
- `GOOGLE_API_KEY` para Gemini Live, o `OPENAI_API_KEY` si usas el fallback OpenAI

## Puesta en marcha

```bash
cp .env.example .env      # rellena LIVEKIT_* y GOOGLE_API_KEY
uv sync
uv run agent.py dev       # worker en modo desarrollo (hot-reload)
```

El worker se queda esperando. Cuando el ESP32-S3 (o cualquier cliente) entra en
una sala, LiveKit despacha un job y el agente se une automáticamente.

## Probar sin la placa

Antes de tener el firmware funcionando puedes hablar con el agente desde el
navegador con el [Agents Playground](https://agents-playground.livekit.io/) o
`lk room join`, apuntando al mismo proyecto de LiveKit Cloud.
