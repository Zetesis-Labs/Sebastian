> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# infra-selfhost — Despliegue productivo: LiveKit self-hosted + token service + agente Sebastian en cortes (Talos/GitOps), con decisión cloud vs self-host

**Veredicto:** viable con riesgos — Todo encaja con la infra existente (ArgoCD + Infisical + cert-manager en cortes) y el SDK C del firmware ya soporta ws:// y wss:// con CA bundle sin tocar código. Riesgos: hostNetwork en nodo único (un pod LiveKit por nodo, cortes sin HA) y pérdida de BVC al salir de LiveKit Cloud (mitigable: el beam ASR del XVF3800 ya hace AEC+beamforming en silicio).

**Esfuerzo:** M — 3,5 a 5 días de una persona (sin contar el upgrade a livekit-agents 1.6.4, que es otra misión; +1 d si se quiere wss con DNS interno + cert LE)

## Hallazgos
- El SDK C de LiveKit vendorizado acepta ws:// y wss:// y ya adjunta el CA bundle de ESP-IDF (esp_crt_bundle_attach) al websocket de señalización: wss:// con cert Let's Encrypt funciona sin cambios de firmware; ws:// en LAN también es válido (el audio va cifrado igualmente por DTLS-SRTP, solo el signaling/JWT iría en claro).  
  _firmware/managed_components/livekit__livekit/core/url.c:56 y core/signaling.c:261_
- El firmware conecta con URL+token estáticos gitignorados (sandbox Cloud con exp ~agosto 2026): migrar a self-host es cambiar 2 strings en secrets.zig o pedir el token por HTTP a un token service.  
  _firmware/main/app.zig:183 y firmware/main/secrets.zig:2-3_
- Helm chart oficial livekit/livekit-server: requiere hostNetwork (puertos RTC directos en el nodo, 1 pod por nodo), UDP 50000-60000 + TCP 7881 + señalización 7880; Redis es OPCIONAL — solo necesario para multi-réplica o egress/ingress, así que en cortes (single-node, single-replica) no hace falta. TURN/coturn tampoco en LAN doméstica (dispositivo y servidor en la misma red).  
  _https://docs.livekit.io/transport/self-hosting/kubernetes/ y https://github.com/livekit/livekit/blob/master/config-sample.yaml ("when redis is set, LiveKit will automatically operate in a fully distributed fashion")_
- En Talos el rango NodePort por defecto (30000-32767) no cubre 50000-60000, así que NodePort queda descartado: hostNetwork es la opción correcta y Talos lo permite sin fricción en un nodo control-plane único.  
  _px-platon/cortes/talconfig.yaml:11-13 (nodo único cortes 10.0.0.151)_
- Pricing LiveKit Cloud (julio 2026): Build gratis = 5.000 min WebRTC + 1.000 min de agent session/mes, 100 conexiones concurrentes; Ship $50/mes = 150.000 min WebRTC + 5.000 min agent, overage $0,01/min. Un agente SELF-HOSTED que conecta a Cloud cuenta como participante WebRTC normal (no consume agent-session minutes, que son solo para agentes desplegados en Cloud). Dispositivo 24/7 + agente 24/7 = 86.400 min WebRTC/mes: revienta el free tier (5.000) pero cabe en Ship a $50/mes; bajo demanda (~180 min/mes) cabe de sobra en el free tier.  
  _https://livekit.com/pricing_
- BVC/Krisp sigue siendo solo-LiveKit-Cloud en 2026: con servidor self-hosted el filtro falla con "audio filter cannot be enabled: LiveKit Cloud is required". Alternativas self-host: plugin OSS livekit-plugins-dtln (Aloware, ONNX in-process, drop-in) o prescindir del filtro y confiar en el beam ASR post-AEC del XVF3800 (que es justo lo que el firmware ya publica).  
  _https://github.com/livekit/livekit/issues/4029 y https://aloware.github.io/livekit-plugins-dtln/_
- livekit-agents 1.6.4 (24-jun-2026) es la versión actual; el turn detector se precachea con `download-files` en el build de la imagen, corre en CPU con <500 MB RAM (v1-mini), y el worker expone health check HTTP en :8081. LiveKit recomienda 4 cores/8GB para 10-25 jobs concurrentes → para 1 sesión doméstica bastan ~1 CPU / 2Gi.  
  _https://pypi.org/project/livekit-agents/ y https://docs.livekit.io/deploy/custom/deployments/_
- El patrón GitOps del usuario ya contempla apps ajenas al monorepo ZP dentro de Mileto (langfuse, herschel): lo natural es un directorio manifests/sebastian/ + Application ArgoCD dedicada en apps/, con la imagen construida por CI del repo Sebastian — el monorepo ZetesisPortal no se entera.  
  _px-platon/cortes/manifests/ y px-platon/cortes/apps/zetesis-portal/applicationset.yaml_
