# Criterios para operación autónoma

Actualizado el **2026-09-19** contra `84f278b6`. Sustituye la fotografía de julio:
ya existen API Go autenticada, flota, perfiles, syslog y reuniones. Este documento
conserva los identificadores A–E usados en el historial y distingue implementación
de aceptación operativa. Una fila implementada no implica criterio cerrado.

Estado y procedencia: [STATUS](docs/STATUS.md) y
[PRs/commits recientes](docs/RECENT_CHANGES.md).

## A. Acceso y credenciales

| ID | Estado en el código | Criterio pendiente de aceptación |
|---|---|---|
| A1 | `/token` retirado de Go/firmware; `POST /v1/sessions` requiere identidad y secreto de unidad. | Comprobar en el entorno de despliegue que peticiones sin credenciales no crean sesiones ni dispatches; medir y acotar solicitudes repetidas. |
| A2 | API administrativa con `X-Admin-Secret`, SSR en el dashboard. `announce` sigue en el control plane Python separado. | Verificar acceso real al panel y al control plane, autenticación y límites de texto/peticiones. El secreto SSR no autentica al usuario del navegador. |
| A3 | La configuración admite URLs de red local; no hay una garantía general de TLS en todo el recorrido. | Documentar transporte, red de confianza y exposición de cada servicio; comprobar el recorrido de credenciales en ese entorno. |
| A4 | El chart requiere secretos existentes para los componentes principales; el Compose incluye valores de desarrollo. | Verificar los secretos y el render de producción, evitando trasladar las credenciales del Compose. |

El instalador embebido entrega información de aprovisionamiento al navegador.
Su acceso debe estar dentro del mismo perímetro que la administración. OIDC
permanece pospuesto; no se considera implementado por tener llamadas SSR.

## B. Actualización y recuperación

| ID | Estado en el código | Criterio pendiente de aceptación |
|---|---|---|
| B1 | `partitions.csv` contiene `factory` y `coredump`; no hay particiones OTA ni cliente de actualización ESP32. | Diseñar y probar actualización remota con rollback ante imagen no arrancable. El flasheo USB es el mecanismo actual. |
| B2 | No hay validación documentada de protección de secretos frente a extracción física. | Definir y verificar Secure Boot/cifrado de flash con una estrategia de recuperación compatible. |
| B3 | Hay fallback de perfil tras tres fallos de arranque y servicio USB si se detiene el arranque del agente. | Inyectar fallos de WiFi, XVF, audio y arranque; comprobar recuperación o estado administrable. Esto no equivale a rollback de firmware. |
| B4 | SNTP arranca y espera 3 s; no comprueba explícitamente sincronización antes de continuar. | Validar hora antes de TLS y probar arranque con NTP lento o inaccesible. |

La actualización I2C del **XVF** es distinta de OTA del **ESP32**. Una unidad que
trae la familia USB del firmware XVF puede necesitar conversión inicial por USB
DFU; ver [XVF3800.md](docs/XVF3800.md). En flota, RF-67 se descartó: USB es el
rescate acordado, no hay reset de fábrica físico pendiente dentro de esa spec.

## C. Diagnóstico sin cable

| ID | Estado en el código | Criterio pendiente de aceptación |
|---|---|---|
| C1 | Logs UDP syslog, causa de reset, resumen de core dump y comprobaciones de heap; métricas OTel del agente. | Reproducir un fallo sin USB y reconstruirlo desde los datos recibidos, incluyendo periodos de falta de conectividad. |
| C2 | Identidad MAC, salas únicas y `wake_id` en partes del recorrido. | Seguir una sesión/reunión de extremo a extremo con unidad, sala e ID, sin depender solo de proximidad temporal. |
| C3 | Compose de observabilidad y configuración de infraestructura disponibles. | Comprobar en el despliegue real ingesta, consultas y retención durante el periodo de uso continuado. |
| C4 | No se ha identificado un recorrido completo de marcador de incidencia. | Definir y comprobar una forma de marcar un fallo y encontrar sus datos asociados. |

El syslog UDP puede perder mensajes. La disponibilidad de un emisor no acredita
recepción, retención ni alertas. Los probes acústicos siguen siendo manuales.

## D. Grabaciones y almacenamiento

| ID | Estado en el código | Criterio pendiente de aceptación |
|---|---|---|
| D1 | Los WAV de diagnóstico están activados por defecto en Python y desactivados por el chart con `SEBASTIAN_RECORD=0`. Las reuniones tienen inicio explícito, retención de 90 días y opción de conservar. | Verificar la política efectiva de cada despliegue, limpieza de WAV/spool, indicadores de grabación, borrado y comportamiento con disco lleno. |
| D2 | Audio de reuniones en directorio/PVC del servidor; metadata en PostgreSQL. | Probar persistencia tras reinicio, copia/restauración conjunta y coherencia al fallar una escritura. |

La retención de `meetings` no gobierna los WAV de diagnóstico ni el catálogo
`recordings`. El volumen por defecto del chart es 5 GiB; la capacidad y las
reuniones marcadas para conservar deben formar parte de la prueba operativa.

## E. Aceptación del sistema completo

| ID | Estado en el código | Criterio pendiente de aceptación |
|---|---|---|
| E1 | Backend, imágenes, chart y configuración GitOps; #45 de release abierto en la revisión. | Identificar las versiones realmente desplegadas y completar conversación y administración desde una placa contra ese backend. |
| E2 | Reuniones A integrado; B–F en #50 abierto. | Validar captura, parada, recuperación, reproducción y transcripción, incluidas órdenes por voz en la placa. |
| E3 | Pruebas puntuales y registro de incidencias disponibles. | Una semana de uso sin USB con fallos explicables, métricas de uso y recuperación documentada. |

La matriz de [TESTING.md](TESTING.md) concreta las interrupciones y transiciones
que deben ejercitarse. Cada aceptación debe anotar commit, imagen, placa,
configuración, fecha y resultado.

## Orden de trabajo

1. Cerrar la aceptación de reuniones en placa y navegador.
2. Ensayar interrupciones de red, procesos y almacenamiento, y corregir los
   fallos con una regresión en la capa donde puedan reproducirse.
3. Completar los criterios de acceso, diagnóstico y recuperación que impidan
   operar de forma autónoma.
4. Registrar uso continuado y priorizar a partir de los fallos observados.

El endpoint permanente de [#44](https://github.com/Zetesis-Labs/Sebastian/pull/44)
es una propuesta en rama con su propia validación pendiente. No se usa como
supuesto para dar por cerrado ninguno de estos criterios.
