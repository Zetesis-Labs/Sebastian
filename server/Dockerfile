# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sebastian-api ./cmd/api
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sebastian-migrate ./cmd/migrate
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sebastian-outbox ./cmd/outbox

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/sebastian-api /usr/local/bin/sebastian-api
COPY --from=builder /out/sebastian-migrate /usr/local/bin/sebastian-migrate
COPY --from=builder /out/sebastian-outbox /usr/local/bin/sebastian-outbox

EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/sebastian-api"]