- El ROADMAP ya prevé exactamente esto en P2 (self-host LiveKit + agente en cortes) y como economía del siempre-conectado propone timeout de sala en ARMED como alternativa (a), lo que haría viable el free tier en P0/P1.  
  _ROADMAP.md:99-105 y ROADMAP.md:160_

## Diseño

# Despliegue Sebastian: LiveKit self-hosted + token service + agente en cortes

## Arquitectura objetivo (P2)

```
ESP32-S3 ──ws(s)://──► livekit-server (cortes, hostNetwork 10.0.0.151:7880)
   ▲  token JWT            ▲ UDP 50000-50100 (media DTLS-SRTP, LAN directa)
   │                       │
token-service (ClusterIP + Ingress interno)   sebastian-agent (Deployment,
   livekit-api Python, mint JWT + dispatch      livekit-agents 1.6.4 start,
   con agent_name="sebastian")                  LIVEKIT_URL=ws://livekit:7880)
```

Todo en namespace `sebastian`, ArgoCD Application dedicada, secretos vía Infisical.

## Paso 1 — LiveKit server (Helm)

- `helm repo add livekit https://helm.livekit.io` → chart `livekit/livekit-server` como Application ArgoCD tipo Helm en `px-platon/cortes/apps/sebastian.yaml` (multi-source: chart + values en Mileto).
- **Sin Redis** (réplica única, sin egress/ingress), **sin TURN** (LAN; el ESP32 y el server comparten red — coturn solo si algún día hay acceso desde fuera, y entonces por Tailscale mejor).
- `hostNetwork` implícito del chart (puertos RTC directos). En Talos: NodePort no sirve (rango 30000-32767 < 50000); hostNetwork funciona sin config extra. Reducir el rango UDP a 100 puertos para un caso doméstico.
- `loadBalancerType: disable`; el 7880 se expone o por hostNetwork directo (`ws://10.0.0.151:7880`) o por Ingress interno con TLS si se quiere wss.
- API key/secret: generar con `livekit-server generate-keys` y subir a Infisical (`/sebastian`); el chart acepta `existingSecret` o keys inline — usar values con placeholder + ExternalSecret (ver riesgo de keys en values).

## Paso 2 — TLS y firmware

- **P2 pragmático**: `ws://10.0.0.151:7880` directo. Cero TLS; el audio va cifrado por DTLS-SRTP igualmente; solo signaling+JWT en claro dentro de la LAN. Cambio en firmware: solo `secrets.zig`.
- **Opcional (wss)**: cert-manager ya presente en cortes → Certificate Let's Encrypt (DNS-01) para `livekit.<dominio>` apuntando a 10.0.0.151 en DNS interno. El SDK C ya usa `esp_crt_bundle_attach` (signaling.c:261) y el bundle Mozilla incluye ISRG Root X1 → funciona sin tocar firmware (SNTP ya está).

## Paso 3 — Token service

- FastAPI + `livekit-api` (~40 líneas): `GET /token?identity=esp32-respeaker&room=sebastian` → JWT con `RoomJoin` + `RoomConfiguration(agents=[RoomAgentDispatch(agent_name="sebastian")])` (explicit dispatch del roadmap P0.6). TTL corto para clientes web, TTL largo (~90 días) para el dispositivo.
- Imagen `python:3.13-slim` + uv; Deployment 1 réplica (50m/128Mi), Service ClusterIP, Ingress interno (misma clase que el resto de cortes). Secret `sebastian-secrets` (LIVEKIT_API_KEY/SECRET) vía ExternalSecret.
- P0-P1: el dispositivo puede seguir con token estático embebido minteado a mano (`lk token create`); el token service entra cuando haya rotación o más dispositivos.

## Paso 4 — Agente Sebastian (Deployment)

- Dockerfile en `agent/` (repo Sebastian): uv + python 3.13, `uv run agent.py download-files` en build para cachear turn detector v1-mini (HF_HOME dentro de la imagen). Ver snippet.
- Cambios de código previos (otra misión, pero condicionan el deploy): upgrade a livekit-agents 1.6.4, `agent_name="sebastian"` en WorkerOptions (explicit dispatch), **quitar BVC** cuando el server sea self-host (gate por env var `LIVEKIT_CLOUD=0`): el beam ASR del XVF ya llega limpio; si hiciera falta, `livekit-plugins-dtln` como sustituto OSS.
- Recursos: requests 500m/1Gi, limits 2/2Gi (1 sesión + turn detector CPU <500MB). Liveness/readiness: HTTP GET :8081 (health del worker). `terminationGracePeriodSeconds: 600` para no cortar conversaciones en rollouts.
- Env: `LIVEKIT_URL=ws://livekit-livekit-server.sebastian.svc:7880` (¡el agente va por Service interno, no por hostNetwork!), `LIVEKIT_API_KEY/SECRET` y `OPENAI_API_KEY` desde `sebastian-secrets`.

