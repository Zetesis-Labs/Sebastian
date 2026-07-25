# USB microphone mode (`CONFIG_SEBASTIAN_USB_MIC`)

Alternate firmware that turns Sebastian into a **plug-and-play USB microphone**:
the XVF3800 comms beam (AEC + adaptive beamforming + NS + de-reverb + limiter)
exposed to the host as a standard **USB Audio Class** device — 48 kHz mono
16-bit. No WiFi, no wake word, no LiveKit; plug it into any computer and it
shows up as **"Sebastian Mic" (Zetesis)**.

Validated on macOS (2026-07-25): enumerates over the XIAO's USB-C, CoreAudio
registers a 1-channel 48 kHz USB input, clean capture with no micro-cuts.

## How it works

- `main/usb_app.zig` is an **alternate Zig app root**, selected by
  `CONFIG_SEBASTIAN_USB_MIC` in `cmake/zig.cmake` (`-Droot`). Everything else —
  board bring-up, XVF DFU/unmute, AEC/gain config, mic capture — is the same
  code the LiveKit agent firmware runs.
- USB side is [`espressif/usb_device_uac`](https://components.espressif.com/components/espressif/usb_device_uac)
  (TinyUSB UAC2 on the S3's USB-OTG PHY). Its mic task calls our `input_cb`
  every 10 ms; the callback drives **`mic_src`'s vtable directly** (no
  `esp_capture` pipeline), so slot selection, gain shift, soft limiter, the
  first-read clock resync, the channel self-heal and the capture-health
  counters are exactly the production mic path.
- The capture stays **consumer-paced**: the callback blocks on I2S for exactly
  the requested samples, and the UAC task's `vTaskDelayUntil` absorbs the wait.
  Residual host-vs-XVF clock drift is soaked by the UAC FIFO (worst case an
  occasional dropped or short packet over hours — inaudible in practice).
- **Beam stays ADAPTIVE** (`cfg.fixed_beam = false` before `applyConfig`):
  with no speaker there is no echo to cancel, so nothing needs the AEC to
  converge and the beam is free to track the talker across the room (path B —
  the comms channel's processing does not depend on a fixed beam).
- **Physical mute button** works: `xvf_ui`'s task reads the XVF mute GPIO and
  gates the capture in software (the ring goes dark). **Host mute/volume**
  (macOS input slider) arrive via UAC feature-unit callbacks and are applied
  after capture, so I2S keeps draining while muted.
- The LED ring shows the DoA beam while you talk (ACTIVE state), same as in a
  session.

## Build & flash

On this branch `sdkconfig.defaults` already enables the mode:

```bash
cd firmware
. ~/esp/esp-idf/export.sh
idf.py build                      # sdkconfig regenerated from defaults → USB mic
idf.py -p /dev/cu.usbmodemXXX flash
```

To build the LiveKit agent firmware from this branch, set
`CONFIG_SEBASTIAN_USB_MIC=n` (menuconfig) and rebuild.

## Reflashing a unit that runs the USB-mic firmware

TinyUSB owns the S3's only USB PHY, so the **USB-Serial-JTAG console/flasher
does not exist** while this firmware runs (that's also why there are no logs
over USB — UART0 pins are repurposed as I2S data on this board).

To flash anything (back to the agent firmware, or a new USB-mic build):

1. Hold the XIAO's **BOOT (B)** button.
2. Tap **RESET** (or replug USB) while holding BOOT, then release.
3. The ROM download mode enumerates as a USB-Serial-JTAG port again →
   `idf.py -p /dev/cu.usbmodemXXX flash`.
4. Press RESET once after flashing (the ROM loader does not auto-run).

## Known limits / future

- Content is voice-band (~8.5 kHz rolloff): inherent to the XVF comms path —
  it is a speech mic, not a music interface.
- Speaker interface is **disabled by design** (`CONFIG_UAC_SPEAKER_CHANNEL_NUM=0`).
  Enabling it and wiring `output_cb` into the render path would make Sebastian a
  full USB **speakerphone** with hardware AEC (the far-end reference already
  reaches the XVF over I2S TX) — natural next step, needs the fixed-beam vs
  tracking trade-off revisited.
- USB descriptor strings come from `sdkconfig.defaults`
  (`CONFIG_UAC_TUSB_MANUFACTURER/PRODUCT`).
