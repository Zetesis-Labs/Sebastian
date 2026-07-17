# Sebastian — Build, Flash, and Run Guide

Practical operator guide for the **Sebastian** project: a voice speaker based
on **ReSpeaker XVF3800 + XIAO ESP32-S3** that joins a LiveKit room, plus a
**Python agent** that converses with it using OpenAI Realtime.

The system has three runtime parts:

- **`firmware/`** — ESP32-S3 firmware (ESP-IDF v5.4 + Zig), joins the LiveKit room.
- **`agent/`** — Python agent (LiveKit Agents + OpenAI Realtime) that joins the same room.
- **`server/`** — Go/OpenAPI control plane that authenticates devices, dispatches
  the agent and issues short-lived LiveKit credentials.

Both meet in the **same LiveKit room** and converse there.

---

## 1. Prerequisites

- **ESP-IDF v5.4** installed in `~/esp/esp-idf`. It is activated with:
  ```bash
  source ~/esp/esp-idf/export.sh
  ```
- **Espressif's Zig fork** (`0.16-xtensa`): it is automatically downloaded by the
  build itself (`cmake/zig.cmake`). **Nothing needs to be installed manually.**
- **The board** (ReSpeaker XVF3800 + XIAO ESP32-S3) enumerates on macOS as
  `/dev/cu.usbmodem101`.
