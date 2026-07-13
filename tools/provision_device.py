# /// script
# requires-python = ">=3.11"
# dependencies = ["pyserial>=3.5"]
# ///
"""Provisiona el device por serial con un JSON sebastian.config.v1.

Corre en el HOST (Docker en macOS no ve el USB), igual que tools/flash.sh:

    uv run tools/provision_device.py tools/provisioning/full_duplex.json [puerto]

Envía la línea de provisioning, espera el ack, y muestra el arranque posterior
(fixed_beam aplicado, WiFi, detección activa) para verificar el modo. Las
claves ausentes del JSON conservan su valor en NVS — en particular el password
WiFi, por eso los JSON de tools/provisioning/ no lo llevan (y por eso están
fuera de git: .git/info/exclude).
"""

import glob
import json
import sys
import time

import serial

VERIFY_SECONDS = 30
INTERESTING = (
    "sebastian.config",
    "fixed_beam",
    "AEC config",
    "wifi creds",
    "Connected:",
    "detection active",
)


def find_port() -> str:
    for pattern in ("/dev/cu.usbmodem*", "/dev/ttyACM*", "/dev/ttyUSB*"):
        ports = sorted(glob.glob(pattern))
        if ports:
            return ports[0]
    sys.exit("Sin puerto serie (cu.usbmodem*/ttyACM*/ttyUSB*) — ¿está enchufado el device? (esto corre en el HOST, no en el devcontainer)")


def main() -> None:
    if len(sys.argv) < 2:
        sys.exit(f"uso: {sys.argv[0]} <config.json> [puerto]")
    cfg = json.loads(open(sys.argv[1], encoding="utf-8").read())
    if cfg.get("schema") != "sebastian.config.v1":
        sys.exit("el JSON debe llevar schema=sebastian.config.v1")
    port = sys.argv[2] if len(sys.argv) > 2 else find_port()

    line = "sebastian.config.v1 " + json.dumps(cfg) + "\n"
    ser = serial.Serial(port, 115200, timeout=1)
    ser.reset_input_buffer()
    ser.write(line.encode())
    ser.flush()
    print(f">>> {port}: mode={cfg.get('mode', '?')}")

    acked = False
    deadline = time.time() + VERIFY_SECONDS
    buf = b""
    while time.time() < deadline:
        buf += ser.read(4096)
        text = buf.decode("utf-8", errors="replace")
        if "sebastian.config.err" in text:
            ser.close()
            sys.exit(f"el device rechazó la config: {text.splitlines()[-1]}")
        if not acked and "sebastian.config.ok" in text:
            acked = True
            print("<<< sebastian.config.ok — reiniciando para aplicar")
    ser.close()

    for ln in buf.decode("utf-8", errors="replace").splitlines():
        if any(k in ln for k in INTERESTING):
            print(f"    {ln.strip()}")
    if not acked:
        sys.exit("sin ack del device — ¿algo más está leyendo el serial (bridge/monitor)?")
    print("[provision] OK")


if __name__ == "__main__":
    main()
