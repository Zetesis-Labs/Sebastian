> **Annex to the implementation report** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-checking). Where this annex contradicts the **Frozen decisions** of the main report, the report prevails.

# infra-selfhost — Productive deployment: LiveKit self-hosted + token service + Sebastian agent in cortes (Talos/GitOps), with cloud vs self-host decision

**Verdict:** viable with risks — Everything fits with the existing infra (ArgoCD + Infisical + cert-manager in cortes) and the firmware C SDK already supports ws:// and wss:// with CA bundle without touching code. Risks: hostNetwork on a single node (one LiveKit pod per node, cortes without HA) and loss of BVC when leaving LiveKit Cloud (mitigable: the XVF3800's ASR beam already does AEC+beamforming in silicon).

**Effort:** M — 3.5 to 5 person-days (not counting the upgrade to livekit-agents 1.6.4, which is another mission; +1 d if wss with internal DNS + LE cert is desired)

## Findings
- The vendorized LiveKit C SDK accepts ws:// and wss:// and already attaches the ESP-IDF CA bundle (esp_crt_bundle_attach) to the signaling websocket: wss:// with Let's Encrypt cert works without firmware changes; ws:// in LAN is also valid (audio is still encrypted by DTLS-SRTP, only signaling/JWT would go in the clear).  
  _firmware/managed_components/livekit__livekit/core/url.c:56 and core/signaling.c:261_
- The firmware connects with gitignored static URL+token (Cloud sandbox with exp ~August 2026): migrating to self-host means changing 2 strings in secrets.zig or requesting the token via HTTP from a token service.  
  _firmware/main/app.zig:183 and firmware/main/secrets.zig:2-3_
- Official livekit/livekit-server Helm chart: requires hostNetwork (direct RTC ports on the node, 1 pod per node), UDP 50000-60000 + TCP 7881 + signaling 7880; Redis is OPTIONAL — only needed for multi-replica or egress/ingress, so in cortes (single-node, single-replica) it's not needed. TURN/coturn not needed in home LAN either (device and server on the same network).  
  _https://docs.livekit.io/transport/self-hosting/kubernetes/ and https://github.com/livekit/livekit/blob/master/config-sample.yaml ("when redis is set, LiveKit will automatically operate in a fully distributed fashion")_
- In Talos the default NodePort range (30000-32767) does not cover 50000-60000, so NodePort is ruled out: hostNetwork is the right choice and Talos allows it without friction on a single control-plane node.  
  _px-platon/cortes/talconfig.yaml:11-13 (single node cortes 10.0.0.151)_
- LiveKit Cloud pricing (July 2026): Build free = 5,000 WebRTC min + 1,000 agent session min/month, 100 concurrent connections; Ship $50/month = 150,000 WebRTC min + 5,000 agent min, overage $0.01/min. A SELF-HOSTED agent connecting to Cloud counts as a normal WebRTC participant (does not consume agent-session minutes, which are only for agents deployed in Cloud). 24/7 device + 24/7 agent = 86,400 WebRTC min/month: bursts the free tier (5,000) but fits in Ship at $50/month; on-demand (~180 min/month) fits well within the free tier.  
  _https://livekit.com/pricing_
- BVC/Krisp remains LiveKit-Cloud-only in 2026: with self-hosted server the filter fails with "audio filter cannot be enabled: LiveKit Cloud is required". Self-host alternatives: livekit-plugins-dtln OSS plugin (Aloware, ONNX in-process, drop-in) or do without the filter and rely on the XVF3800's post-AEC ASR beam (which is exactly what the firmware already publishes).  
  _https://github.com/livekit/livekit/issues/4029 and https://aloware.github.io/livekit-plugins-dtln/_
- livekit-agents 1.6.4 (24-Jun-2026) is the current version; the turn detector is pre-cached with `download-files` in the image build, runs on CPU with <500 MB RAM (v1-mini), and the worker exposes an HTTP health check on :8081. LiveKit recommends 4 cores/8GB for 10-25 concurrent jobs → for 1 home session ~1 CPU / 2Gi is enough.  
  _https://pypi.org/project/livekit-agents/ and https://docs.livekit.io/deploy/custom/deployments/_
- The user's GitOps pattern already contemplates apps external to the ZP monorepo within Mileto (langfuse, herschel): the natural fit is a dedicated manifests/sebastian/ directory + ArgoCD Application in apps/, with the image built by the Sebastian repo's CI — the ZetesisPortal monorepo doesn't know about it.  
  _px-platon/cortes/manifests/ and px-platon/cortes/apps/zetesis-portal/applicationset.yaml_
- The ROADMAP already foresees exactly this in P2 (LiveKit self-host + agent in cortes) and as an economy measure for the always-connected it proposes room timeout in ARMED as an alternative (a), which would make the free tier viable in P0/P1.  
  _ROADMAP.md:99-105 and ROADMAP.md:160_

