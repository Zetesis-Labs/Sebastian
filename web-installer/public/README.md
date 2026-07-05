# Sebastian Web Installer

Static GitHub Pages installer for ReSpeaker XVF3800 + XIAO ESP32-S3 using ESP Web Tools.
It depends on Web Serial, so it needs HTTPS/localhost and a browser that exposes
`navigator.serial` (Chromium desktop browsers, or Firefox 151+ desktop). Safari/iOS
are not supported.

Expected URL once Pages serves `docs/`:

```text
https://zetesis-labs.github.io/Sebastian/installer/
```

## Firmware packaging

ESP Web Tools expects ESP-IDF v4+ ESP32 images to be merged into one binary at offset `0`.
Generate it from an existing firmware build:

```bash
make fw-build
tools/prepare_web_installer.sh
```

That writes:

- `docs/installer/firmware/sebastian-esp32s3-merged.bin`
- `docs/installer/manifest.json`

The generated `.bin` is ignored by git because the current firmware embeds local WiFi and
token-server configuration. Only publish a factory image that is intentionally safe to share.

## Parameterized installs

The page can install from a custom manifest:

```text
https://zetesis-labs.github.io/Sebastian/installer/?manifest=https://example.com/manifest.json
```

Or from a merged binary URL by generating a temporary manifest in the browser:

```text
https://zetesis-labs.github.io/Sebastian/installer/?bin=https://example.com/sebastian.bin&version=v0.1.0
```

External firmware hosts need CORS headers that allow the Pages origin.

## Install troubleshooting

`Failed to initialize` means the browser opened the serial port but esptool.js
could not sync with the ESP32-S3 bootloader. Close any open serial dialog/monitor,
then retry while holding BOOT when clicking "Connect and install"; release BOOT
once initialization starts. If Chrome still owns the port after a failed attempt,
close the install dialog or reload the page before trying again.

## Runtime provisioning

The page also prepares a `sebastian.config.v1` JSON payload for device provisioning.
It can be downloaded, copied, or sent over Web Serial. The firmware receiver is the
next piece: it should parse that line, validate it, persist it in NVS, and reboot.
See [PROVISIONING.md](PROVISIONING.md).

The payload contract is versioned in
[`sebastian-config.schema.json`](sebastian-config.schema.json). The installer
validates imported, downloaded, copied, and serial-sent configs against that JSON
Schema before accepting them.

Export the current repo-local configuration and load it in the page:

```bash
tools/export_current_config.py --out docs/installer/sebastian-config.local.json
```

Then use the page's "Import" control and select that generated file. The
`.local.json` file is ignored by git because it contains the real WiFi password.
For hosted configs without secrets, the page also accepts:

```text
https://zetesis-labs.github.io/Sebastian/installer/?config=https://example.com/sebastian-config.json
```

## Current product gap

This web installer is ready as a delivery surface, but a public WLED/ESPHome-style flow
still needs runtime provisioning. Today the firmware gets WiFi credentials from
`firmware/sdkconfig` and the token-server URL from `firmware/main/secrets.zig`, both at
build time. A general installer should move those values to NVS/serial provisioning, or
implement Improv Serial, before publishing one binary for all users.
