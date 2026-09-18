# syntax=docker/dockerfile:1.7
# Build context is the REPO ROOT (docker build -f dashboard/Dockerfile .): the
# dashboard embeds the web installer (web-installer/) pre-filled with the
# control room, and reuses its form metadata for the device page.

FROM node:24.18.0-bookworm-slim AS builder
RUN corepack enable
WORKDIR /app
COPY dashboard/package.json dashboard/package-lock.json ./dashboard/
COPY web-installer/package.json web-installer/pnpm-lock.yaml ./web-installer/
RUN --mount=type=cache,target=/root/.npm cd dashboard && npm ci
RUN --mount=type=cache,target=/root/.local/share/pnpm cd web-installer && pnpm install --frozen-lockfile
COPY web-installer ./web-installer
COPY server/api/openapi.yaml ./server/api/openapi.yaml
COPY dashboard ./dashboard
WORKDIR /app/dashboard
RUN npm run typecheck && npm run test && npm run build

FROM node:24.18.0-bookworm-slim
ENV NODE_ENV=production PORT=3001 HOST=0.0.0.0
WORKDIR /app
COPY --from=builder /app/dashboard/.output ./.output
USER node
EXPOSE 3001
CMD ["node", ".output/server/index.mjs"]
