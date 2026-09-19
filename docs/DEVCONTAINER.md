# Entorno de desarrollo

El devcontainer reúne compilación de firmware, agente Python, API Go y dashboard.
El Compose añade PostgreSQL 18, una base separada para Atlas, NATS JetStream,
LiveKit y observabilidad LGTM/Promtail. En macOS, el USB se controla desde el host.

## Preparación

1. Definir `LIVEKIT_NODE_IP` y `LIVEKIT_API_SECRET` en `.devcontainer/.env`,
   siguiendo [BUILD_AND_RUN.md](BUILD_AND_RUN.md). La IP debe ser alcanzable desde
   la placa; se utiliza tanto para señalización como para anunciar medios.
2. Abrir el repositorio/worktree correcto en VS Code o Cursor y ejecutar
   «Reopen in Container».
3. `onCreateCommand` instala dependencias Python, Go y dashboard. El instalador
   embebido necesita además sus dependencias; desde `/workspace/web-installer`:

   ```bash
   npx --yes pnpm@9 install --frozen-lockfile
   ```

4. Configurar claves de proveedor y organización en los procesos correspondientes.
   El Compose solo aporta los valores locales de infraestructura. Para reuniones,
   comprobar también ffmpeg: está en las imágenes de aplicación pero no se
   instala actualmente en `.devcontainer/Dockerfile`.

La imagen base es `espressif/idf:release-v5.4`; el CI está fijado a **5.4.4**.
Revisar `idf.py --version` si el contenedor se creó antes de las correcciones de
septiembre. Zig `0.16.0-xtensa` se descarga y verifica durante el build. Go, Node,
Atlas y Helm se incorporan desde las imágenes/versiones declaradas en el Dockerfile.

## Procesos y puertos

Desde `/workspace`, dentro del contenedor:

| Comando | Función |
|---|---|
| `make server-migrate` | Aplica migraciones antes de arrancar la API. |
| `make server-run` | API Go, publicada al host en `:8787`. |
| `make server-outbox` | Publica el outbox a NATS. |
| `make agent` | Worker LiveKit en modo desarrollo. |
| `make dashboard-dev` | Panel, publicado en `:3001`. |
| `make fw-test` / `make fw-build` | Pruebas lógicas y compilación del firmware. |
| `make provision` | Provisiona el dashboard de Grafana. |

Los servicios se ejecutan en terminales separados. LiveKit expone `:7880`,
`:7881` y UDP `:7882`; LGTM expone Grafana y OTLP según el Compose. El agente usa
`http://lgtm:4318` para OTel dentro de la red de contenedores.

`make token` queda como utilidad Python antigua. El flujo activo usa el servidor
Go y sesiones autenticadas; no arrancar ambos en el mismo puerto.

## Volúmenes y artefactos

- El código se monta en `/workspace`.
- El venv Python vive en `agent_venv`, que cubre `/workspace/agent/.venv`: un
  intérprete instalado dentro del contenedor no debe depender de symlinks a un
  Python del host.
- Las cachés de Go y los datos de PostgreSQL/NATS usan volúmenes separados.
- La base `postgres-schema` es auxiliar de Atlas; no sustituye a la base de datos
  de desarrollo.
- `firmware/build` contiene los artefactos que luego flashea el host. No reutilizar
  una caché CMake de macOS para compilar dentro de Linux.

El nombre de proyecto Compose puede variar por worktree. Usar los servicios y la
red del Compose correspondiente, sin presuponer un `container_name` fijo.

## USB

La vía habitual en macOS es compilar en el contenedor y ejecutar en el host:

```bash
make flash
make bridge
```

El bridge ocupa el puerto: detener su lectura para flashear o usar WebSerial.
TinyUSB convierte el periférico en micrófono después de la ventana inicial de
aprovisionamiento; una reconexión abre esa ventana de nuevo.

La alternativa es `make serial-share` en el host y `make fw-flash` dentro del
contenedor con `SEBASTIAN_HOST_IP` configurada. RFC2217 requiere un único cliente
y el reset al bootloader puede necesitar intervención física. En Linux puede
mapearse el dispositivo USB directamente al contenedor mediante `devices:`.

La imagen normal incorpora `okay_nabu.tflite`; el entrenamiento de una palabra
personalizada no es un prerrequisito del entorno. Las suites y sus límites están
en [TESTING.md](../TESTING.md).
