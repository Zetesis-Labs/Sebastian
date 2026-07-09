# syntax=docker/dockerfile:1.7
#
# One image, three services. The Sebastian backend is a single uv package
# (this `agent/` dir) with three entrypoints — token server, LiveKit agent
# worker, control plane. We publish three images (sebastian/token-server,
# sebastian/agent, sebastian/control-plane) that differ ONLY in their default
# service, selected at build time via `--build-arg SERVICE=…`. Same code, same
# layers up to the final ARG — mirrors ZetesisPortal's per-service images while
# staying DRY (the code really is one package).
#
# Build context = this `agent/` directory.

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

COPY --from=builder /app /app

# Which service this image runs by default. Overridable at build time, and the
# entrypoint also honours a SEBASTIAN_SERVICE env override at run time.
ARG SERVICE=agent
ENV SEBASTIAN_SERVICE=${SERVICE}

RUN groupadd --system --gid 1001 sebastian \
 && useradd  --system --uid 1001 --gid sebastian sebastian \
 && chown -R sebastian:sebastian /app

USER sebastian

# token server :8787, control plane :8790 (agent worker has no inbound port).
EXPOSE 8787 8790

ENTRYPOINT ["/app/docker-entrypoint.sh"]
