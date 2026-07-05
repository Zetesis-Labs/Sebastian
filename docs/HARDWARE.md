# Hardware — Seeed ReSpeaker XVF3800 + XIAO ESP32-S3

4-microphone array board with dedicated voice processor, designed for 5m and 360° voice capture.

## Components

| Part | What it does |
|---|---|
| **XIAO ESP32-S3** | Host MCU (WiFi/BT, runs the firmware). ESP32-S3R8: 8 MB flash, 8 MB octal PSRAM. |
| **XMOS XVF3800** | Voice processor: AEC, beamforming, noise suppression, AGC, VAD, DoA. Delivers already processed audio. |
| **TLV320AIC3104** | Analog codec (DAC/amp) for speaker / AUX output. |
| **PCAL6416A** | IO expander (buttons, LEDs). |
| 4-mic array | 360° far-field capture. |
| 5 W Amp + JST | Speaker output. Also 3.5 mm AUX jack. |

## Audio topology

```
4 mics ──> XVF3800 (AEC/beamforming/NS) ──I2S(DIN)──> ESP32-S3
                                                          │
ESP32-S3 ──I2S(DOUT)──> TLV320AIC3104 ──> 5 W speaker / AUX
```

The XVF3800 (with "inthost" firmware) is the **I2S MASTER**: it generates the clock at **48 kHz**, 32-bit per slot, stereo. The **ESP32-S3 is a slave**, and uses **two separate I2S ports** — RX (mic, I2S_NUM_1) and TX (speaker, I2S_NUM_0) — sharing BCLK/WS. A single shared duplex channel was corrupting the DMA. MCLK is not used. The AIC3104 is also a slave of the same clock. Details in [ARCHITECTURE.md](ARCHITECTURE.md).

## Pinout

| Signal | GPIO |
|---|---|
| I2S BCLK (input, from XVF) | 8 |
| I2S WS / LRCLK (input, from XVF) | 7 |
| I2S DOUT (→ AIC3104, speaker) | 44 |
| I2S DIN (← XVF3800, mic) | 43 |
| I2S MCLK | — (unused) |
| I2C SDA | 5 |
| I2C SCL | 6 |

I2S format: **48 kHz**, 32-bit per slot, stereo. **Left** slot = processed voice with noise suppression (communication); **right** slot = raw ASR beam (no NS) — this project uses the **right** one (see [ARCHITECTURE.md](ARCHITECTURE.md) and [XVF3800.md](XVF3800.md)).

## I2C Addresses

| Device | 7-bit Address |
|---|---|
| TLV320AIC3104 | `0x18` |
| PCAL6416A (IO expander) | `0x21` |
| XVF3800 | `0x2C` |

The first boot performs an I2C scan and should list these three addresses. It is the best sign that wiring/power are fine.

## XVF3800 firmware modes

The XVF needs the **"inthost" (I2S master, 1.0.7)** firmware to clock the bus and stream the mic. The default firmware ("non-master"/i2s_dfu) DOES NOT stream audio. **Our own Zig firmware re-flashes the XVF via DFU over I2C on the first boot** (`xvf_dfu.zig`, without ESPHome or external tools) and unmutes it. Full details of the protocol and firmware families in [XVF3800.md](XVF3800.md).

## References

- [reSpeaker XVF3800 + XIAO ESP32-S3 — getting started](https://wiki.seeedstudio.com/respeaker_xvf3800_xiao_getting_started/)
- [Seeed esp32-client (Agora) — reference I2S/codec code](https://github.com/Seeed-Projects/seeed-respeaker-agora-tenframework)
- [LiveKit Client SDK for ESP32](https://github.com/livekit/client-sdk-esp32)
