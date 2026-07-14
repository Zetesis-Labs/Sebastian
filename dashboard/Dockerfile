# syntax=docker/dockerfile:1.7

FROM node:24.18.0-bookworm-slim AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY . .
RUN npm run typecheck && npm run test && npm run build

FROM node:24.18.0-bookworm-slim
ENV NODE_ENV=production PORT=3001 HOST=0.0.0.0
WORKDIR /app
COPY --from=builder /app/.output ./.output
USER node
EXPOSE 3001
CMD ["node", ".output/server/index.mjs"]
