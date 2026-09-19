# Pruebas de Sebastian

Referencia: **2026-09-19**, `84f278b6`. Describe las suites y workflows presentes,
no una ejecución nueva. La evidencia de placa y navegador está en
[SESSION.md](SESSION.md), [STATUS](docs/STATUS.md) y
[PRs recientes](docs/RECENT_CHANGES.md).

## Cobertura existente

| Capa | Qué se prueba | Qué no demuestra |
|---|---|---|
| Firmware, `core_test.zig` | DSP/decimación, PCM, pre-roll y su límite, compuerta, reductor de sesión, tiempos, selector, arbitraje USB, URL y gestos. | Calidad acústica, timings I2S reales, comportamiento de I2C/USB o estabilidad del transporte. |
| Build ESP-IDF | Compilación/enlace para ESP32-S3 y comprobaciones ABI C/Zig. | Que el binario funcione en la placa o se recupere de un fallo físico. |
| Go unitario | Flota, adopción, sesiones, máquina de reuniones, transcripciones, fragmentación y cliente del proveedor, con dobles y detector de carreras. | Recorrido completo por hardware y servicios externos. |
| Go integración | PostgreSQL, migraciones, eventos/outbox y JetStream. | Despliegue, persistencia del volumen de audio o recuperación tras pérdida de procesos. |
| Python | Identidad, pre-roll, señales de eco, texto y reuniones: codificador, subida reanudable, silencio e intención de voz. | Reconocimiento de órdenes reales con la acústica de la sala. |
| Dashboard | Lógica pura de flota/reuniones, tipos generados, tipos TypeScript y build. | Reproducción multimedia, WebSerial y recorrido visual completo en navegador. |

El protocolo de adopción tiene pruebas con un dispositivo simulado en Go. No
hay un harness unitario C para ejecutar directamente `adopt.c`/`provisioning.c`;
el simulador no reemplaza las comprobaciones de esas implementaciones en placa.

## Qué ejecuta CI

- [Firmware](.github/workflows/firmware-tests.yml): pruebas Zig nativas y formato,
  más build ESP-IDF **5.4.4**. Zig usa el fork fijado `0.16.0-xtensa`.
- [Servidor](.github/workflows/server-ci.yml): generación OpenAPI, validación de
  migraciones, formato, migración aplicada dos veces, `go test -race`, integración
  PostgreSQL/NATS, vet, build y análisis de vulnerabilidades.
- [Dashboard](.github/workflows/dashboard-ci.yml): generación de tipos sin diff,
  typecheck, Vitest, build con instalador y auditoría de dependencias.
- [Agente](.github/workflows/lint.yml): **Ruff 0.14.0**. El workflow actual no
  ejecuta pytest; disponer de tests en `agent/tests` no implica cobertura en CI.

El `make check` de la raíz ejecuta **solo `fw-test`**. No agrega las suites de los
otros componentes.

## Comandos locales

Todos los comandos siguientes se ejecutan **dentro del devcontainer**, con las
dependencias instaladas y PostgreSQL/NATS del Compose disponibles. ffmpeg debe
estar en PATH para las pruebas de reuniones del agente; las imágenes de agente y
servidor lo incluyen, pero el Dockerfile de desarrollo no lo instala actualmente.

```bash
# Desde /workspace
make fw-test
make fw-build
make server-check
make -C server integration
make -C server build
make -C server vuln
make dashboard-check

# Desde /workspace/agent
uv run pytest
uvx ruff==0.14.0 check .
```

`make dashboard-check` también construye el instalador. Instalar antes sus
dependencias según [DEVCONTAINER.md](docs/DEVCONTAINER.md).
El build de firmware puede reescribir la ruta de LiveKit en
`firmware/dependencies.lock`; revisar ese diff y conservar la ruta relativa.

## Aceptación pendiente de reuniones

Ejecutar con versiones identificadas de firmware, servidor, agente y dashboard.
La implementación completa está en #50; no asumir que la instalación de `main`
ya contiene B–F.

