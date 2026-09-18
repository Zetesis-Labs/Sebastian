# Fleet provisioning and adoption from the control room — design

> Written 2026-09-18 from a design conversation with Rubén, after the "load from
> device" installer work. Status: **implemented the same day (PR
> `feat/fleet-adoption`)**; deviations from the first draft are noted inline.
> The reference model is UniFi: devices announce themselves on the LAN, any
> controller sees them, adoption binds a device to a controller, and the
> controller owns its configuration from then on.

## 1. Goal

Several **control rooms** (a Sebastian server + dashboard: today one on cortes,
one on Rubén's Mac) share a network with several speakers: some bound to one
control room, some to another, some never provisioned, some whose control room
is down. Opening *any* control room must show every speaker within reach with
its state, allow adopting the ones one is entitled to, and govern their
configuration afterwards **without USB**. USB (the web installer) remains for
first contact and rescue only.

## 2. Vocabulary and states

- **Control room**: a `sebastian-server` instance plus its dashboard. Identified
  by the origin of its API URL (`http://10.0.0.188:8787`) until it gets a
  configurable display name.
- **Bound**: the speaker's NVS `token_url` points at a control room.
- **Adoption**: the act of binding a speaker to a control room over the network.
  Payload-wise it is *the same* `sebastian.config.v1` JSON the web installer
  sends over USB (`web-installer/public/PROVISIONING.md`), so there is one
  provisioning path with three transports: USB serial, web installer, LAN.

States shown by a control room for each speaker it can see:

| State | Meaning | Source |
|-------|---------|--------|
| **Adopted here** | bound to this control room and polling it | `devices` table + poll |
| **Adopted here, absent** | in `devices`, not seen on the LAN nor polling for > 3 poll periods | `devices` only |
| **Managed by *X*** | announces another control room; read-only, shows *X* | mDNS |
| **Unadopted** | announces no control room (factory / forgotten) | mDNS |
| **Orphan** | announces a control room *and* reports its last contact failed | mDNS TXT `err` |

## 3. The four blocks (plus block 0)

Ordered by dependency. Each block is shippable on its own.

### Block 0 — the web installer inside the dashboard (independent)

The same React app in `web-installer/` is built twice: for GitHub Pages
(public, no control room, blank form — **stays**) and into the dashboard image
under `/installer`, **pre-filled with this control room**: token server URL,
syslog receiver, organization secret (§5). The operator only types the WiFi. A
speaker flashed and provisioned from there is born adopted.

- The factory image (`sebastian-esp32s3-merged.bin`) is attached to the
  firmware release by CI and packaged into the dashboard image of the matching
  server version, so each control room installs the firmware it is compatible
  with rather than "whatever Pages has today".
- A small authenticated endpoint returns the control room's base config for the
  pre-fill; the organization secret only over an authenticated dashboard session.
- **Web Serial needs a secure context**: `localhost` or HTTPS. A control room
  served over plain HTTP can still use it by adding its origin to
  `chrome://flags/#unsafely-treat-insecure-origin-as-secure` on that browser;
  the installer page must say so instead of showing a dead button. Cortes'
  dashboard is already HTTPS on the tailnet.

### Block 1 — per-device credentials and `/v1/sessions` (foundation)

Today the firmware fetches tokens from the unauthenticated legacy `/token`, so
every session is attributed to the fixed id `esp32-respeaker` while the real
units (`68ee8f4d8dd4`, `e072a1f895f4`) only exist through the profile poll.

- Firmware: `token.zig` calls `POST /v1/sessions` with `X-Device-Id` = MAC and
  `X-Device-Secret` from NVS (new key `dev_secret`); falls back to `/token`
  only when no secret is stored (unadopted unit on a legacy server).
- Server: `TouchProfile`'s auto-registration stays; a device without a secret
  is "registered, not adopted". Sessions and recordings hang off the real id.
- Born adopted (RF-03): a unit provisioned from the embedded installer holds
  the organization secret and no device secret. Before its first poll
  `control.zig` enrols: `GET /v1/devices/{id}/enroll` → nonce,
  `POST …/enroll {nonce, mac}` with `mac = HMAC-SHA256(orgSecret, nonce + "." +
  id)` (the adoption construction) → `{deviceSecret}`, stored in NVS through
  `provisioning.c`. The server marks the unit adopted like a network adoption
  would; the nonce is single-use, 60 s.
- Retire `SEBASTIAN_LEGACY_DEVICE_ID` once both units run the new firmware.

### Block 2 — announce and the "on the network" view

- Firmware advertises `_sebastian._tcp.local` (ESP-IDF `mdns` component; its
  allocations forced to PSRAM, see §7) with TXT records:

  | key | value |
  |-----|-------|
  | `id` | MAC, lowercase hex, as in `devices.id` |
  | `fw` | firmware version |
  | `prof` | running profile (`agente` / `micro-usb`) |
  | `cr` | origin of `token_url`, empty when unbound |
  | `err` | result of the last control-room contact: `ok`, `dns`, `timeout`, `http:503`… |
  | `cfg` | short hash of the running config (block 4) |

- Server: a discovery goroutine browses mDNS and keeps an in-memory
  "seen on LAN" table (not persisted; it is a live view). New endpoint
  `GET /v1/admin/lan-devices` joins it with `devices` into the states of §2.
- Dashboard: an "On the network" section on the Devices page with those
  states, and the *Adopt* / *Adopt by IP* actions of block 3.
- Multicast does not cross subnets. That is inherent to mDNS (UniFi has the
  same limit); an mDNS reflector on the router (VyOS/avahi) extends the view
  when wanted. Adoption never depends on discovery (block 3). On Kubernetes
  (cortes) the discovery goroutine needs host networking or a reflector; the
  server still works without it, the LAN view is just empty.

### Block 3 — adoption over the network, discovered or by IP

- Firmware: a UDP listener (port **5688**, receive buffer in PSRAM, stack in
  internal RAM because NVS writes disable the cache) accepting `hello` and
  `adopt`. Handling reuses `provisioning.c`'s
  `store_wifi` verbatim: store, reply, restart.
- Protocol, one round trip plus reply:
  1. Control room → device: `{"t":"hello"}` — device answers
     `{"t":"nonce","n":<32 random bytes hex>}` (valid 30 s, single use).
  2. Control room → device: `{"t":"adopt","n":<nonce>,"cfg":<sebastian.config.v1 JSON>,"mac":<hmac>}`
     with `mac = HMAC-SHA256(secret, nonce || canonical(cfg))`.
  3. Device verifies with §5 rules, stores, answers `{"t":"ok"}` or
     `{"t":"err","why":"auth|json|wifi"}`, restarts.
- `cfg` carries the new `livekit.tokenServerUrl`, `telemetry.*`, the
  organization secret (if the unit had none) and the **per-device secret** the
  control room just generated (block 1). WiFi is optional and only when the
  operator asks (see block 4's rollback rule).
- Dashboard: *Adopt* on a discovered unit fills the IP from mDNS; *Adopt by IP*
  asks for it. Same server endpoint `POST /v1/admin/lan-devices/{id}/adopt`
  with `{ip}`; if the datagram reaches the unit, it provisions.
- "Forget device" in the control room sends an `adopt` with an empty control
  room and empty secrets: the unit returns to factory (unadopted, keeps WiFi).

### Block 4 — desired configuration from the control room

Generalizes the existing desired-profile poll (`control.zig`, every 30 s).

- Poll gains `?cfg=<hash>`; the response, besides the desired profile, can say
  `config: <hash>`; when it differs the device does
  `GET /v1/devices/{id}/config`, stores it through `provisioning.c`, restarts.
  Reboot is deliberate, as for profiles: the boot path is the one always tested.
- Governable today without new NVS keys: mode (half/full duplex), fixed beam and
  azimuth, token server, syslog, profile. Session timing
  (`silenceTimeoutMs`, `voiceLevel`) moves to NVS in this block. `micChannel`
  stays compile-time (a reflash): it selects the I2S slot at comptime in
  `xvf_pcm.zig`.
- **WiFi changes only with rollback**: if no IP within 2 min on the new
  network, restore the previous credentials and report `err=wifi-rollback`.
- The poll reports the running config (or its hash) so the dashboard shows
  *running* vs *desired* and "applying…" until they match — same UX as the
  profile today.

## 4. Data model changes (server)

- `devices`: `device_secret_digest` (bytea, reuse `credential_digest`),
  `org_adopted_at`, `desired_config jsonb`, `reported_config_hash`,
  `reported_config_at`, `last_seen_lan_at` (from discovery, in memory → optional column).
- Control room identity: `SEBASTIAN_CONTROL_ROOM_NAME` (env), defaulting to the
  API origin; announced in the pre-fill and used by the dashboard.
- Organization secret: `SEBASTIAN_ORG_SECRET` (env, from Infisical in prod).

## 5. Secrets and who may adopt

Two secrets with different jobs:

| Secret | Scope | Set when | Used for |
|--------|-------|----------|----------|
| **Organization secret** | all control rooms of the organization | first provisioning (USB / installer) or first adoption of a factory unit | authorizing adoption |
| **Device secret** | one speaker | generated by the control room at adoption; shown **once** in the dashboard (copy/QR) | `/v1/sessions` auth, and handing a single unit to a third party |

Rules the device applies to an `adopt` message:

1. Unit without organization secret (factory): accept; the message sets it.
   Physical consent (press MUTE within 30 s, ring blinks) is required here so a
   stranger's factory unit on the same WiFi cannot be grabbed silently.
2. Unit with organization secret: accept if the HMAC verifies with **either**
   the organization secret **or** the current device secret. No button, even
   if the unit is bound to another healthy control room: knowing the secret is
   the entitlement (decided 2026-09-18).
3. Anything else: `err auth`, and the attempt is announced in the next mDNS
   TXT (`err=adopt-denied`) so the legitimate control room sees it.
4. The nonce is single-use and expires in 30 s: no replay.

Physical access always wins (USB re-provisioning, or MUTE held at boot for a
factory reset — to be confirmed in block 3): whoever holds the unit owns it.

## 6. Endpoints (delta on `server/api/openapi.yaml`)

| Method | Path | Block |
|--------|------|-------|
| `GET` | `/v1/admin/control-room` (name, API origin, syslog, org secret*) | 0 |
| `POST` | `/v1/sessions` — already exists; the firmware starts using it | 1 |
| `GET` | `/v1/admin/devices` — the fleet view (inventory ⋈ LAN); no separate lan-devices endpoint | 2 |
| `GET`/`PATCH` | `/v1/admin/devices/{id}` — detail with sessions / rename | 2 |
| `POST` | `/v1/admin/devices/{id}/adopt` `{ip?, deviceSecret?, config?}` → 202 job; `GET /v1/admin/adoptions/{jobId}` | 3 |
| `POST` | `/v1/admin/devices/{id}/forget` → 202 job | 3 |
| `POST` | `/v1/admin/devices/{id}/secret` — regenerate, shown once | 3 |
| `GET` | `/v1/devices/{id}/desired-profile?current=&cfg=` — extended | 4 |
| `GET` | `/v1/devices/{id}/config` (device-facing, device secret) | 4 |
| `GET`/`POST` | `/v1/devices/{id}/enroll` (device-facing: nonce, then HMAC proof of the organization secret → device secret) | 1 |
| `PUT`/`DELETE` | `/v1/admin/devices/{id}/desired-config` | 4 |

\* org secret only to an authenticated dashboard session, never to the browser
in the pre-fill JavaScript bundle.

## 7. Constraints that shape the implementation

- **Internal RAM is the scarce resource, not memory.** With a session open the
  unit has ~37 KB of internal SRAM free (largest block 27 KB); PSRAM has
  megabytes. `sdkconfig` already sends allocations ≥ 16 KB to PSRAM and allows
  task stacks there. Everything new (mDNS, the UDP listener, JSON buffers) is
  allocated with `MALLOC_CAP_SPIRAM` explicitly; the internal cost is the
  socket itself. No HTTP server on the device: not for RAM but for surface and
  maintenance — the adoption message fits one signed datagram.
- **Every config change reboots**, on purpose.
- **USB window**: unchanged (5 s at boot, 120 s after a `config.get`).
- **Subnets**: discovery is best-effort; adoption by IP is the guarantee.

## 8. Order and size

| Block | Firmware | Server | Dashboard |
|-------|----------|--------|-----------|
| 0 installer in dashboard | — | ½ d | 1 d (build + pre-fill + HTTP note) |
| 1 device secret + `/v1/sessions` | ½ d | ½ d | — |
| 2 mDNS + LAN view | 1 d | 1 d | ½ d |
| 3 adoption (mDNS / by IP / forget) | 1 d | ½ d | ½ d |
| 4 desired config | 1 d | ½ d | ½ d |

Block 0 can start today; 1 → 2 → 3 → 4 in that order. Each block is verified
on the two units on Pizarro before the next.

## 9. Decisions taken in the conversation (so they are not re-litigated)

- Both installers stay: GitHub Pages (public, blank) and the dashboard one
  (pre-filled). One app, two builds.
- Adoption over HTTP-only control rooms: document the Chrome flag; do not
  build around it.
- Adoption entitlement = organization secret **or** device secret; no button
  for units that already carry the organization secret.
- Orphan = 3 consecutive failed polls (90 s).
- Control room name = API origin until a name is configured.
- Discovery's subnet limit is accepted; adoption by IP covers it.
