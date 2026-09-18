# XVF3800 firmware image embedded in the ESP32 firmware

| File | Size | SHA-256 |
|------|------|---------|
| `xvf_master_1.0.7.bin` | 888 832 bytes | `d1132b7e779818923072396315304a5dd2324aa56969b4111b30678f968a7c6a` |

Stock XMOS image `application_xvf3800_inthost-lr48-sqr-i2c-v1.0.7` (I2S master,
LR clock 48 kHz, I2C control) from XMOS's XVF3800 release package. Not modified
by this project. It is not any of the binaries in Seeed's
`respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY` repo (hash-checked 2026-09-18).

`xvf_dfu.zig` embeds it with `@embedFile` and uploads it over I2C when the XVF
reports another version. The same file is what `dfu-util -a 1 -D` sends over
USB in safe mode for boards that ship with the USB firmware
(`docs/XVF3800.md`, "Boards that ship with the USB firmware").

Verify after any change: `shasum -a 256 xvf_master_1.0.7.bin`.
