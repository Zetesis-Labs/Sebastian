# Provisioning profiles

One binary, several personalities. A **profile** is a named capability bundle
stored in NVS; the active profile decides what Sebastian IS after boot:

- `mode`: `agent` (LiveKit voice assistant) or `usb_mic` (plug-and-play USB
  microphone — see `USB_MIC.md`)
- optional overrides of the audio config: `fullDuplex`, `fixedBeam`,
  `beamAzimuthDeg`
- reserved for the next phases: `wifi`, `telemetry` (parsed and stored today,
  honored when WiFi/telemetry land in usb_mic mode)

Fields a profile does not set are inherited from the legacy NVS keys /
compiled defaults (`config.zig`), so pre-profile units keep their behaviour.

With no provisioned profiles the firmware ships two built-ins:

| # | Name | Ring color | What |
|---|------|-----------|------|
| 0 | `agente` | azul | LiveKit agent, config inherited |
| 1 | `micro-usb` | ámbar | USB mic, adaptive beam |

## Switching profiles

**Boot selector (on-device)** — when the ring lights up dim white shortly
after plugging in (~2-3 s, once the XVF is up), double-tap the mute button.
The ring splits into one colored arc per profile: each further press advances
the highlighted arc, leaving the button alone for 5 s confirms. The choice
persists and boot continues straight into the chosen profile. A unit that
merely boots muted never triggers the selector (zero button *changes*).

**Serial (installer / scripts)** — one line over USB-Serial-JTAG. In agent
mode the port is always live; in usb_mic mode there is a **5 s provisioning
window** at every boot (before TinyUSB claims the USB PHY) where the same
commands work — after that the device is audio-only until the next plug:

```
sebastian.profile.set {"name":"micro-usb"}
```

Replies `sebastian.profile.ok` and reboots into the profile (`err` if the name
matches no profile). The full installer flow (`sebastian.config.v1 {...}`) also
accepts a `profiles` array plus `activeProfile`:

```json
{
  "schema": "sebastian.config.v1",
  "wifi": { "ssid": "...", "password": "..." },
  "profiles": [
    { "name": "salon-full", "mode": "agent", "fullDuplex": true, "fixedBeam": true },
    { "name": "salon-half", "mode": "agent", "fullDuplex": false, "fixedBeam": false },
    { "name": "micro-usb", "mode": "usb_mic" }
  ],
  "activeProfile": "salon-full"
}
```

Limits: max 6 profiles, names up to 23 chars; the whole provisioning line must
fit the 1 KB serial buffer.

## Safe boot (anti-brick)

A boot counter increments at every boot start and clears once the selected
mode is up. "Up" means **administrable**, not connected: for the agent that is
provisioning listening + audio pipeline built, deliberately before the WiFi
connect (an outage is not a broken boot); for usb_mic it is the UAC device
initialized. Three straight failed boots ⇒ the firmware runs the first `agent`
profile for that boot, **in memory only** — the provisioned choice is never
rewritten, so a transient failure can't hijack the profile (field-validated).
While failures persist the fallback re-fires each boot; one healthy boot
clears the counter and the provisioned profile returns.

## Storage

NVS namespace `sebastian` (same as provisioning): `profiles` (raw JSON array),
`active_prof` (name), `boot_fails` (i32). Parsing lives in
`main/profiles.c`; `main/profile.zig` overlays the active profile onto
`config.zig` at boot, and the selector state machine is host-tested pure logic
in `main/core/selector_core.zig`.