## Design

# Sebastian Deployment: LiveKit self-hosted + token service + agent in cortes

## Target architecture (P2)

```
ESP32-S3 ──ws(s)://──► livekit-server (cortes, hostNetwork 10.0.0.151:7880)
   ▲  token JWT            ▲ UDP 50000-50100 (media DTLS-SRTP, direct LAN)
   │                       │
token-service (ClusterIP + internal Ingress)  sebastian-agent (Deployment,
   livekit-api Python, mint JWT + dispatch      livekit-agents 1.6.4 start,
   with agent_name="sebastian")                 LIVEKIT_URL=ws://livekit:7880)
```

Everything in namespace `sebastian`, dedicated ArgoCD Application, secrets via Infisical.

## Step 1 — LiveKit server (Helm)

- `helm repo add livekit https://helm.livekit.io` → chart `livekit/livekit-server` as Helm type ArgoCD Application in `px-platon/cortes/apps/sebastian.yaml` (multi-source: chart + values in Mileto).
- **No Redis** (single replica, no egress/ingress), **no TURN** (LAN; the ESP32 and server share the network — coturn only if there is outside access someday, and then better via Tailscale).
- Implicit `hostNetwork` from the chart (direct RTC ports). In Talos: NodePort doesn't work (range 30000-32767 < 50000); hostNetwork works without extra config. Reduce the UDP range to 100 ports for a home case.
- `loadBalancerType: disable`; port 7880 is exposed either by direct hostNetwork (`ws://10.0.0.151:7880`) or by internal Ingress with TLS if wss is desired.
- API key/secret: generate with `livekit-server generate-keys` and upload to Infisical (`/sebastian`); the chart accepts `existingSecret` or inline keys — use values with placeholder + ExternalSecret (see risk of keys in values).

## Step 2 — TLS and firmware

- **Pragmatic P2**: direct `ws://10.0.0.151:7880`. Zero TLS; audio is still encrypted by DTLS-SRTP anyway; only signaling+JWT in cleartext within the LAN. Firmware change: only `secrets.zig`.
- **Optional (wss)**: cert-manager already present in cortes → Let's Encrypt Certificate (DNS-01) for `livekit.<domain>` pointing to 10.0.0.151 in internal DNS. The C SDK already uses `esp_crt_bundle_attach` (signaling.c:261) and the Mozilla bundle includes ISRG Root X1 → works without touching firmware (SNTP is already there).

## Step 3 — Token service

- FastAPI + `livekit-api` (~40 lines): `GET /token?identity=esp32-respeaker&room=sebastian` → JWT with `RoomJoin` + `RoomConfiguration(agents=[RoomAgentDispatch(agent_name="sebastian")])` (explicit dispatch from P0.6 roadmap). Short TTL for web clients, long TTL (~90 days) for the device.
- Image `python:3.13-slim` + uv; Deployment 1 replica (50m/128Mi), Service ClusterIP, internal Ingress (same class as the rest of cortes). Secret `sebastian-secrets` (LIVEKIT_API_KEY/SECRET) via ExternalSecret.
- P0-P1: the device can continue with a static embedded token minted by hand (`lk token create`); the token service comes in when there is rotation or more devices.

## Step 4 — Sebastian Agent (Deployment)

- Dockerfile in `agent/` (Sebastian repo): uv + python 3.13, `uv run agent.py download-files` on build to cache v1-mini turn detector (HF_HOME inside the image). See snippet.
- Prior code changes (another mission, but they condition the deploy): upgrade to livekit-agents 1.6.4, `agent_name="sebastian"` in WorkerOptions (explicit dispatch), **remove BVC** when the server is self-host (gate by env var `LIVEKIT_CLOUD=0`): the XVF ASR beam already arrives clean; if needed, `livekit-plugins-dtln` as OSS replacement.
- Resources: requests 500m/1Gi, limits 2/2Gi (1 session + CPU turn detector <500MB). Liveness/readiness: HTTP GET :8081 (worker health). `terminationGracePeriodSeconds: 600` so as not to cut conversations on rollouts.
- Env: `LIVEKIT_URL=ws://livekit-livekit-server.sebastian.svc:7880` (the agent goes via internal Service, not hostNetwork!), `LIVEKIT_API_KEY/SECRET` and `OPENAI_API_KEY` from `sebastian-secrets`.

## Step 5 — GitOps Integration (Mileto)

