# Devcontainer — el entorno entero en un contenedor (menos el USB)

Todo el entorno de desarrollo corre en el devcontainer: **build de firmware
(ESP-IDF v5.4 + fork Zig-Xtensa), agente LiveKit, token server y el stack de
observabilidad LGTM**. La única frontera física es el USB: **Docker en macOS no
puede pasar dispositivos serie al contenedor**, así que flashear y el bridge de
telemetría son los dos únicos comandos que se ejecutan en el host.

```
┌─ devcontainer (dev) ─────────────────┐   ┌─ compose (lgtm) ──────────────┐
│ idf.py build  (ESP-IDF + Zig linux)  │   │ grafana/otel-lgtm             │
│ make agent    (livekit-agents + HA)  │──▶│ :3000 Grafana  :4318 OTLP     │
│ make token    (:8787 → LAN)          │   └───────▲───────────────────────┘
└──────────────────┬───────────────────┘           │ OTLP
                   │ artefactos en firmware/build/ │
┌─ host macOS ─────▼───────────────────────────────┴───────────────────────┐
│ make flash   (uvx esptool @flash_args — único que toca /dev/cu.usbmodem*)│
│ make bridge  (serial → OTLP localhost:4318)                              │
└──────────────────────────────────────────────────────────────────────────┘
```

## Arrancar

1. VS Code / Cursor: "Reopen in Container" (o `devcontainer up`). El
   `postCreate` sincroniza el venv del agente; el `postStart` provisiona el
   dashboard de Grafana.
2. Dentro del contenedor: `make fw-build`, `make token`, `make agent`.
3. En el host: `make flash` (tras un build) y `make bridge`.

El token server se publica en `0.0.0.0:8787` del Mac, así que el device lo
alcanza en la IP LAN del Mac igual que antes (`secrets.zig` no cambia).

## Migración desde el entorno host (una sola vez)

- `docker rm -f sebastian-lgtm` — quita el LGTM viejo arrancado a mano con
  `docker run`. Su nombre fijo bloqueaba al compose del devcontainer (conflicto
  de nombre entre proyectos). El compose levanta el suyo, `<proyecto>-lgtm-1`.
- `rm -rf firmware/build` — la caché de CMake del host (macOS) no vale en
  Linux; el primer `make fw-build` del contenedor la regenera y descarga el
  Zig `linux-musl` pineado en `cmake/zig.cmake` (ya multiplataforma).
- **El venv del agente vive en un volumen nombrado** (`agent_venv`), no en el
  mount: el `python` de un venv uv es un symlink a un intérprete local al
  contenedor, así que un venv en el mount compartido cuelga en otro
  contenedor/host y aparece incompleto (Pylance no resuelve `livekit`…). Al
  crear/rebuildear el contenedor, `postCreate` (`uv sync`) lo puebla; el volumen
  lo mantiene íntegro entre reinicios y aislado del venv del host.
- Matar agente/token del host (`pkill -f agent.py; pkill -f token_server`) —
  pasan a vivir en el contenedor. El bridge se queda en el host.

### MCP de Grafana tras la migración

El `container_name` fijo se quitó (chocaba entre proyectos). El contenedor de
LGTM pasa a llamarse `sebastian_devcontainer-lgtm-1` (VS Code) o
`devcontainer-lgtm-1` (compose manual). Dos formas de apuntar el MCP:

- **Por red compartida**: `--network container:sebastian_devcontainer-lgtm-1`
  y `GRAFANA_URL=http://localhost:3000` (como antes, nombre nuevo).
- **Por la red del compose** (más estable): `--network sebastian_devcontainer_default`
  y `GRAFANA_URL=http://lgtm:3000` (resuelve por nombre de servicio, inmune a
  renombrados). Recomendado.

## Por qué así

- **Firmware en contenedor**: `cmake/zig.cmake` ya pineaba hashes para
  `aarch64/x86_64-linux-musl`, y la imagen `espressif/idf:release-v5.4` trae el
  toolchain Xtensa completo. Ningún build vuelve a depender del Mac.
- **Flasheo por `flash_args`**: los artefactos del build (rutas relativas) se
  flashean desde el host con esptool a pelo (`uvx --from esptool esptool.py`),
  sin instalar ESP-IDF en el Mac.
- **`container_name: sebastian-lgtm`**: estable para que el bridge del host
  (localhost:4318) y el MCP de Grafana no cambien.
- El agente dentro del contenedor exporta OTel a `http://lgtm:4318`
  (variable `OTEL_EXPORTER_OTLP_ENDPOINT` en el compose).

## El USB en macOS: dos opciones

`devices:` de compose solo funciona en hosts Linux (mapea nodos del kernel).
En macOS, Docker corre en una VM Linux y `/dev/cu.usbmodem*` es un device de
Darwin: no existe dentro de la VM, no hay nada que mapear. Las dos vías:

- **A (por defecto): flasheo nativo en el host.** `make flash` usa
  `uvx esptool` contra `firmware/build/flash_args` (rutas relativas — los
  artefactos del contenedor se flashean tal cual). `make bridge` en el host.
- **B (todo dentro): serial compartido por TCP.** En el host,
  `make serial-share` levanta `esp_rfc2217_server` (viene con esptool, con
  líneas de reset incluidas). Dentro del contenedor:
  `SEBASTIAN_HOST_IP=<ip-lan-del-mac> make fw-flash`, y el bridge con
  `SEBASTIAN_SERIAL_URL=rfc2217://<ip>:4000 make bridge`. Ojo: usar la IP LAN
  del Mac, no `host.docker.internal` (no ruta en este Docker). El servidor
  RFC2217 admite UN cliente a la vez: parar el bridge para flashear, como en
  el host.

## Portabilidad por plataforma

| | Contenedor (build+agente+LGTM) | Serial (flash/bridge) |
|---|---|---|
| **macOS** | ✓ | Host (opción A) o RFC2217 (opción B) |
| **Linux** | ✓ | **Directo al contenedor**: descomenta `devices:` en el compose y todo (flash incluido) corre dentro — el mejor caso |
| **Windows** | ✓ (Docker Desktop + WSL2) | `usbipd-win attach --wsl` mete el USB en WSL2 (`/dev/ttyACM0`) → `devices:` como en Linux; o RFC2217 desde PowerShell |

La autodetección de puerto (bridge y `flash.sh`) cubre `cu.usbmodem*` (macOS)
y `ttyACM*`/`ttyUSB*` (Linux/WSL2). El único componente atado a una plataforma
es el entrenamiento del wake word (MPS de Apple).

## Qué NO va en el contenedor

- **Entrenamiento del wake word** — fuera de alcance a propósito: el modelo
  entrenado (`firmware/main/sebastian.tflite`) va commiteado en el repo y el
  entorno funciona con él de serie. Quien quiera otra palabra se lo entrena
  con los scripts de `wakeword/` (flujo de host con GPU/MPS; el trainer de
  ~40GB está gitignored).