- **Python Agent**: requires [`uv`](https://github.com/astral-sh/uv) (astral uv),
  an **OpenAI key**, and **LiveKit credentials**.

---

## 2. Secrets (all gitignored — NEVER commit)

None of these files are in git. They must be created locally.

### `firmware/main/secrets.zig`

A single const: the compatibility route of **Sebastian Server** on the device's LAN.
The firmware no longer carries a static JWT — it requests a fresh one (with the
embedded agent dispatch) in each session. Format:

```zig
pub const token_server_url = "http://<lan-ip-of-server>:8787/token";
```

Inside the devcontainer, initialize and start it with:

```bash
make server-migrate
make server-run
```

In a second devcontainer terminal, run the durable event publisher:

```bash
make server-outbox
```

The devcontainer provides NATS JetStream at `nats://nats:4222`; API requests
remain available if NATS is temporarily down because events first commit to the
PostgreSQL outbox.

Start the administration panel in another terminal:

```bash
make dashboard-dev
```

Open `http://localhost:3001`. Its TanStack Start server calls the Go API using
the devcontainer's shared administration secret; that value is never sent to
the browser.

`LIVEKIT_URL` is no longer placed in the firmware: Sebastian Server returns it
with the short-lived JWT. The old Python token server remains only during the
migration and must not run on the same port.

### `firmware/sdkconfig`

Contains the **actual** WiFi SSID/password. The values in
`sdkconfig.defaults` are placeholders (`"changeme"`). You need to set:

```
CONFIG_LK_EXAMPLE_WIFI_SSID="<your-ssid>"
CONFIG_LK_EXAMPLE_WIFI_PASSWORD="<your-password>"
```

> `sdkconfig` is generated during the first `idf.py build` from `sdkconfig.defaults`.
> Edit the actual credentials there; it is gitignored.

### `agent/.env`

Agent variables (see `agent/.env.example`):

```
OPENAI_API_KEY=<...>
LIVEKIT_URL=wss://<your-project>.livekit.cloud
LIVEKIT_API_KEY=<...>
LIVEKIT_API_SECRET=<...>
```

---

## 3. Build the firmware

```bash
source ~/esp/esp-idf/export.sh
cd ~/Developer/Sebastian/firmware
idf.py build
```

> Lines like `unsupported hash type blake2` are harmless warnings from
> pyenv; they can be ignored.

The board is an **ESP32-S3R8** (8 MB flash, 8 MB octal PSRAM). The target and the
flash/PSRAM configuration are already set in `sdkconfig.defaults`.

---

## 4. Flashing

### Reliable method (recommended)

The XIAO ESP32-S3 uses **native USB-JTAG**; esptool's `default_reset` activates it.

```bash
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset \
  write_flash --flash_mode dio --flash_size 8MB --flash_freq 80m \
  0x0 build/bootloader/bootloader.bin 0x8000 build/partition_table/partition-table.bin 0x10000 build/sebastian.bin
```

### Alternative

```bash
idf.py -p /dev/cu.usbmodem101 flash
```

### Note — XVF3800 DFU on first boot

On the **first boot**, the firmware updates (DFU) the XVF3800 to the
**I2S-master** firmware via I2C. It takes **~30-60 s** (see `docs/XVF3800.md`). This happens **only
once**: on subsequent boots the XVF is already at 1.0.7 and skips it.

---

## 5. Flashing recovery (important)

If the board is in a crash loop or hung, esptool fails with
`Failed to connect: No serial data received`. Fixes, in order:

1. **Retry** the esptool command several times — the USB-JTAG reset
   sometimes needs 2-3 attempts.
2. **Physical power cycle**: unplug the USB, wait ~3 s, plug it back
   in, and flash.
3. **Manual download mode**: press and hold the **BOOT** button (recessed) on the
   BOARD while plugging in the USB.

> The board **does not get bricked** by this: it recovers.

---

## 6. Run the agent

```bash
cd ~/Developer/Sebastian/agent
uv sync            # only the first time
uv run agent.py dev
```

> `uv run agent.py start` is production worker mode; `dev` is local with hot-reload.

### CRITICAL: only one agent at a time

Run **exactly ONE** agent process. Multiple agents cause the
assistant to **"talk to itself"** or behave erratically.

To kill rogue processes and start a clean one:

```bash
pkill -9 -f "agent.py"
# ...then start just one
uv run agent.py dev
```

---

## 7. Dispatch: get the agent into the device's room

The agent **only auto-joins a NEW (fresh) LiveKit room**. To put it
in the room together with the device: **delete the room** and **reset the device**
so it rejoins a fresh room.

```bash
# Requires the env vars LIVEKIT_URL / LIVEKIT_API_KEY / LIVEKIT_API_SECRET
lk room delete sebastian

# Reset the board (rejoins fresh)
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset chip_id
```

Verify that the room has **2 participants** (device + agent):

```bash
lk room list
```

---

## 8. Audio verification

The agent records the incoming microphone track to `/tmp/sebastian_rx.wav`
(16 kHz mono). To check **what the agent actually hears**:

```bash
afplay /tmp/sebastian_rx.wav      # listen
```

For quantitative analysis, a short Python script with the `wave`
and `array` modules that calculates RMS per 0.5 s window, peak, clipping count, and a
rough spectrum:

> Note: `audioop` was **removed in Python 3.13**, which is why `array` is used
> instead.

```python
import wave, array, math

with wave.open("/tmp/sebastian_rx.wav", "rb") as w:
    rate = w.getframerate()
    frames = w.readframes(w.getnframes())

samples = array.array("h")
samples.frombytes(frames)

window = int(rate * 0.5)  # 0.5 s windows
peak = max(abs(s) for s in samples)
clipping = sum(1 for s in samples if abs(s) >= 32700)

print(f"rate={rate} n={len(samples)} peak={peak} clipping={clipping}")
for i in range(0, len(samples), window):
    chunk = samples[i:i + window]
    if not chunk:
        continue
    rms = math.sqrt(sum(s * s for s in chunk) / len(chunk))
    print(f"t={i / rate:5.1f}s  rms={rms:8.1f}")
```

> **Key takeaway**: audio **LEVELS** are not the same as
> **INTELLIGIBILITY**. You have to **listen** and/or look at the **spectrum** — a healthy
> RMS does not guarantee that anything can be understood.

---

## 9. Serial monitor

```bash
cat /dev/cu.usbmodem101
# or else
idf.py -p /dev/cu.usbmodem101 monitor
```

On boot, the init log shows:

- **I2C scan**: expects `0x18` (AIC3104) and `0x2C` (XVF3800).
- The **DFU/version of the XVF**.
- `mic source started`.
- The room state.

---

## 10. Full workflow (summary)

```bash
# 1. Build
source ~/esp/esp-idf/export.sh
cd ~/Developer/Sebastian/firmware
idf.py build

# 2. Flash
idf.py -p /dev/cu.usbmodem101 flash

# 3. Start ONE agent
cd ~/Developer/Sebastian/agent
pkill -9 -f "agent.py"
uv run agent.py dev

# 4. Dispatch to a fresh room
lk room delete sebastian
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset chip_id

# 5. Verify
lk room list                 # -> 2 participants
afplay /tmp/sebastian_rx.wav # -> listen to what the agent hears
```
