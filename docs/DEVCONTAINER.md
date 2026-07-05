# Devcontainer — the entire environment in a container (minus USB)

The entire development environment runs in the devcontainer: **firmware build
(ESP-IDF v5.4 + Zig-Xtensa fork), LiveKit agent, token server, and the LGTM
observability stack**. The only physical boundary is the USB: **Docker on macOS
cannot pass serial devices to the container**, so flashing and the telemetry
bridge are the only two commands executed on the host.

```
┌─ devcontainer (dev) ─────────────────┐   ┌─ compose (lgtm) ──────────────┐
│ idf.py build  (ESP-IDF + Zig linux)  │   │ grafana/otel-lgtm             │
│ make agent    (livekit-agents + HA)  │──▶│ :3000 Grafana  :4318 OTLP     │
│ make token    (:8787 → LAN)          │   └───────▲───────────────────────┘
└──────────────────┬───────────────────┘           │ OTLP
                   │ artifacts in firmware/build/  │
┌─ host macOS ─────▼───────────────────────────────┴───────────────────────┐
│ make flash   (uvx esptool @flash_args — only touches /dev/cu.usbmodem*  )│
│ make bridge  (serial → OTLP localhost:4318)                              │
└──────────────────────────────────────────────────────────────────────────┘
```

## Startup

1. VS Code / Cursor: "Reopen in Container" (or `devcontainer up`). The
   `postCreate` synchronizes the agent's venv; `postStart` provisions the
   Grafana dashboard.
2. Inside the container: `make fw-build`, `make token`, `make agent`.
3. On the host: `make flash` (after a build) and `make bridge`.

The token server is published on `0.0.0.0:8787` on the Mac, so the device
reaches it on the Mac's LAN IP just like before (`secrets.zig` doesn't change).

## Migration from the host environment (one time only)

- `docker rm -f sebastian-lgtm` — removes the old LGTM started manually with
  `docker run`. Its fixed name blocked the devcontainer compose (name conflict
  between projects). The compose brings up its own, `<project>-lgtm-1`.
- `rm -rf firmware/build` — the host (macOS) CMake cache is not valid in
  Linux; the first `make fw-build` in the container regenerates it and downloads
  the pinned `linux-musl` Zig in `cmake/zig.cmake` (now cross-platform).
- **The agent's venv lives in a named volume** (`agent_venv`), not in the
  mount: a uv venv's `python` is a symlink to a local interpreter in the
  container, so a venv in the shared mount points to another
  container/host and appears incomplete (Pylance doesn't resolve `livekit`…).
  Upon creating/rebuilding the container, `postCreate` (`uv sync`) populates it;
  the volume keeps it intact between restarts and isolated from the host's venv.
- Kill host agent/token (`pkill -f agent.py; pkill -f token_server`) —
  they now live in the container. The bridge stays on the host.

### Grafana MCP after migration

The fixed `container_name` was removed (it collided between projects). The LGTM
container is now named `sebastian_devcontainer-lgtm-1` (VS Code) or
`devcontainer-lgtm-1` (manual compose). Two ways to point the MCP:

- **Via shared network**: `--network container:sebastian_devcontainer-lgtm-1`
  and `GRAFANA_URL=http://localhost:3000` (like before, new name).
- **Via the compose network** (more stable): `--network sebastian_devcontainer_default`
  and `GRAFANA_URL=http://lgtm:3000` (resolves by service name, immune to
  renames). Recommended.

## Why like this

- **Firmware in container**: `cmake/zig.cmake` already pinned hashes for
  `aarch64/x86_64-linux-musl`, and the `espressif/idf:release-v5.4` image brings the
  complete Xtensa toolchain. No build ever depends on the Mac again.
- **Flashing via `flash_args`**: the build artifacts (relative paths) are
  flashed from the host with raw esptool (`uvx --from esptool esptool.py`),
  without installing ESP-IDF on the Mac.
- **`container_name: sebastian-lgtm`**: stable so the host bridge
  (localhost:4318) and the Grafana MCP do not change.
- The agent inside the container exports OTel to `http://lgtm:4318`
  (`OTEL_EXPORTER_OTLP_ENDPOINT` variable in the compose).

## USB on macOS: two options

`devices:` in compose only works on Linux hosts (it maps kernel nodes).
On macOS, Docker runs in a Linux VM and `/dev/cu.usbmodem*` is a Darwin
device: it doesn't exist inside the VM, there's nothing to map. The two ways:

- **A (default): native flashing on the host.** `make flash` uses
  `uvx esptool` against `firmware/build/flash_args` (relative paths — the
  container artifacts are flashed as is). `make bridge` on the host.
- **B (everything inside): serial shared via TCP.** On the host,
  `make serial-share` starts `esp_rfc2217_server` (comes with esptool, with
  reset lines included). Inside the container:
  `SEBASTIAN_HOST_IP=<mac-lan-ip> make fw-flash`, and the bridge with
  `SEBASTIAN_SERIAL_URL=rfc2217://<ip>:4000 make bridge`. Note: use the Mac's
  LAN IP, not `host.docker.internal` (no route in this Docker). The RFC2217
  server allows ONE client at a time: stop the bridge to flash, just like on
  the host.

## Platform portability

| | Container (build+agent+LGTM) | Serial (flash/bridge) |
|---|---|---|
| **macOS** | ✓ | Host (option A) or RFC2217 (option B) |
| **Linux** | ✓ | **Direct to container**: uncomment `devices:` in compose and everything (flash included) runs inside — the best case |
| **Windows** | ✓ (Docker Desktop + WSL2) | `usbipd-win attach --wsl` puts the USB in WSL2 (`/dev/ttyACM0`) → `devices:` like in Linux; or RFC2217 from PowerShell |

Port autodetection (bridge and `flash.sh`) covers `cu.usbmodem*` (macOS)
and `ttyACM*`/`ttyUSB*` (Linux/WSL2). The only platform-tied component
is the wake word training (Apple MPS).

## What DOES NOT go in the container

- **Wake word training** — out of scope on purpose: the trained
  model (`firmware/main/sebastian.tflite`) is committed in the repo and the
  environment works with it out of the box. Whoever wants another word trains it
  with the `wakeword/` scripts (host flow with GPU/MPS; the ~40GB trainer
  is gitignored).
