# Cambios recientes y procedencia

Revisión del historial y de GitHub realizada el **2026-09-19**. El último bloque
integrado antes de septiembre corresponde al 26–27 de julio; los commits del
18–19 de septiembre concentran los cambios de estabilidad, flota y reuniones.
Las fechas se interpretan en Europe/Madrid.

## Base anterior: julio

| PR | Resultado | Qué aporta |
|---|---|---|
| [#36](https://github.com/Zetesis-Labs/Sebastian/pull/36) | Integrado | API Go, PostgreSQL/Bun, Atlas, outbox/NATS, catálogo de grabaciones, dashboard SSR y comprobaciones del ABI C/Zig. |
| [#40](https://github.com/Zetesis-Labs/Sebastian/pull/40) | Integrado | Un binario con perfiles `agente` y `micro-usb`, selector al arrancar y fallback de perfil ante fallos de arranque. |
| [#41](https://github.com/Zetesis-Labs/Sebastian/pull/41) y [#42](https://github.com/Zetesis-Labs/Sebastian/pull/42) | Integrados; #42 llevó el contenido a `main` | Control de perfiles por red, convivencia USB/agente, arena de activación en PSRAM y ajuste del AGC. |
| [#43](https://github.com/Zetesis-Labs/Sebastian/pull/43) | Release integrada | Punto de referencia previo a la actividad de septiembre. |

#40 y #42 registran pruebas en hardware de perfiles, USB y convivencia. #41
conserva una descripción de validación anterior; la evidencia posterior de #42
amplía esa cobertura. Evitar sumar sus commits como funcionalidades independientes:
son PRs apilados con contenido compartido.

## 17–18 de septiembre: estabilidad y aprovisionamiento

Estos cambios aparecen directamente en el historial de `main`, antes de los PRs
de flota:

| Commit | Cambio y consecuencia |
|---|---|
| `4dc968d4` | Limita el pre-roll enviado a 80 KB/2,5 s; el anillo de 12 s no se envía completo. Evita superar la caché SCTP de 100 KB. |
| `87264fde` | Reduce la frecuencia de consulta de los LEDs cuando el XVF deja de responder por I2C. |
| `352b93bf` | Guarda pánicos en flash y reporta causa de reset, backtrace y comprobaciones de heap por syslog. |
| `7aafa852` | Fija ESP-IDF 5.4.4 en CI; la sesión identifica un pánico del driver I2C en 5.4.0. |
| `45f4bf75` | Conserva el error de persistencia en la cadena de error al abrir una sesión. |
| `c6651a7c`, `fc267e08` | Documentan unidades con firmware XVF de familia USB y la conversión inicial a `inthost` por USB DFU. |
| `f48aea91`, `c3428d28` | Leer configuración desde la unidad, conservar la contraseña WiFi sin devolverla y mantener abierta 120 s la ventana WebSerial. |

[SESSION.md](../SESSION.md) conserva la reproducción de fallos y las pruebas de
campo. La corrección del driver evita el pánico del ESP32; no prueba que el XVF
responda correctamente bajo cualquier condición acústica.

## 18 de septiembre: flota integrada

| PR | Estado en GitHub | Alcance final |
|---|---|---|
| [#46](https://github.com/Zetesis-Labs/Sebastian/pull/46) | Integrado | Descubrimiento mDNS, adopción UDP, consentimiento físico, secretos por unidad, configuración deseada/ejecutada e instalador embebido. |
| [#47](https://github.com/Zetesis-Labs/Sebastian/pull/47) | Integrado | Eventos persistentes de unidad, olvido, QR, versión del instalador y retirada de `/token` del servidor Go y del firmware. |
| [#48](https://github.com/Zetesis-Labs/Sebastian/pull/48) | Integrado | Consentimiento con secreto nacido en placa, corrección de fuga mDNS e identidad del participante tomada de los metadatos del despacho. |

El contrato evolucionó dentro de #46. El estado final es **secreto generado por
la placa y entregado durante adopción/incorporación**, con rotación opcional.
Las descripciones iniciales que hablan de secreto generado por el servidor o de
fallback a `/token` no describen el resultado después de #47.

#48 y SESSION registran adopción con MUTE, conversación con identidad MAC,
reversión de WiFi, regeneración de secreto y traslado entre control rooms. El
rescate físico acordado en la especificación es USB; RF-67 se descartó.

## 18–19 de septiembre: reuniones

| PR/commit | Estado | Alcance |
|---|---|---|
| [#49](https://github.com/Zetesis-Labs/Sebastian/pull/49), hasta `3f3d74fb` | Integrado en `main` | Bloque A: estado, órdenes, confirmación, audio reanudable, persistencia y outbox. |
| [#50](https://github.com/Zetesis-Labs/Sebastian/pull/50), `ac5a9ae0`–`321a95cf` | Abierto | B–E: firmware, captura Python, transcripción/resumen y panel. |
| #50, `79bd4286` | Abierto | Dobles de pruebas seguros ante carreras para reloj y worker. |
| #50, `84f278b6` | Abierto | F: inicio y parada por voz. |

**El alcance final de #50 es B–F:** el commit `84f278b6` incorporó las órdenes
por voz después de la descripción inicial B–E. Esta actualización documental
refleja ese alcance y toma dicho commit como referencia del código revisado.

Evidencia registrada: B/C probados en `68ee`, tres reuniones transcritas con
hablantes y resumen, inicio/parada y límites desde el panel. La prueba manual
del reproductor y el flasheo/prueba de F siguen pendientes según SESSION.
Los checks consultados de #46–50 terminaron correctamente; el CI Python ejecuta
Ruff, no pytest. Un check verde no sustituye las pruebas de aceptación en placa.

## Ramas y release que siguen separadas

- [#44](https://github.com/Zetesis-Labs/Sebastian/pull/44), endpoint permanente,
  sigue abierto. Propone conexión continua y modelo perezoso, con recuperación
  ante pérdida de agente/conexión. Su descripción registra compilación y pruebas
  lógicas, sin prueba en hardware. No forma parte del recorrido actual por
  activación de `main` ni de `feat/meetings-b`.
- [#45](https://github.com/Zetesis-Labs/Sebastian/pull/45), release de `main`, sigue
  abierto. En la revisión propone agente 0.1.9, servidor/dashboard 1.1.0 y chart
  0.1.7. Esos números son **versiones propuestas**, no una publicación confirmada.
- PR integrado, release publicada, firmware flasheado y backend desplegado son
  hechos distintos. Esta revisión no consultó el estado vivo del cluster.

## Cómo interpretar la documentación

[STATUS.md](STATUS.md) y [ARCHITECTURE.md](ARCHITECTURE.md) describen este código.
Las especificaciones 12–14 conservan requisitos y decisiones, incluidas partes
que requieren aceptación. [ROADMAP.md](../ROADMAP.md) separa prioridades actuales
del material histórico. Las evidencias deben incluir fecha, commit, unidad y
entorno para no trasladar una prueba de julio a un recorrido añadido en septiembre.
