# MILESTONE — living-room-ready

> The gate between a **bench prototype** (everything on the Mac, device tethered by
> USB, "LAN of trust") and a **living-room deployment** (device on its own, no
> cable, failures analyzed after the fact). The ROADMAP describes where Sebastian
> is *going*; this document is the single door it has to walk through *first*.
>
> **This is not a feature list.** Every item below is an **exit criterion** with a
> concrete test. The milestone is done when every box is checked — not when the
> features feel done.

---

## Why this exists

The project changed category in the 2026-07-08/09 sessions — Helm chart, Harbor
images, release-please, a public web installer, an ingress. The *capability* work
crossed into "deployed service". The *productionization* work did not. The
ROADMAP's own near-term plan (§6) is *"deploy in the living room in a few weeks,
mark failure moments, analyze later"* — and that plan is **not reachable today**
for three reasons the backlog never lists as items:

1. **The exposed HTTP surface has no authentication.** Self-hosting (§3 #2) is
   exactly what puts the token server and control plane behind the ingress.
2. **There is no way to update or recover a deployed device.** §6 says "freeze the
   firmware" — but a firmware you can't update remotely can only be recovered over
   USB.
3. **Device-side failure signals never reach production.** §6's own pre-deploy
   checklist admits this: telemetry leaves over the serial port, so in the living
   room it is lost — "analyze later" becomes "guess later".

Until these are closed, deploying in the living room means running blind on an
unauthenticated, unrecoverable device. This milestone closes them.

---

## Exit criteria

Grouped by theme. Each has: **state today**, **what's missing**, **exit test**
(how you know it's done), and the anchor in code / ROADMAP.

### A. Security of the exposed surface

The self-host milestone (§3 #2) is **not "done" until this group is done** — it is
the change that makes the surface internet-reachable, so its own security is in
scope, not a separate later task.

> **2026-07-11 decision:** A1/A2 will **not** be implemented incrementally on
> `token_server.py` / `control_plane.py`. That whole layer is being replaced by
> a single smart-speaker **provisioning server**, where auth, rate-limiting and
> OTA image serving are first-class design requirements. A1/A2 carry over as
> exit criteria for *that* server; A3/A4 remain as written.

- [ ] **A1 — `/token` requires authentication and is rate-limited.**
  - *Today:* `agent/token_server.py:82` serves a valid LiveKit JWT to any
    anonymous GET, **and** fires `create_dispatch` (a paid-LLM agent) per request.
    No rate limit. `docker-entrypoint.sh` binds `0.0.0.0`; the chart ships
    `ingress.enabled: true`.
  - *Missing:* a shared bearer token (device carries it in NVS, provisioned by the
    installer) or mTLS; a per-source rate limit; dispatch only after auth passes.
  - *Exit test:* an unauthenticated request from off-LAN returns `401` and creates
    **zero** dispatches; a flood from one source is throttled. Verified against the
    deployed ingress, not just localhost.

- [ ] **A2 — `/announce` requires authentication and bounds input.**
  - *Today:* `agent/control_plane.py:69` lets any POST make the device speak
    arbitrary text; no length cap; same public ingress.
  - *Missing:* auth on the control-plane face; a `text` length limit; the caller
    (Grafana/HA/web) carries a credential.
  - *Exit test:* unauthenticated POST → `401`, device stays silent; an oversized
    body → `413`, not an unbounded TTS.

- [ ] **A3 — the token JWT does not travel in clear text off-device.**
  - *Today:* `firmware/main/token_http.c:14` fetches over plain HTTP
    (`crt_bundle_attach = NULL`); the JWT (a bearer credential) crosses the WiFi in
    the clear.
  - *Missing:* TLS on the token server (or an explicit, documented decision that the
    device and server share an isolated VLAN where this is acceptable — written
    down, not assumed).
  - *Exit test:* a packet capture of a session open shows no readable JWT, **or**
    the VLAN-isolation decision is documented in this file with its threat model.

- [ ] **A4 — no default-insecure credentials in the deploy path.**
  - *Today:* `helm/sebastian/values.yaml:45` defaults `keyName: "devkey"` with
    `existingSecret: ""`.
  - *Exit test:* a render with no secret configured **fails** (does not silently run
    with the dev key); the LiveKit SFU pod gets a `securityContext` (it runs root +
    hostNetwork today).

### B. Recoverability (you can fix a device without touching it)

The "freeze the firmware" thesis (§6) is only safe if a frozen firmware can still
be updated. Otherwise the freeze locks in every bug.

- [ ] **B1 — OTA firmware update works end-to-end.**
  - *Today:* `firmware/partitions.csv` has a single `factory` partition — no
    `ota_0/ota_1`/`otadata`, no OTA code. USB is the only update path.
  - *Missing:* dual-OTA partition table, an OTA client, a rollback-on-failed-boot
    guard, and a place to serve signed images.
  - *Exit test:* a device in the living room takes a new firmware over the network,
    boots it, and **rolls back automatically** if the new image fails to come up.

- [ ] **B2 — secrets at rest are not extractable with physical access.**
  - *Today:* Secure Boot and Flash Encryption are SUPPORTED/PREFERRED but **not
    enabled** in `firmware/sdkconfig`; WiFi creds + token URL live in NVS in clear.
  - *Exit test:* a flash dump of a provisioned device yields no readable WiFi
    password or token URL. (Sequence this carefully — flash encryption is
    one-way; validate on a sacrificial unit first.)

- [ ] **B3 — a boot failure recovers itself.**
  - *Today:* `firmware/main/app.zig:610-666` — any failure in board/network/codec/
    audio/wakeword init does `return` from `app_main`; the device hangs until a
    manual power-cycle. Only the session path has a watchdog.
  - *Missing:* reboot-on-boot-failure with bounded backoff; a boot-loop guard that
    falls back to a safe/provisioning state instead of bricking.
  - *Exit test:* inject a transient I2C failure at boot → the device reboots and
    recovers unattended, and a persistent failure lands in provisioning mode, not a
    hang.

- [ ] **B4 — clock is verified before the first TLS handshake.**
  - *Today:* `firmware/main/app.zig:254-259` does a blind `vTaskDelay(3000)` after
    starting SNTP; if NTP is slow/fails, the `wss://` cert validation fails with no
    clear diagnostic.
  - *Exit test:* SNTP is confirmed synced (or retried) before `joinRoom`; a slow NTP
    server delays the connect instead of failing it.

### C. Observability (a living-room failure is analyzable)

This is §6's own pre-deploy checklist, promoted here from "partial / #3.5" to a
hard gate — because without it, criterion **#1 of the ROADMAP backlog ("validate
in daily use") is not executable**: you cannot validate what you cannot see.

- [ ] **C1 — device vitals reach production without USB.**
  - *Today:* heap / mic level / echo / wake probs / SCTP / AEC state go out the
    serial port → `bridge.py` → OTLP, which needs a tethered host (§6). In the
    living room, lost.
  - *Missing:* push firmware vitals over the LiveKit data channel (or a small
    UDP/MQTT path) to Grafana. (ROADMAP §3.5 "Firmware telemetry batch", Pending.)
  - *Exit test:* with **no USB attached**, heap trend, mic level, echo and wake
    events appear on a Grafana dashboard.

- [ ] **C2 — device and agent logs share a correlation ID.**
  - *Today:* aligned by wall clock only, because both run on one machine (§6).
  - *Exit test:* a single session/room ID appears on both the device telemetry and
    the agent logs, joinable in one query.

- [ ] **C3 — LGTM (or equivalent) runs 24/7 with weeks of retention on Cortes.**
  - *Today:* devcontainer default-retention image on the Mac — "a Tuesday failure
    is gone by Thursday" (§6).
  - *Exit test:* a failure from N days ago is still queryable.

- [ ] **C4 — "mark this moment" exists.**
  - *Missing:* a button gesture or voice command that drops a correlated marker into
    the logs (§6 checklist).
  - *Exit test:* during a failure the user marks it, and the marker is a jump target
    in Grafana — not timestamp archaeology.

### D. Privacy of the always-near-a-mic device

- [ ] **D1 — session recording is opt-in with retention.**
  - *Today:* `agent/audio_input.py:49` — `SEBASTIAN_RECORD` defaults **on**; every
    session writes two WAVs to `recordings/` with no consent gate, no encryption, no
    retention, unbounded disk growth. Pre-roll also dumps to
    `/tmp/sebastian_preroll.wav`.
  - *Missing:* default **off**; when on, a retention/rotation policy and a documented
    consent/storage model. (The forensics value in §2/§6 is real — this doesn't
    forbid recording, it gates it.)
  - *Exit test:* a fresh deploy records nothing until explicitly enabled; when
    enabled, old recordings are pruned on a policy and disk cannot fill unbounded.

### E. Foundation

- [ ] **E1 — self-hosted LiveKit runs on Cortes via Mileto GitOps.**
  - *Today:* dev edition shipped and E2E-validated in the devcontainer (§3 #2 [x]).
    Remaining: the Cortes deploy (Helm/ArgoCD/Infisical + UDP/node-ip plumbing).
  - *Note:* the BVC loss on self-host is **already resolved** by the path-B comms
    channel (§5 / §6) — not a blocker here.
  - *Exit test:* a device provisioned against `wss://livekit.<lan>` completes a full
    wake→talk→response loop through the Cortes SFU, keys sourced from Infisical.

---

## Explicitly OUT of scope for this milestone

To stop the platform ambition from creeping into the gate. These are real and
good — they are just *after* the door, not part of it:

- **§8 multi-room / arbitration** — needs a second physical unit; untestable now.
- **§9 full control plane** (MCP face, `record_note`, music publisher, web UI) —
  the shipped `announce(text)` HTTP slice is enough platform for a living-room
  device. The rest is additive later.
- **§9.3 always-connected / endpoint mode to `main`** — see the caveat below; it
  changes the recovery model and must not merge until B/C are in place.
- **Full-duplex double-talk perfection** (§5, `PP_DTSENSITIVE`) — half-duplex is
  the robust daily driver. Timebox **one** home session on the knob; if it doesn't
  land clean, freeze in half-duplex and move on. Do not let it block the milestone.

### A load-bearing caveat on endpoint mode

§9.3 marks always-connected as "SHIPPED v1", but it lives in unmerged branches and
carries a production-grade trap: *"the client SDK lies about room state after a
server-side room delete (stuck CONNECTED/RECONNECTING forever)"*. A wake-gated
device self-recovers (fresh room per wake); an always-connected one can go
**silently dead** in the living room. **Do not merge endpoint mode to `main` until
the app-level liveness ping/pong (its own TODO #1) exists** and C1 telemetry would
surface a wedged device. Until then, wake-gated is the deployable model.

---

## Definition of done

The milestone is complete when:

- Every box in **A–E** is checked and its exit test passed **against the deployed
  Cortes stack**, not localhost.
- A device runs **unattended in the living room for one week**, and every failure
  in that week is **reconstructable from Grafana alone** (no USB, no "I think it
  was around Tuesday").
- Recovering a bad device requires **no physical access** (OTA + auto-reboot cover
  it).

Only then does ROADMAP §3 #1 ("validation in daily real-world use") actually begin
— with the instrumentation to make it mean something — and the §6/§7/§9 platform
work becomes worth starting.

---

## Suggested order within the milestone

The exit criteria are unordered as *requirements*; as *work*, this sequence
front-loads the cheapest risk-retirement:

1. **C1–C3 observability** first — you want eyes on before you change anything else,
   and it's the thing that makes the rest debuggable.
2. **A1–A4 auth**, bundled into the **E1 Cortes self-host** deploy — they ship
   together because self-host is what exposes the surface.
3. **D1 recording opt-in** — a one-line default flip plus a retention job; cheap,
   high privacy payoff.
4. **B3–B4 boot recovery + SNTP** — firmware reliability hygiene (allowed under the
   §6 freeze, it makes the endpoint safer, not more featureful).
5. **B1–B2 OTA + secure boot** — the largest single piece; do it last but **before**
   the one-week unattended run, because that run needs remote recovery to exist.