| Caso | Verificación esperada |
|---|---|
| Inicio/parada desde panel y gesto | Una reunión por orden, estado coherente, anillo rojo y vuelta a reposo al finalizar. |
| Inicio por voz en conversación | Confirmación audible, cierre de conversación y comienzo de captura sin dos sesiones compitiendo. |
| Parada por voz durante reunión | Activación, reconocimiento de la orden, confirmación y parada; frase ajena no para la grabación. |
| Mute y avisos | Indicadores coherentes y ausencia de voz grabada mientras está silenciado. |
| Reproductor en navegador normal | Carga de metadatos, reproducción audible, pausa, seek y correspondencia con la transcripción. Probar una grabación larga. |
| Silencio y duración máxima | Aviso y cierre en los límites configurados para esa unidad, sin parada duplicada. |
| Pérdida de WiFi/LiveKit | Estado final explicable, audio recibido conservado y unidad recuperable. |
| Corte de subida HTTP | Offset correcto al reanudar, sin duplicar ni perder bytes ya confirmados. |
| Reinicio del agente | Comprobar qué audio persiste y qué pasa con la reunión y el spool; registrar cualquier recuperación que requiera acción manual. |
| Reinicio del servidor | Recuperación de trabajos elegibles y estado visible coherente con audio/DB. Incluir reuniones cortadas. |
| Proveedor ausente, error o rate limit | Estado y error visibles, reintento limitado y posibilidad de reintentar desde el panel. |
| Disco lleno o volumen inaccesible | Fallo visible, sin declarar una grabación completa; comprobar recuperación tras liberar espacio. |
| Retención, conservar y borrar | Se respeta la exclusión de conservar y se retiran audio y contenidos según política. |
| Captura USB durante una sesión | Cede el micrófono sin lectores concurrentes; la sesión/reunión termina de forma coherente y la escucha vuelve cuando corresponde. |

Esto es una lista de aceptación; no afirma que todos los comportamientos estén
resueltos. Para cada fallo, añadir una regresión en la capa más pequeña capaz de
reproducirlo y repetir el recorrido físico afectado.

## Regresión de flota

Repetir adopción por consentimiento y credenciales, traslado entre control rooms,
rotación de secreto, olvido, configuración deseada frente a ejecutada y reversión
de WiFi. Confirmar que un anuncio mDNS viejo no sobreescribe un contacto más
reciente, que una MAC identifica la misma unidad en panel y sala, y que los
eventos de rechazo permanecen visibles con su fecha.

Tras cambios de aprovisionamiento, probar lectura/edición por WebSerial con la
contraseña WiFi conservada y la ventana ampliada; tras cambios de perfiles,
probar selector y fallback sin sobrescribir permanentemente el perfil elegido.

## Audio y pruebas prolongadas

Los probes de `xvf_aec.zig` permiten medir referencia, convergencia y ambos canales;
sus flags son de compilación y deben quedar apagados en la imagen normal. Los
buffers de diagnóstico pueden consumir la RAM interna necesaria para transporte.

La evaluación acústica incluye voz a distancia, ruido/TV, doble habla, volumen,
activaciones falsas y captura USB. Registrar muestras y escucharlas: nivel RMS y
una suite lógica verde no acreditan inteligibilidad.

HIL en runner, reproducción de trazas reales y banco acústico siguen siendo
posibles ampliaciones. La prioridad actual es cerrar la matriz de aceptación y
el uso continuado de [MILESTONE.md](MILESTONE.md).

## Registro de resultados

Anotar fecha, commit/imagen de cada componente, identidad de unidad, perfil,
configuración, entorno y resultado. Adjuntar los IDs de sala/reunión y logs
necesarios, sin secretos. Conservar por separado:

- resultados automatizados y su comando;
- pruebas en placa y navegador;
- incidencias pendientes y pasos de reproducción.

Las anotaciones antiguas de SESSION son evidencia para la versión probada,
no un certificado de comportamiento para los commits posteriores.
