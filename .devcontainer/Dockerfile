# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS go-toolchain
FROM node:24.18.0-bookworm-slim AS node-toolchain
FROM arigaio/atlas:1.2.3-alpine AS atlas-tool
FROM alpine:3.23 AS helm-tool

ARG TARGETARCH
ARG HELM_VERSION=v4.2.0
RUN apk add --no-cache ca-certificates curl tar \
    && curl -fsSLO "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
    && curl -fsSLO "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" \
    && sha256sum -c "helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" \
    && tar -xzf "helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
    && mv "linux-${TARGETARCH}/helm" /helm

FROM espressif/idf:release-v5.4

# Go lives in the same development container as ESP-IDF, Zig and Python. The
# module cache is intentionally container-local; only source files are mounted.
COPY --from=go-toolchain /usr/local/go /usr/local/go
COPY --from=node-toolchain /usr/local/bin/node /usr/local/bin/node
COPY --from=node-toolchain /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=atlas-tool /atlas /usr/local/bin/atlas
COPY --from=helm-tool /helm /usr/local/bin/helm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV PATH="/usr/local/go/bin:${PATH}"

# uv: agente + token server + scripts (gestiona sus propios Python)
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

# El entrypoint de la imagen ya exporta el entorno IDF, pero los shells del
# devcontainer (VS Code / docker exec) no pasan por él: bashrc cubre los
# interactivos y profile.d los de login (docker compose exec bash -lc).
RUN echo '. "$IDF_PATH/export.sh" > /dev/null 2>&1 || true' >> /etc/bash.bashrc \
    && echo '. "$IDF_PATH/export.sh" > /dev/null 2>&1 || true' > /etc/profile.d/esp-idf.sh
