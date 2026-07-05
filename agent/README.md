# Sebastian — voice agent (Python)

Conversational brain of the speaker. Runs on your Mac (or in the cloud) and joins the
same LiveKit room as the ESP32-S3 device.

Stack: [`livekit-agents`](https://github.com/livekit/agents) 1.6 + configurable realtime
model (`gemini` by default, `openai` as fallback) +
BVC noise cancellation.

## Requirements

- Python ≥ 3.13
- [`uv`](https://docs.astral.sh/uv/) (`brew install uv`)
- LiveKit Cloud account
- `GOOGLE_API_KEY` for Gemini Live, or `OPENAI_API_KEY` if using the OpenAI fallback

## Getting started

```bash
cp .env.example .env      # fill in LIVEKIT_* and GOOGLE_API_KEY
uv sync
uv run agent.py dev       # worker in development mode (hot-reload)
```

The worker stays waiting. When the ESP32-S3 (or any client) enters a
room, LiveKit dispatches a job and the agent joins automatically.

## Testing without the board

Before having the firmware working you can talk to the agent from the
browser with the [Agents Playground](https://agents-playground.livekit.io/) or
`lk room join`, pointing to the same LiveKit Cloud project.
