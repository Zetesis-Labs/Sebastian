# Hardware — Seeed ReSpeaker XVF3800 + XIAO ESP32-S3

Placa de array de 4 micrófonos con procesador de voz dedicado, pensada para
captura de voz a 5 m y 360°.

## Componentes

| Pieza | Qué hace |
|---|---|
| **XIAO ESP32-S3** | MCU host (WiFi/BT, corre el firmware). ESP32-S3R8: 8 MB flash, 8 MB PSRAM octal. |
| **XMOS XVF3800** | Procesador de voz: AEC, beamforming, supresión de ruido, AGC, VAD, DoA. Entrega audio ya procesado. |
| **TLV320AIC3104** | Codec analógico (DAC/amp) para la salida de altavoz / AUX. |
| **PCAL6416A** | Expansor de IO (botones, LEDs). |
| Array 4 mics | Captura far-field 360°. |
| Amp 5 W + JST | Salida de altavoz. También jack AUX 3.5 mm. |

## Topología de audio

```
4 mics ──> XVF3800 (AEC/beamforming/NS) ──I2S(DIN)──> ESP32-S3
                                                          │
ESP32-S3 ──I2S(DOUT)──> TLV320AIC3104 ──> altavoz 5 W / AUX
```

El XVF3800 (con firmware "inthost") es el **I2S MASTER**: genera el reloj a
**48 kHz**, 32-bit por slot, estéreo. El **ESP32-S3 es esclavo**, y usa **dos
puertos I2S separados** — RX (micro, I2S_NUM_1) y TX (altavoz, I2S_NUM_0) —
compartiendo BCLK/WS. Un único canal dúplex compartido corrompía el DMA. MCLK no
se usa. El AIC3104 también es esclavo del mismo reloj. Detalle en
[ARCHITECTURE.md](ARCHITECTURE.md).

## Pinout

| Señal | GPIO |
|---|---|
| I2S BCLK (entrada, del XVF) | 8 |
| I2S WS / LRCLK (entrada, del XVF) | 7 |
| I2S DOUT (→ AIC3104, altavoz) | 44 |
| I2S DIN (← XVF3800, micro) | 43 |
| I2S MCLK | — (sin usar) |
| I2C SDA | 5 |
| I2C SCL | 6 |

Formato I2S: **48 kHz**, 32-bit por slot, estéreo. Slot **izquierdo** = voz
procesada con supresión de ruido (comunicación); slot **derecho** = haz ASR crudo
(sin NS) — este proyecto usa el **derecho** (ver [ARCHITECTURE.md](ARCHITECTURE.md)
y [XVF3800.md](XVF3800.md)).

## Direcciones I2C

| Dispositivo | Dirección 7-bit |
|---|---|
| TLV320AIC3104 | `0x18` |
| PCAL6416A (IO expander) | `0x21` |
| XVF3800 | `0x2C` |

El primer boot hace un escaneo I2C y debería listar estas tres direcciones. Es la
mejor señal de que el cableado/alimentación están bien.

## Modos de firmware del XVF3800

El XVF necesita el firmware **"inthost" (I2S master, 1.0.7)** para clockear el bus
y streamear el micro. El firmware que trae por defecto ("non-master"/i2s_dfu) NO
streamea audio. **Nuestro propio firmware Zig re-flashea el XVF por DFU sobre I2C
en el primer arranque** (`xvf_dfu.zig`, sin ESPHome ni herramientas externas) y lo
des-mutea. Todo el detalle del protocolo y las familias de firmware en
[XVF3800.md](XVF3800.md).

## Referencias

- [reSpeaker XVF3800 + XIAO ESP32-S3 — getting started](https://wiki.seeedstudio.com/respeaker_xvf3800_xiao_getting_started/)
- [Seeed esp32-client (Agora) — código I2S/codec de referencia](https://github.com/Seeed-Projects/seeed-respeaker-agora-tenframework)
- [LiveKit Client SDK for ESP32](https://github.com/livekit/client-sdk-esp32)