Recommendation: **manifests in Mileto + image from the Sebastian repo** (pattern already used with langfuse/herschel; the ZP monorepo doesn't know Sebastian and ArgoCD already trusts Mileto):
1. `manifests/infisical/components/secrets-sebastian/` (ExternalSecret) — secrets in Infisical path `/sebastian` of the cortes-cluster project.
2. `manifests/sebastian/` (kustomize: namespace, token-service, agent Deployment) + `helm/values-livekit.yaml`.
3. `apps/sebastian-project.yaml` + `apps/sebastian.yaml` (multi-source Application: livekit chart + kustomize; sync-wave 0 secrets → 1 apps).
4. CI in Sebastian repo (GH Actions): build+push `ghcr.io/<user>/sebastian-agent` and `sebastian-token` with tag by SHA/semver; manual bump of the tag in Mileto (or Renovate/Image Updater later). Do not touch `envs/prod/env.json` from zetesis-portal.

## Cost table (July 2026 pricing)

| Scenario | Min/month | Cloud Build (free) | Cloud Ship ($50) | Self-host cortes |
|---|---|---|---|---|
| 24/7 device + self-hosted worker agent | 86,400 WebRTC | ✗ (>5,000) | ✓ $50/month | $0 marginal |
| Same with agent hosted in Cloud | 43,200 WebRTC + 43,200 agent | ✗ | ~$432/month ($50 + 38,200×$0.01) | n/a |
| On-demand (room timeout in ARMED, ~90 min use) | ~180 WebRTC | ✓ $0 | — | $0 |
| Self-host | — | — | — | $0 + operation (~1 pod, no Redis) |

## Recommendation by phase

- **P0-P1 (dev)**: stay on LiveKit Cloud **free tier** implementing room timeout in ARMED (ROADMAP option a). BVC available; iteration without operating infra. Renew the sandbox token (expires ~Aug-2026) or switch now to `lk token create` with own project.
- **P2 (home production)**: self-host in cortes with ws:// LAN, token service, agent as Deployment, without BVC (XVF beam; DTLN if needed). $0/month, voice doesn't leave the house except for the OpenAI Realtime segment, and enables always-connected without counting minutes.

## Effort

LiveKit Helm + values: 0.5 d · token service: 0.5 d · agent Dockerfile+Deployment: 1 d · GitOps/Infisical/CI wiring: 1 d · E2E tests with device (ws, latency, no BVC): 1 d.

## Code
**px-platon/cortes/helm/values-livekit.yaml (new, in Mileto)** — Helm values for livekit/livekit-server in cortes (single-node, LAN, no Redis or TURN) — Mileto: px-platon/cortes/helm/values-livekit.yaml

```yaml
replicaCount: 1
terminationGracePeriodSeconds: 300

livekit:
  log_level: info
  rtc:
    use_external_ip: false        # LAN: announces the node's IP (10.0.0.151)
    port_range_start: 50000
    port_range_end: 50100         # 100 ports are enough for home use
    tcp_port: 7881
  # no redis: single replica, no egress/ingress
  keys:
    # generate with: docker run --rm livekit/livekit-server generate-keys
    # injected via values secret or ExternalSecret + helm secret ref
    APIsebastian: "<LIVEKIT_API_SECRET>"
  turn:
    enabled: false                # direct LAN; without coturn

loadBalancer:
  type: disable                   # no cloud LB; hostNetwork exposes 7880

resources:
  requests: { cpu: 250m, memory: 256Mi }
  limits:   { cpu: "2",  memory: 1Gi }
```

**agent/Dockerfile (new, Sebastian repo)** — Agent Dockerfile (Sebastian repo, agent/Dockerfile): uv + python 3.13 + turn detector precache on build

```dockerfile
FROM python:3.13-slim
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /usr/local/bin/

ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy \
    HF_HOME=/app/.cache/huggingface
WORKDIR /app

COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev

COPY agent.py ./
# downloads the v1-mini turn detector weights (and other models) to the image
RUN uv run agent.py download-files

# non-root user (the image runs in cortes)
RUN useradd -m agent && chown -R agent:agent /app
USER agent

EXPOSE 8081  # worker health check
CMD ["uv", "run", "agent.py", "start"]
```

**px-platon/cortes/manifests/sebastian/agent-deployment.yaml (new, in Mileto)** — Agent Deployment + ExternalSecret following the cortes Infisical pattern — Mileto: manifests/sebastian/ and manifests/infisical/components/secrets-sebastian/

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: { name: sebastian-secrets }
spec:
  refreshInterval: 1m
  secretStoreRef: { name: SECRETSTORE_NAME, kind: ClusterSecretStore }
  target: { name: sebastian-secrets, creationPolicy: Owner }
  data:
    - { secretKey: LIVEKIT_API_KEY,    remoteRef: { key: LIVEKIT_API_KEY } }
    - { secretKey: LIVEKIT_API_SECRET, remoteRef: { key: LIVEKIT_API_SECRET } }
    - { secretKey: OPENAI_API_KEY,     remoteRef: { key: OPENAI_API_KEY } }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: sebastian-agent, namespace: sebastian }
