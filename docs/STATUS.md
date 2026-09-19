# Estado de Sebastian

Actualizado el **2026-09-19** contra `84f278b6` (`feat/meetings-b`). Describe este
árbol de código; no acredita el estado del despliegue. La evidencia de hardware y
navegador citada procede de [SESSION.md](../SESSION.md), no de nuevas pruebas
realizadas durante esta actualización documental.

## Integración y publicación

La revisión de GitHub confirma #46–48 (flota) y #49 (reuniones A) integrados.
#50 contiene B–F y sigue abierto; F se incorporó en el commit `84f278b6`.
La release #45 y el endpoint permanente #44 también
siguen abiertos. [RECENT_CHANGES.md](RECENT_CHANGES.md) detalla la secuencia,
los commits y las pruebas registradas.

## Implementación y evidencia

| Capacidad | Implementación actual | Evidencia y límite |
|---|---|---|
| Conversación | Activación local «Okay Nabu», sesión autenticada, sala nueva, agente Gemini/OpenAI, interrupción y cierre. | Las sesiones registran uso en placa y correcciones de fallos. No equivale a una prueba continua de estabilidad. |
| Audio | LEFT/comms a 48 kHz, captura acompasada al consumidor, AEC verificado por lectura de configuración y modos full/half duplex. | Hay pruebas acústicas históricas. El comportamiento depende de la sala, el volumen y la configuración. |
| USB y perfiles | Micrófono UAC2 mono, perfiles `agente`/`micro-usb`, arbitraje de captura y selección de perfil. | Los PRs #40/#42 registran pruebas en hardware de perfiles y convivencia. Incluirlas en las regresiones de reuniones. |
| Flota | Descubrimiento, adopción, secreto propio de la unidad, incorporación automática, configuración deseada/ejecutada, traslado y olvido. | SESSION registra pruebas de varias transiciones en la unidad `68ee`. La cobertura funcional completa necesita un recorrido de aceptación explícito. |
| Reuniones A–C | Estados, órdenes por LAN/sondeo, gesto MUTE, LEDs, captura dedicada y subida reanudable. | SESSION registra pruebas de B/C en `68ee`. |
| Reuniones D | Transcripción por fragmentos, hablantes, resumen, reintentos y retención. | SESSION registra tres reuniones transcritas con dos hablantes y resúmenes. Falta validar fallos prolongados y recuperación. |
| Reuniones E | Lista, detalle, búsqueda, transcripción sincronizada, exportación y límites por unidad. | SESSION registra inicio/parada y edición de límites desde Chrome. Reproducción manual pendiente. |
| Reuniones F | Inicio desde conversación y parada por voz durante la grabación. | Implementado con pruebas automatizadas; la sesión registra firmware compilado, pendiente de flasheo y prueba de ambas órdenes. |

## Decisiones vigentes

- **Activación:** modelo stock `okay_nabu.tflite`. Las métricas de entrenamiento de
  «Sebastián» que aparecen en documentos de julio no describen este modelo.
- **Audio:** `mic_channel = .left`. Los valores base de `fixed_beam` y
  `full_duplex` son `true`, modificables mediante configuración/perfil. Si falla
  la configuración necesaria del AEC, el firmware fuerza half-duplex.
- **Audio previo a la activación:** el anillo tiene 12 s de capacidad, pero el
  envío se limita a los últimos 2,5 s (80 KB de PCM) para respetar el presupuesto
  del transporte SCTP. Capacidad del anillo y tamaño enviado son distintos.
- **Conversación:** Gemini por defecto, OpenAI seleccionable. No hay cambio
  automático de proveedor conversacional por fallo. BVC depende del despliegue;
  no se presupone en LiveKit alojado localmente.
- **Identidad:** MAC normalizada y secreto generado por la propia unidad. Las
  sesiones usan `POST /v1/sessions`; el servidor Go ya no expone `/token` legado.
- **Persistencia:** PostgreSQL guarda el estado y los eventos/outbox. NATS entrega
  eventos a otros consumidores. El audio de reuniones se guarda en el directorio
  del servidor; su metadata y transcripción están en PostgreSQL.
- **Dos clases de grabación:** los WAV de diagnóstico de conversación, el catálogo
  `recordings` y las reuniones `meetings` tienen recorridos distintos. La
  retención de reuniones no limpia automáticamente los WAV de diagnóstico.

## Correcciones recientes que condicionan el trabajo

La sesión del 17–19 de septiembre documenta dos causas de pánicos: el driver I2C
con ESP-IDF 5.4.0 y envíos de pre-roll superiores a la caché SCTP. El CI de firmware
usa 5.4.4 y el envío está acotado. También se incorporaron volcado de pánicos a
flash y envío de su resumen por syslog al arrancar.

En flota se corrigieron fugas de goroutines del descubrimiento mDNS, desbordamiento
de pila al reportar configuración y uso de una identidad fija en el agente. La
identidad del participante procede ahora de los metadatos del despacho.

Las notas mantienen abierta la pérdida de respuesta del XVF con el altavoz a
máximo volumen. Una compilación correcta no cierra esa comprobación acústica.

## Prioridades de estabilización

1. **Cerrar reuniones en hardware y navegador:** inicio/parada por voz, gesto,
   mute, LEDs y reproducción con búsqueda temporal. Registrar commit, unidad,
   configuración y resultado.
2. **Probar las interrupciones entre componentes:** WiFi/LiveKit, reinicio del
   agente o servidor, subida parcial, reintentos y almacenamiento lleno. Comprobar
   audio conservado, estado visible y posibilidad de recuperación; no dar por
   cubiertos estos casos por una prueba del recorrido normal.
3. **Cerrar los criterios de operación autónoma:** actualización y recuperación,
   acceso administrativo, retención y diagnóstico. Los criterios concretos están
   en [MILESTONE.md](../MILESTONE.md).
4. **Uso continuado:** mantener un registro de activaciones falsas, cierres
   inesperados, interrupciones y reuniones incompletas para priorizar sobre
   evidencia de uso.

El [plan de pruebas](../TESTING.md) convierte estas prioridades en verificaciones.
Los documentos de diseño expresan requisitos; una casilla histórica o un bloque
implementado no certifican su aceptación ni su despliegue.
