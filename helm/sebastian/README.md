# sebastian (Helm chart)

Backend for the Sebastian smart speaker, deployed to Cortes via ArgoCD. Six
workloads:

| Workload | What | Exposure |
|----------|------|----------|
| `livekit` | Self-hosted LiveKit SFU | `hostNetwork` on the node's LAN IP — signalling `:7880` (ws), media UDP mux `:7882`. `--node-ip` from the downward API. |
| `nats` | Durable JetStream transport for domain events | ClusterIP only. |
| `server` | Go/OpenAPI control plane: device authentication, sessions, LiveKit dispatch and outbox (`:8787`) | `hostPort` on the node for the LAN device; optional ingress. |
| `dashboard` | React/TanStack Start administration panel (`:3001`) | Traefik ingress. |
| `agent` | LiveKit agent worker (Gemini/OpenAI + Home Assistant) | Outbound-only (no service). |
| `control-plane` | HTTP control face — `announce(text)`, … (`:8790`) | Traefik ingress (internal automations). |

## Values

See `values.yaml` for the full set. Each component has `enabled`, `image`,
`existingSecret` and `resources`. Prod overrides live in
`Mileto-Infra-GitOps/px-platon/cortes/helm/sebastian-values*.yaml`.

Secret keys expected:

- **livekit** `existingSecret`: `LIVEKIT_KEYS` (`"<keyName>: <secret>"`).
- **server** `existingSecret`: `DATABASE_URL`, `LIVEKIT_URL`,
  `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET`, `SEBASTIAN_ADMIN_SECRET`; also `NATS_URL` when the managed NATS
  workload is disabled and `server.outbox.natsURL` is empty.
- **control-plane** `existingSecret`: `LIVEKIT_URL`, `LIVEKIT_API_KEY`,
  `LIVEKIT_API_SECRET`.
- **agent** `existingSecret` (loaded via `envFrom`): the LiveKit keys plus
  `GOOGLE_API_KEY` and/or `OPENAI_API_KEY`, `SEBASTIAN_HA_MCP_URL`,
  `SEBASTIAN_HA_TOKEN`.
- **dashboard** `existingSecret`: `SEBASTIAN_ADMIN_SECRET`; when empty it reuses
  `server.existingSecret`.

The server pod runs the outbox publisher as a separate container. By default it
connects to the chart-managed NATS service. Set `nats.enabled=false` for an
external cluster.

## Networking

The ESP32 device and the cluster node share a LAN. The device reaches LiveKit
(signalling + UDP media) and Sebastian Server directly on the node IP — no
Tailscale/ingress on the device path. Provision the device's `tokenServerUrl`
at `http://<node-ip>:8787/token`.
