# Instalador web de Sebastian

Instalador para ReSpeaker XVF3800 + XIAO ESP32-S3 mediante ESP Web Tools y
WebSerial. La misma aplicación se publica en GitHub Pages y se incorpora al
dashboard en `/installer/`.

WebSerial necesita un navegador que exponga `navigator.serial` y un contexto
permitido (HTTPS o localhost). Comprobar la disponibilidad en el navegador; no
inferirla del nombre del sistema operativo.

## Instalar y configurar

El firmware de fábrica permite configurar WiFi, URL del control room, modo,
perfiles y datos de adopción en **NVS**, después del flasheo. Es un binario común;
no requiere compilar las credenciales de cada unidad.

La interfaz permite importar/exportar configuración, enviarla por serie y
**leer la guardada en el dispositivo**. La lectura conserva la contraseña WiFi
sin devolverla y mantiene abierta la ventana USB durante 120 s. Después del
arranque TinyUSB usa el periférico como micrófono; reconectar para volver a la
ventana de aprovisionamiento.

El instalador embebido obtiene datos de organización y control room desde
`/installer/control-room.json`. Ese endpoint entrega información sensible de
aprovisionamiento al navegador y debe compartir el acceso protegido del panel.
La unidad genera su secreto propio y puede incorporarse automáticamente al
control room configurado.

Ver [PROVISIONING.md](PROVISIONING.md) y el contrato
[sebastian-config.schema.json](sebastian-config.schema.json). Algunos campos
conservados por compatibilidad no gobiernan el firmware: el canal de micrófono,
por ejemplo, se decide al compilar; el actual es LEFT/comms.

## Empaquetado

Desde el devcontainer y la raíz del repositorio:

```bash
make fw-build
tools/prepare_web_installer.sh
```

El script prepara el binario fusionado y `manifest.json` bajo `docs/installer/`.
El build público y el del dashboard empaquetan firmware de fábrica desde sus
workflows. La interfaz consume el manifiesto de su propia base; las antiguas
instrucciones de parámetros `?manifest`, `?bin` y `?config` no corresponden al
código actual.

## Recuperación de conexión

Cerrar otras lecturas del puerto (bridge, monitor o diálogo de instalación).
Si no sincroniza, reconectar la unidad o mantener BOOT del XIAO al conectarla
para entrar al bootloader. El RESET del ReSpeaker resetea el XVF.

Una placa nueva con firmware XVF de familia USB requiere el procedimiento inicial
por USB DFU documentado en el repositorio, `docs/XVF3800.md`. El instalador ESP32
no sustituye ese cambio de familia del chip XMOS.