## Paso 5 — Encaje GitOps (Mileto)

Recomendación: **manifests en Mileto + imagen desde el repo Sebastian** (patrón ya usado con langfuse/herschel; el monorepo ZP no conoce Sebastian y ArgoCD ya confía en Mileto):
1. `manifests/infisical/components/secrets-sebastian/` (ExternalSecret) — secretos en Infisical path `/sebastian` del proyecto cortes-cluster.
2. `manifests/sebastian/` (kustomize: namespace, token-service, agent Deployment) + `helm/values-livekit.yaml`.
3. `apps/sebastian-project.yaml` + `apps/sebastian.yaml` (Application multi-source: chart livekit + kustomize; sync-wave 0 secretos → 1 apps).
4. CI en repo Sebastian (GH Actions): build+push `ghcr.io/<user>/sebastian-agent` y `sebastian-token` con tag por SHA/semver; bump manual del tag en Mileto (o Renovate/Image Updater más adelante). No tocar `envs/prod/env.json` de zetesis-portal.

## Tabla de costes (pricing jul-2026)

| Escenario | Min/mes | Cloud Build (free) | Cloud Ship ($50) | Self-host cortes |
|---|---|---|---|---|
| Dispositivo 24/7 + agente self-hosted worker | 86.400 WebRTC | ✗ (>5.000) | ✓ $50/mes | $0 marginal |
| Ídem con agente hosteado en Cloud | 43.200 WebRTC + 43.200 agent | ✗ | ~$432/mes ($50 + 38.200×$0,01) | n/a |
| Bajo demanda (timeout sala en ARMED, ~90 min uso) | ~180 WebRTC | ✓ $0 | — | $0 |
| Self-host | — | — | — | $0 + operación (~1 pod, sin Redis) |

## Recomendación por fase

- **P0-P1 (dev)**: seguir en LiveKit Cloud **free tier** implementando el timeout de sala en ARMED (ROADMAP opción a). BVC disponible; iteración sin operar infra. Renovar el token sandbox (expira ~ago-2026) o pasar ya a `lk token create` con proyecto propio.
- **P2 (producción doméstica)**: self-host en cortes con ws:// LAN, token service, agente como Deployment, sin BVC (beam XVF; DTLN si hiciera falta). $0/mes, la voz no sale de casa salvo el tramo OpenAI Realtime, y habilita el siempre-conectado sin contar minutos.

## Esfuerzo

Helm LiveKit + values: 0,5 d · token service: 0,5 d · Dockerfile+Deployment agente: 1 d · wiring GitOps/Infisical/CI: 1 d · pruebas E2E con dispositivo (ws, latencia, sin BVC): 1 d.

## Código
**px-platon/cortes/helm/values-livekit.yaml (nuevo, en Mileto)** — values de Helm para livekit/livekit-server en cortes (single-node, LAN, sin Redis ni TURN) — Mileto: px-platon/cortes/helm/values-livekit.yaml

```yaml
replicaCount: 1
terminationGracePeriodSeconds: 300

livekit:
  log_level: info
  rtc:
    use_external_ip: false        # LAN: anuncia la IP del nodo (10.0.0.151)
    port_range_start: 50000
    port_range_end: 50100         # 100 puertos bastan para uso domestico
    tcp_port: 7881
  # sin redis: replica unica, sin egress/ingress
  keys:
    # generar con: docker run --rm livekit/livekit-server generate-keys
    # inyectado via values secreto o ExternalSecret + helm secret ref
    APIsebastian: "<LIVEKIT_API_SECRET>"
  turn:
    enabled: false                # LAN directa; sin coturn

loadBalancer:
  type: disable                   # nada de LB cloud; hostNetwork expone 7880

resources:
  requests: { cpu: 250m, memory: 256Mi }
  limits:   { cpu: "2",  memory: 1Gi }
```

**agent/Dockerfile (nuevo, repo Sebastian)** — Dockerfile del agente (repo Sebastian, agent/Dockerfile): uv + python 3.13 + precache del turn detector en build

