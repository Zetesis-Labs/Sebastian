# Sebastian — Guía de compilación, flasheo y ejecución

Guía práctica de operador para el proyecto **Sebastian**: un altavoz de voz basado
en **ReSpeaker XVF3800 + XIAO ESP32-S3** que se une a una sala de LiveKit, más un
**agente Python** que conversa con él usando OpenAI Realtime.

El sistema tiene dos mitades:

- **`firmware/`** — firmware del ESP32-S3 (ESP-IDF v5.4 + Zig), se une a la sala LiveKit.
- **`agent/`** — agente Python (LiveKit Agents + OpenAI Realtime) que se une a la misma sala.

Ambos se encuentran en la **misma sala de LiveKit** y ahí conversan.

---

## 1. Requisitos previos

- **ESP-IDF v5.4** instalado en `~/esp/esp-idf`. Se activa con:
  ```bash
  source ~/esp/esp-idf/export.sh
  ```
- **Fork de Zig de Espressif** (`0.16-xtensa`): lo descarga automáticamente el
  propio build (`cmake/zig.cmake`). **No hay que instalar nada a mano.**
- **La placa** (ReSpeaker XVF3800 + XIAO ESP32-S3) enumera en macOS como
  `/dev/cu.usbmodem101`.
- **Agente Python**: necesita [`uv`](https://github.com/astral-sh/uv) (astral uv),
  una **clave de OpenAI** y **credenciales de LiveKit**.

---

## 2. Secretos (todos gitignoreados — NUNCA commitear)

Ninguno de estos ficheros está en git. Hay que crearlos localmente.

### `firmware/main/secrets.zig`

Una sola const: la URL del **token server** (`agent/token_server.py`) en la LAN
del dispositivo. El firmware ya no lleva un JWT estático — pide uno fresco (con el
dispatch del agente embebido) en cada sesión. Forma:

```zig
pub const token_server_url = "http://<ip-lan-del-server>:8787/token";
```

Arranca el token server con `uv run token_server.py` desde `agent/` (usa el mismo
`.env` que el agente). La `server_url` de LiveKit ya no se pone aquí: la devuelve
el token server en la respuesta.

### `firmware/sdkconfig`

Contiene el SSID/contraseña de WiFi **reales**. Los valores de
`sdkconfig.defaults` son marcadores de posición (`"changeme"`). Hay que fijar:

```
CONFIG_LK_EXAMPLE_WIFI_SSID="<tu-ssid>"
CONFIG_LK_EXAMPLE_WIFI_PASSWORD="<tu-password>"
```

> `sdkconfig` se genera en el primer `idf.py build` a partir de `sdkconfig.defaults`.
> Edita ahí las credenciales reales; está gitignoreado.

### `agent/.env`

Variables del agente (ver `agent/.env.example`):

```
OPENAI_API_KEY=<...>
LIVEKIT_URL=wss://<tu-proyecto>.livekit.cloud
LIVEKIT_API_KEY=<...>
LIVEKIT_API_SECRET=<...>
```

---

## 3. Compilar el firmware

```bash
source ~/esp/esp-idf/export.sh
cd ~/Developer/Sebastian/firmware
idf.py build
```

> Las líneas del tipo `unsupported hash type blake2` son avisos inofensivos de
> pyenv; se pueden ignorar.

La placa es un **ESP32-S3R8** (8 MB flash, 8 MB PSRAM octal). El target y la
configuración de flash/PSRAM ya vienen fijados en `sdkconfig.defaults`.

---

## 4. Flashear

### Método fiable (recomendado)

El XIAO ESP32-S3 usa **USB-JTAG nativo**; el `default_reset` de esptool lo activa.

```bash
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset \
  write_flash --flash_mode dio --flash_size 8MB --flash_freq 80m \
  0x0 build/bootloader/bootloader.bin 0x8000 build/partition_table/partition-table.bin 0x10000 build/sebastian.bin
```

### Alternativa

```bash
idf.py -p /dev/cu.usbmodem101 flash
```

### Nota — DFU del XVF3800 en el primer arranque

En el **primer arranque** el firmware actualiza (DFU) el XVF3800 a la firmware
**I2S-master** por I2C. Tarda **~30-60 s** (ver `docs/XVF3800.md`). Ocurre **una
sola vez**: en arranques posteriores el XVF ya está en 1.0.7 y se lo salta.

---

## 5. Recuperación de flasheo (importante)

Si la placa está en bucle de crash o colgada, esptool falla con
`Failed to connect: No serial data received`. Arreglos, en orden:

1. **Reintentar** el comando de esptool varias veces — el reset por USB-JTAG a
   veces necesita 2-3 intentos.
2. **Ciclo de alimentación físico**: desenchufa el USB, espera ~3 s, vuelve a
   enchufar y flashea.
3. **Modo descarga manual**: mantén pulsado el botón **BOOT** (recessed) de la
   PLACA mientras enchufas el USB.

> La placa **no se brickea** con esto: se recupera.

---

## 6. Ejecutar el agente

```bash
cd ~/Developer/Sebastian/agent
uv sync            # solo la primera vez
uv run agent.py dev
```

> `uv run agent.py start` es el modo de worker de producción; `dev` es local con hot-reload.

### CRÍTICO: un solo agente a la vez

Ejecuta **exactamente UN** proceso de agente. Varios agentes hacen que el
asistente **"hable consigo mismo"** o se comporte de forma errática.

Para matar procesos sueltos y arrancar uno limpio:

```bash
pkill -9 -f "agent.py"
# ...luego arranca uno solo
uv run agent.py dev
```

---

## 7. Dispatch: meter el agente en la sala del dispositivo

El agente **solo se auto-une a una sala de LiveKit NUEVA (fresca)**. Para meterlo
en la sala junto con el dispositivo: **borra la sala** y **resetea el dispositivo**
para que se re-una en una sala fresca.

```bash
# Requiere las env vars LIVEKIT_URL / LIVEKIT_API_KEY / LIVEKIT_API_SECRET
lk room delete sebastian

# Resetea la placa (se re-une fresca)
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset chip_id
```

Verifica que la sala tiene **2 participantes** (dispositivo + agente):

```bash
lk room list
```

---

## 8. Verificación de audio

El agente graba el track de micrófono entrante en `/tmp/sebastian_rx.wav`
(16 kHz mono). Para comprobar **lo que el agente realmente oye**:

```bash
afplay /tmp/sebastian_rx.wav      # escuchar
```

Para un análisis cuantitativo, un script corto en Python con los módulos `wave`
y `array` que calcule RMS por ventana de 0.5 s, pico, cuenta de clipping y un
espectro aproximado:

> Nota: `audioop` fue **eliminado en Python 3.13**, por eso se usa `array` en su
> lugar.

```python
import wave, array, math

with wave.open("/tmp/sebastian_rx.wav", "rb") as w:
    rate = w.getframerate()
    frames = w.readframes(w.getnframes())

samples = array.array("h")
samples.frombytes(frames)

window = int(rate * 0.5)  # ventanas de 0.5 s
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

> **Lección clave**: los **NIVELES** de audio no son lo mismo que la
> **INTELIGIBILIDAD**. Hay que **escuchar** y/o mirar el **espectro** — un RMS
> sano no garantiza que se entienda nada.

---

## 9. Monitor serie

```bash
cat /dev/cu.usbmodem101
# o bien
idf.py -p /dev/cu.usbmodem101 monitor
```

En el arranque, el log de init muestra:

- **Escaneo I2C**: espera `0x18` (AIC3104) y `0x2C` (XVF3800).
- La **DFU/versión del XVF**.
- `mic source started`.
- El estado de la sala (room state).

---

## 10. Flujo de trabajo completo (resumen)

```bash
# 1. Compilar
source ~/esp/esp-idf/export.sh
cd ~/Developer/Sebastian/firmware
idf.py build

# 2. Flashear
idf.py -p /dev/cu.usbmodem101 flash

# 3. Arrancar UN agente
cd ~/Developer/Sebastian/agent
pkill -9 -f "agent.py"
uv run agent.py dev

# 4. Dispatch a sala fresca
lk room delete sebastian
python -m esptool --chip esp32s3 -p /dev/cu.usbmodem101 --before default_reset --after hard_reset chip_id

# 5. Verificar
lk room list                 # -> 2 participantes
afplay /tmp/sebastian_rx.wav # -> escuchar lo que oye el agente
```