spec:
  replicas: 1
  strategy: { type: Recreate }   # one worker; avoids double dispatch on rollout
  template:
    spec:
      terminationGracePeriodSeconds: 600   # lets the conversation finish
      containers:
        - name: agent
          image: ghcr.io/OWNER/sebastian-agent:v0.1.0
          env:
            - name: LIVEKIT_URL
              value: ws://livekit-livekit-server.sebastian.svc.cluster.local:7880
            - name: LIVEKIT_CLOUD
              value: "0"          # gate in agent.py: no BVC in self-host
          envFrom: [ { secretRef: { name: sebastian-secrets } } ]
          ports: [ { containerPort: 8081, name: health } ]
          livenessProbe:
            httpGet: { path: /, port: health }
            initialDelaySeconds: 15
          readinessProbe:
            httpGet: { path: /, port: health }
          resources:
            requests: { cpu: 500m, memory: 1Gi }
            limits:   { cpu: "2",  memory: 2Gi }
```

## Risks
- **hostNetwork on single node: any other pod using ports 7880-7881/50000-50100 collides, and a chart upgrade cuts active rooms (no HA possible with 1 node).** → Reduced UDP range (50000-50100), Recreate strategy assumed, and maintenance window; the device already reconnects automatically on boot (requireLiveKitOk + retries).
- **Loss of BVC when self-hosting: today agent.py depends on noise_cancellation.BVC() and fails on self-hosted server ("LiveKit Cloud is required").** → Gate by env var to disable BVC outside Cloud; the post-AEC ASR beam from the XVF3800 is already the clean signal (prior firmware decision); plan B: livekit-plugins-dtln (OSS, ONNX, in-process).
- **LiveKit API keys inline in Helm values (the chart expects them in livekit.keys) would end up in Mileto's git.** → Values with placeholder + plugin/SOPS secret already used in Mileto (inline-secrets.sops.yaml exists), or mount the full config from a Secret generated by ExternalSecret instead of inline keys.
- **ws:// in LAN exposes the signaling JWT in cleartext; and if outside access is desired tomorrow, TURN/TLS will be missing.** → Media is always DTLS-SRTP; for remote use Tailscale (already deployed in cortes: manifests/tailscale) instead of opening TURN to the internet; internal wss with cert-manager as an incremental improvement.
- **Static embedded token in firmware expires (current one expires ~August 2026) and forces reflashing.** → Token service with 90-day TTL + HTTP endpoint that the firmware queries at boot (P2); meanwhile, mint long tokens with lk CLI and document the date.
- **Cloud free tier in P0/P1 runs out if the room timeout in ARMED is not implemented (86,400 min/month ≫ 5,000).** → Implement disconnection in ARMED first (ROADMAP option a); with ~180 real min/month there is plenty of margin; if it runs out, Ship $50/month as a temporary cushion.
- **Pending verification if cortes has the Talos ingress firewall active (would block UDP 50000-50100 and 7880/7881 towards the node).** → Review talconfig.yaml/patches before deployment and open the ports in the NetworkRuleConfig if it exists.

## Open questions
- Target registry for the sebastian-agent/sebastian-token images: the user's ghcr.io or the primary cluster Harbor (px-socrates)? Conditions the imagePullSecrets in cortes.
- Does cortes have the Talos ingress firewall configured (NetworkRuleConfig)? If yes, UDP 50000-50100 + TCP 7880/7881 must be opened explicitly.
- Is wss:// desired from day one (requires internal DNS resolving livekit.<domain> → 10.0.0.151 + DNS-01 cert) or is LAN ws:// enough in P2?
- Will the Infisical path be /sebastian in the cortes-cluster project (new) or will /zetesis-portal be reused? Dedicated path recommended.
- When will the upgrade to livekit-agents 1.6.4 + explicit dispatch (agent_name) happen? The Deployment assumes it; with 1.2 the agent would auto-dispatch to any room.
- When removing BVC in self-host, is the XVF ASR beam enough in a real environment (TV/background music) or does livekit-plugins-dtln need to be integrated? Requires A/B testing with recordings.

## Sources
- https://docs.livekit.io/transport/self-hosting/kubernetes/
- https://github.com/livekit/livekit-helm/blob/master/server-sample.yaml
- https://github.com/livekit/livekit/blob/master/config-sample.yaml
- https://livekit.com/pricing
- https://docs.livekit.io/deploy/custom/deployments/
- https://github.com/livekit/livekit/issues/4029
- https://aloware.github.io/livekit-plugins-dtln/
- https://pypi.org/project/livekit-agents/
- https://docs.livekit.io/agents/build/turns/turn-detector
- https://github.com/livekit-examples/agent-deployment
- https://docs.livekit.io/transport/media/noise-cancellation/