```dockerfile
FROM python:3.13-slim
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /usr/local/bin/

ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy \
    HF_HOME=/app/.cache/huggingface
WORKDIR /app

COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev

COPY agent.py ./
# descarga los pesos del turn detector v1-mini (y demas modelos) a la imagen
RUN uv run agent.py download-files

# usuario no-root (la imagen corre en cortes)
RUN useradd -m agent && chown -R agent:agent /app
USER agent

EXPOSE 8081  # health check del worker
CMD ["uv", "run", "agent.py", "start"]
```

**px-platon/cortes/manifests/sebastian/agent-deployment.yaml (nuevo, en Mileto)** — Deployment del agente + ExternalSecret siguiendo el patron Infisical de cortes — Mileto: manifests/sebastian/ y manifests/infisical/components/secrets-sebastian/

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
  strategy: { type: Recreate }   # un worker; evita doble dispatch en rollout
  template:
    spec:
      terminationGracePeriodSeconds: 600   # deja acabar la conversacion
      containers:
        - name: agent
          image: ghcr.io/OWNER/sebastian-agent:v0.1.0
          env:
            - name: LIVEKIT_URL
              value: ws://livekit-livekit-server.sebastian.svc.cluster.local:7880
            - name: LIVEKIT_CLOUD
              value: "0"          # gate en agent.py: sin BVC en self-host
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

## Riesgos
- **hostNetwork en nodo único: cualquier otro pod que use los puertos 7880-7881/50000-50100 colisiona, y un upgrade del chart corta las salas activas (sin HA posible con 1 nodo).** → Rango UDP reducido (50000-50100), Recreate strategy asumida, y ventana de mantenimiento; el dispositivo ya reconecta solo al arrancar (requireLiveKitOk + reintentos).
- **Pérdida de BVC al self-hostear: hoy agent.py depende de noise_cancellation.BVC() y falla en server self-hosted ("LiveKit Cloud is required").** → Gate por env var para desactivar BVC fuera de Cloud; el beam ASR post-AEC del XVF3800 ya es la señal limpia (decisión previa del firmware); plan B: livekit-plugins-dtln (OSS, ONNX, in-process).
- **API keys de LiveKit inline en values de Helm (el chart las espera en livekit.keys) acabarían en git de Mileto.** → Values con placeholder + secret plugin/SOPS ya usado en Mileto (inline-secrets.sops.yaml existe), o montar la config completa desde un Secret generado por ExternalSecret en vez de keys inline.
- **ws:// en LAN expone el JWT de señalización en claro; y si mañana se quiere acceso fuera de casa, faltará TURN/TLS.** → El media va siempre DTLS-SRTP; para remoto usar Tailscale (ya desplegado en cortes: manifests/tailscale) en lugar de abrir TURN a internet; wss interno con cert-manager como mejora incremental.
- **Token estático embebido en firmware caduca (el actual expira ~agosto 2026) y obliga a reflashear.** → Token service con TTL 90 días + endpoint HTTP que el firmware consulte al boot (P2); mientras tanto, mintear tokens largos con lk CLI y documentar la fecha.
- **Free tier Cloud en P0/P1 se agota si el timeout de sala en ARMED no se implementa (86.400 min/mes ≫ 5.000).** → Implementar primero la desconexión en ARMED (ROADMAP opción a); con ~180 min/mes reales sobra margen; si se agota, Ship $50/mes como colchón temporal.
- **Verificación pendiente de si cortes tiene el ingress firewall de Talos activo (bloquearía UDP 50000-50100 y 7880/7881 hacia el nodo).** → Revisar talconfig.yaml/patches antes del despliegue y abrir los puertos en la NetworkRuleConfig si existe.

## Preguntas abiertas
- ¿Registry destino para las imágenes sebastian-agent/sebastian-token: ghcr.io del usuario o el Harbor del cluster primario (px-socrates)? Condiciona los imagePullSecrets en cortes.
- ¿Tiene cortes el ingress firewall de Talos configurado (NetworkRuleConfig)? Si sí, hay que abrir UDP 50000-50100 + TCP 7880/7881 explícitamente.
- ¿Se quiere wss:// desde el día uno (requiere DNS interno resolviendo livekit.<dominio> → 10.0.0.151 + cert DNS-01) o basta ws:// LAN en P2?
- ¿El path de Infisical será /sebastian en el proyecto cortes-cluster (nuevo) o se reutiliza /zetesis-portal? Recomendado path propio.
- ¿Cuándo se hace el upgrade a livekit-agents 1.6.4 + explicit dispatch (agent_name)? El Deployment lo asume; con 1.2 el agente auto-dispatcharía a cualquier sala.
- Al quitar BVC en self-host, ¿basta el beam ASR del XVF en entorno real (TV/música de fondo) o hay que integrar livekit-plugins-dtln? Requiere prueba A/B con grabaciones.

## Fuentes
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