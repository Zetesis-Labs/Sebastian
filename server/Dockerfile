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

FROM gcr.io/distroless/static-debian12:nonroot

# ffmpeg cuts meeting audio into pieces for transcription (design 14 block D).
COPY --from=mwader/static-ffmpeg:7.1.1 /ffmpeg /usr/local/bin/ffmpeg
COPY --from=builder /out/sebastian-api /usr/local/bin/sebastian-api
COPY --from=builder /out/sebastian-migrate /usr/local/bin/sebastian-migrate

EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/sebastian-api"]
