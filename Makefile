# Sebastian — puntos de entrada del entorno.
#
# DENTRO del devcontainer (build + servicios):
#   make fw-build     compila el firmware (ESP-IDF + Zig, todo en el contenedor)
#   make agent        agente LiveKit en dev (hot-reload)
#   make token        token server (:8787, publicado a la LAN)
#   make provision    (re)carga el dashboard de Grafana
#
# En el HOST macOS (lo único que necesita el USB):
#   make flash        A) flashea nativo los artefactos del contenedor (uvx esptool)
#   make serial-share B) comparte el USB por TCP (RFC2217) → flash/bridge DESDE el contenedor
#   make bridge       bridge serial → OTLP (host, o contenedor con SEBASTIAN_SERIAL_URL)
#
# Con serial-share activo, DENTRO del contenedor (SEBASTIAN_HOST_IP = IP LAN del Mac):
#   make fw-flash                                  flashea vía rfc2217
#   SEBASTIAN_SERIAL_URL=rfc2217://$IP:4000 make bridge

.PHONY: fw-build fw-flash agent token provision flash bridge serial-share

fw-build:
	cd firmware && idf.py build

# rfc2217 no reenvía el auto-reset del USB-JTAG: pon el chip en download mode
# a mano (mantén BOOT, pulsa RESET, suelta BOOT) antes de lanzar esto.
fw-flash:
	cd firmware && idf.py -p "rfc2217://$${SEBASTIAN_HOST_IP:?exporta la IP LAN del Mac}:4000?ign_set_control" flash

# Comparte el USB del host por TCP para que el contenedor flashee (autodetectado
# por tools/devtools.sh en host.docker.internal:4000). Déjalo corriendo.
# Versión PINEADA a la del contenedor (IDF 5.4 → esptool 4.11.0): el reset por
# rfc2217 se negocia con un protocolo propio; si cliente y servidor no coinciden,
# conecta pero NO resetea a bootloader ("Wrong boot mode detected").
serial-share:
	uvx --from "esptool==4.11.0" esp_rfc2217_server.py -p 4000 \
		$$(ls /dev/cu.usbmodem* /dev/ttyACM* /dev/ttyUSB* 2>/dev/null | head -1)

agent:
	cd agent && uv run agent.py dev

token:
	cd agent && uv run token_server.py

provision:
	tools/telemetry/provision.sh

flash:
	tools/flash.sh

bridge:
	uv run tools/telemetry/bridge.py
