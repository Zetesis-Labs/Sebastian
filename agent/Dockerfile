# syntax=docker/dockerfile:1.7
#
# The LiveKit agent worker (sebastian/agent). Build context = this `agent/`
# directory.

ARG BASE_IMAGE=ghcr.io/astral-sh/uv:python3.13-bookworm-slim

# ── Build stage ────────────────────────────────────────────────────────────
FROM ${BASE_IMAGE} AS builder

ENV UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_DOWNLOADS=never

WORKDIR /app

# Deps first (cached until the lock changes), then the source.
COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project

COPY . .
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev

# ── Runtime stage ──────────────────────────────────────────────────────────
FROM ${BASE_IMAGE} AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PATH="/app/.venv/bin:$PATH"

WORKDIR /app

# ffmpeg encodes the meeting audio (Ogg/Opus, design 14 block C).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app /app

RUN groupadd --system --gid 1001 sebastian \
 && useradd  --system --uid 1001 --gid sebastian sebastian \
 && chown -R sebastian:sebastian /app

USER sebastian

# The worker is outbound-only: it registers with the SFU and has no port.
CMD ["python", "agent.py", "start"]
