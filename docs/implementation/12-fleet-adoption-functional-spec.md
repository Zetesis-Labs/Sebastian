# Provisioning y adopción de altavoces desde el control room — especificación funcional

> 2026-09-18. Especificación funcional del diseño
> [11-fleet-adoption-control-room.md](11-fleet-adoption-control-room.md). Este
> documento dice **qué** hace el sistema y cómo lo ve el operador; el diseño
> dice cómo se construye. Estado: **pendiente de aprobación por Rubén**. Cada
> requisito lleva identificador (`RF-…`) y criterios de aceptación para que
> los tests verticales se escriban contra ellos antes de implementar.

## 1. Propósito y alcance

Hoy un altavoz solo existe para el control room al que se le grabó por USB, y
solo mientras ese control room esté vivo. Queremos que **cualquier control room
vea todos los altavoces a su alcance**, con su estado, que pueda **adoptar** los
que le corresponden y que **gobierne su configuración** sin volver a tocar el
USB. El USB queda para el primer contacto y para el rescate.

Dentro del alcance: instalador embebido en el control room, inventario y vista
de red, adopción (descubierta o por IP), secretos, configuración deseada y el
comportamiento del altavoz que lo soporta. Fuera del alcance: §10.

## 2. Actores

- **Operador**: persona con acceso al dashboard de un control room. Hoy no hay
  usuarios ni roles; el acceso al dashboard es el permiso.
- **Control room**: servidor Sebastian más su dashboard. Puede haber varios en
  la misma red y en redes distintas.
- **Altavoz**: unidad ReSpeaker XVF3800 + XIAO ESP32-S3 con el firmware
  Sebastian. Se identifica por su MAC WiFi (`68ee8f4d8dd4`).
- **Tercero**: alguien en la misma WiFi con su propio control room. No debe
  poder adoptar altavoces que no son suyos.

## 3. Conceptos

### 3.1 Vinculación

Un altavoz está **vinculado** a un control room cuando su configuración apunta
a él (URL del token server). Solo puede estar vinculado a uno. Un altavoz sin
control room está **sin adoptar**.

### 3.2 Estados de un altavoz vistos desde un control room

| Estado | Definición |
|--------|------------|
| **Adoptado aquí** | vinculado a este control room y ha contactado en los últimos 90 s |
| **Adoptado aquí, ausente** | vinculado a este control room, sin contacto en más de 90 s y no visto en la red |
| **Gestionado por *X*** | visto en la red, vinculado a otro control room *X* |
| **Sin adoptar** | visto en la red, sin control room |
| **Huérfano** | visto en la red, vinculado a un control room al que no consigue llegar |

Las cuatro últimas solo existen si el altavoz se ve en la red (§6). Un
altavoz de otra subred que no se descubre simplemente no aparece; se puede
adoptar por IP igualmente (RF-31).

### 3.3 Secretos

| Secreto | Alcance | Quién lo pone | Para qué |
|---------|---------|---------------|----------|
| **De organización** | común a todos los control rooms de la organización | el operador, en la primera provisión; o el primer control room que adopta una unidad de fábrica | autoriza adoptar |
| **De altavoz** | uno por unidad | lo genera el control room al adoptar | autentica al altavoz en su día a día; permite ceder esa unidad sin dar el de organización |

**Regla de adopción:** un control room puede adoptar un altavoz si conoce el
secreto de organización **o** el secreto de ese altavoz. No hace falta ningún
gesto físico. La única excepción es la unidad de fábrica (sin secreto de
organización): exige consentimiento físico (§7, RF-34).

### 3.4 Configuración

Un solo formato (`sebastian.config.v1`) para todo: USB, instalador web,
adopción y control room. Campos y si son gobernables desde el control room:

| Campo | Gobernable desde el control room | Notas |
|-------|----------------------------------|-------|
| WiFi (SSID, contraseña, oculta) | sí, con vuelta atrás (RF-45) | la contraseña nunca se relee |
| Control room (URL del token server) | solo mediante adopción | cambiarlo *es* adoptar |
| Syslog (IP, puerto) | sí | |
| Modo half-duplex / full-duplex | sí | reinicia |
| Beam fijo y azimut | sí | reinicia |
| Perfil activo (agente / micro USB) | sí | ya existe hoy |
| Tiempos de sesión (silencio, nivel de voz) | sí | hoy son de compilación; pasan a configuración |
| Canal del micro (izquierdo / derecho) | **no** | requiere reflashear; el dashboard lo muestra como solo lectura |
| Autotests de arranque | **no** | requieren reflashear |

## 4. Instalador embebido en el control room

**RF-01 Dos instaladores, una app.** El instalador web existe en GitHub Pages
(público, sin control room, formulario en blanco) y dentro de cada dashboard en
`/installer`. Es la misma aplicación; cualquier cambio aparece en los dos.

**RF-02 Prerrelleno.** Al abrir el instalador desde un dashboard, el formulario
viene relleno con los datos de ese control room: URL del token server, IP y
puerto del syslog, y el secreto de organización (oculto, como una contraseña).
El operador solo tiene que introducir la WiFi.

- *Aceptación:* abrir `/installer` en un control room recién desplegado muestra
  su URL y su syslog sin teclear nada; el JSON que se envía los contiene.

**RF-03 Un altavoz provisionado desde el instalador embebido nace adoptado.** Al
primer contacto aparece como "Adoptado aquí", con secreto de altavoz emitido en
ese primer contacto (§8). Mecanismo: la unidad lleva el secreto de organización
y ningún secreto de altavoz; antes de su primer poll pide un reto a
`GET /v1/devices/{id}/enroll`, lo firma (HMAC del nonce con el secreto de
organización, como la adopción en red) y `POST …/enroll` le devuelve su
secreto de altavoz, que guarda en NVS. Si el control room no tiene secreto de
organización o la prueba falla, la unidad reintenta en cada poll y mientras
tanto aparece como "Registrado · sin adoptar". El secreto emitido así no se
muestra en el dashboard (va directo a la unidad); si el operador lo necesita,
lo regenera desde la ficha (RF-53).

**RF-04 Firmware compatible.** El botón de flashear del instalador embebido
instala la versión de firmware empaquetada con ese control room, no la última
publicada en GitHub Pages. La versión se muestra junto al botón.

**RF-05 Contexto seguro.** Web Serial solo funciona en `localhost` o HTTPS. Si
el control room se sirve por HTTP, el instalador no oculta el botón: explica que
hay que añadir ese origen en
`chrome://flags/#unsafely-treat-insecure-origin-as-secure` en ese navegador, con
el origen ya escrito para copiar.

**RF-06 Cargar desde el dispositivo** (ya implementado el 2026-09-18) sigue
igual en ambos instaladores: el puerto se mantiene 120 s, la contraseña WiFi
sale bloqueada y cambiarla es una acción explícita.

## 5. Inventario y ficha del altavoz

**RF-10 Lista.** La página *Devices* muestra en una sola lista todos los
altavoces conocidos (tabla del control room) y todos los vistos en la red,
con su estado (§3.2), nombre, MAC, versión de firmware, perfil en ejecución y
última vez visto. Orden: adoptados aquí primero, luego huérfanos, sin adoptar,
gestionados por otros, ausentes.

**RF-11 Ficha.** Al entrar en un altavoz adoptado aquí se ve: identidad
(MAC, nombre editable), versión de firmware, IP actual, estado y último
contacto; configuración **en ejecución** frente a **deseada** (§7); sesiones y
grabaciones de esa unidad; y las acciones: editar configuración, cambiar
perfil, ver o regenerar el secreto de altavoz, olvidar.

- *Aceptación:* tras una conversación con la unidad `68ee…`, su ficha lista esa
  sesión; la ficha de `e072…` no.

**RF-12 Sesiones atribuidas a la unidad real.** Las sesiones y grabaciones
cuelgan del altavoz que las produjo, no del identificador legado
`esp32-respeaker`. Ese identificador desaparece del inventario cuando ninguna
unidad lo usa.

**RF-13 Un altavoz gestionado por otro control room es de solo lectura**: se ve
su estado y el nombre del otro control room; la única acción es *Adoptar*.

## 6. Descubrimiento en la red

**RF-20 Anuncio.** Todo altavoz encendido y con WiFi se anuncia en su red
local de forma continua, esté o no adoptado, con: MAC, versión de firmware,
perfil, control room al que está vinculado (o ninguno) y el resultado de su
último intento de contacto con él.

**RF-21 Vista en red.** Cada control room escucha esos anuncios y los cruza con
su inventario para producir los estados de §3.2. Un altavoz deja de verse en la
red a los 60 s sin anuncio.

**RF-22 Huérfano.** Un altavoz se muestra huérfano cuando su propio anuncio dice
que su último contacto con su control room ha fallado. El control room que lo
ve muestra el motivo tal como lo reporta el altavoz (sin DNS, sin respuesta,
error HTTP…).

**RF-23 Límite de subred, explícito.** El descubrimiento solo alcanza la subred
del control room. La página lo dice ("solo se descubren altavoces de la red
10.0.0.0/24; para otras redes, *Adoptar por IP*"). Un reflector mDNS en el
router amplía la vista; no es requisito.

**RF-24 Sin descubrimiento no se pierde nada.** Si el control room no puede
escuchar la red (por ejemplo en Kubernetes sin red del host), la lista muestra
solo su inventario y un aviso; todo lo demás funciona.

## 7. Adopción

**RF-30 Adoptar un altavoz descubierto.** En un altavoz *Sin adoptar*,
*Huérfano* o *Gestionado por X*, el botón *Adoptar* le envía la configuración
de este control room (URL, syslog, secretos) y opcionalmente cambia otros
campos que el operador edite en el mismo diálogo. La WiFi no se cambia al
adoptar salvo que el operador lo pida expresamente.

- *Aceptación:* unidad vinculada al control room A; desde B, *Adoptar* →
  en menos de 30 s la unidad aparece en B como "Adoptado aquí" y en A como
  "Gestionado por B".

**RF-31 Adoptar por IP.** Un botón *Adoptar por IP* pide la IP del altavoz y
hace lo mismo sin necesidad de haberlo descubierto. Si la IP no responde en 5 s,
lo dice. Si responde, provisiona igual.

**RF-32 Autorización.** El altavoz solo acepta la adopción si el mensaje está
firmado con el secreto de organización que ya tiene o con su propio secreto de
altavoz. Si no, la rechaza, no cambia nada y lo anuncia (RF-36).

**RF-33 Sin gesto físico** para unidades que ya llevan secreto de organización,
aunque estén vinculadas a otro control room sano. Conocer el secreto es el
permiso.

**RF-34 Unidad de fábrica.** Un altavoz sin secreto de organización acepta la
primera adopción solo con consentimiento físico: tras recibir la petición, el
anillo parpadea en ámbar durante 30 s y hay que pulsar MUTE en ese tiempo. Si
no se pulsa, la adopción caduca y el dashboard lo indica. La adopción exitosa
graba el secreto de organización del control room adoptante.

**RF-35 Resultado visible.** El dashboard muestra el resultado de cada intento
de adopción: aceptada (y la unidad reiniciando), rechazada por secreto, sin
respuesta, esperando el botón, caducada.

**RF-36 Intento denegado visible para el dueño.** Cuando un altavoz rechaza una
adopción, su siguiente anuncio lo dice, y el control room que lo gestiona lo
muestra en su ficha ("intento de adopción rechazado desde 10.0.0.77 a las
12:31"). Un tercero no puede adoptar y el dueño se entera de que lo intentó.

**RF-37 Olvidar.** *Olvidar* en un altavoz adoptado aquí lo devuelve al estado
*Sin adoptar*: borra el control room y los dos secretos en la unidad, conserva
la WiFi, y lo elimina del inventario conservando su historial de sesiones. Pide
confirmación escribiendo la MAC.

**RF-38 Ceder una unidad.** Para entregar un altavoz a alguien de fuera sin
darle el secreto de organización: el dueño le comunica el secreto de altavoz
(RF-52); el receptor lo adopta por IP con ese secreto; al adoptar, el nuevo
control room emite un secreto de altavoz nuevo y graba su propio secreto de
organización, con lo que el antiguo dueño pierde ambos.

**RF-39 Sin reprovisionar por sorpresa.** Toda adopción reinicia el altavoz. Si
tiene una conversación en curso, la adopción espera a que termine (máximo
2 min) y después aplica y reinicia.

## 8. Secretos

**RF-50 Secreto de organización.** Se configura en cada control room
(variable de entorno; en producción, desde Infisical). El instalador embebido lo
prerrellena; el instalador de GitHub Pages lo pide como campo opcional.

**RF-51 Secreto de altavoz emitido al adoptar.** Al adoptar (o al primer contacto
de una unidad provisionada desde el instalador embebido, RF-03), el control
room genera un secreto por unidad y se lo entrega en el mismo mensaje. La
unidad lo usa desde entonces para abrir sesiones.

**RF-52 Ver una vez.** El secreto de altavoz se muestra una sola vez en el
dashboard, en el momento de emitirlo o de regenerarlo, con botón de copiar y
código QR. Después no se puede volver a ver, solo regenerar.

**RF-53 Regenerar.** *Regenerar secreto* en la ficha emite uno nuevo y lo
entrega a la unidad en su siguiente poll; el anterior deja de valer cuando la
unidad confirma el nuevo.

**RF-54 Nunca en el navegador.** El secreto de organización no viaja al
JavaScript del dashboard ni aparece en respuestas de la API sin sesión de
administración. Ningún secreto aparece en logs ni en syslog.

## 9. Configuración deseada desde el control room

**RF-40 Editar.** La ficha permite editar la configuración gobernable (§3.4) en
un formulario idéntico al del instalador (mismos campos, misma validación). Al
guardar, la configuración pasa a ser la **deseada**.

**RF-41 Aplicar.** El altavoz recoge la configuración deseada en su siguiente
poll (30 s como máximo), la guarda y se reinicia. Mientras tanto la ficha
muestra "aplicando…". Cuando el altavoz vuelve y reporta la configuración en
ejecución igual a la deseada, la ficha muestra "sincronizado".

- *Aceptación:* cambiar de half-duplex a full-duplex en la ficha → en menos
  de 60 s el altavoz reporta full-duplex y el log de arranque lo confirma.

**RF-42 Real frente a deseada.** La ficha muestra siempre las dos columnas y
resalta las diferencias. Si el altavoz no aplica en 3 polls, la ficha lo marca
como "no aplicada" con el motivo reportado por la unidad.

**RF-43 Campos que exigen reflashear** se muestran en la ficha con su valor
actual, deshabilitados y con la explicación ("canal del micro: requiere
reflashear").

**RF-44 Nada se pierde en el reinicio.** Los campos que no se han editado se
conservan; una configuración deseada nunca contiene la contraseña WiFi salvo
que el operador la haya cambiado en esa edición.

**RF-45 WiFi con vuelta atrás.** Si la configuración deseada cambia la WiFi y
el altavoz no obtiene IP en 2 min en la nueva red, restaura la anterior, vuelve
a contactar y reporta "WiFi rechazada, restaurada la anterior". La ficha lo
muestra y deja la deseada marcada como fallida hasta que el operador la edite.

**RF-46 Perfil.** El cambio de perfil (agente / micro USB) sigue funcionando
como hoy y se integra en la misma vista real/deseada.

## 10. Comportamiento del altavoz (requisitos del firmware)

**RF-60** Se anuncia en la red desde que tiene IP hasta que se apaga, en todos
los perfiles, incluido micro USB.

**RF-61** Hace su poll al control room cada 30 s con: perfil en ejecución, hash
de la configuración en ejecución y versión de firmware.

**RF-62** Registra el resultado del último poll y lo incluye en su anuncio.

**RF-63** Escucha peticiones de adopción en la red y las procesa según §7; una
petición mal firmada no produce ningún cambio ni reinicio.

**RF-64** Indica con el anillo: ámbar parpadeando = esperando consentimiento
físico (RF-34); ámbar fijo 2 s = adopción aceptada, va a reiniciar.

**RF-65** Todo cambio de configuración se aplica reiniciando. Nunca se reinicia
con una conversación en curso salvo que hayan pasado 2 min desde la orden.

**RF-66** El USB sigue funcionando como hoy: ventana de 5 s al arrancar,
120 s tras una carga desde el instalador, provisioning completo por serie
incluidos ambos secretos.

**RF-67** Reset de fábrica físico: MUTE mantenido 10 s durante el arranque
borra control room y secretos, conserva la WiFi, y el anillo lo confirma en
rojo. (Pendiente de confirmar que MUTE es legible por el ESP32 en el
arranque; si no, la vía de rescate es el USB.)

## 11. Errores y mensajes al operador

| Situación | Mensaje |
|-----------|---------|
| Adoptar por IP sin respuesta | "10.0.0.77 no responde. ¿Está encendido y en una red alcanzable desde este control room?" |
| Adopción rechazada | "El altavoz ha rechazado la adopción: el secreto no coincide. Necesitas el secreto de organización de su dueño o el secreto de ese altavoz." |
| Esperando botón | "Pulsa MUTE en el altavoz antes de 30 s. El anillo parpadea en ámbar." |
| Caducada | "No se pulsó MUTE a tiempo. Vuelve a adoptar cuando tengas el altavoz a mano." |
| Adoptar con conversación en curso | "Adopción en cola: el altavoz está en una conversación. Se aplicará al terminar (máximo 2 min)." |
| WiFi restaurada | "El altavoz no pudo conectar a *Casa-5G* y ha vuelto a *Pizarro*. Revisa la contraseña o si la red es 2,4 GHz." |
| Sin descubrimiento | "Este control room no puede escuchar la red local; solo se muestra el inventario. Usa *Adoptar por IP*." |
| Instalador en HTTP | "Web Serial requiere HTTPS o localhost. Para usarlo aquí, añade `http://10.0.0.188:3001` en chrome://flags/#unsafely-treat-insecure-origin-as-secure." |

## 12. Requisitos no funcionales

- **Seguridad.** Ningún mensaje de adopción sin firma válida cambia nada.
  Nonce de un solo uso, caducidad 30 s. Secretos nunca en logs, syslog ni
  JavaScript del navegador. Un tercero en la misma WiFi con su propio control
  room ve los altavoces (anuncio público) pero no puede adoptarlos ni cambiar
  nada.
- **Memoria del altavoz.** Todo lo nuevo en el firmware se reserva en PSRAM;
  el consumo de SRAM interna en sesión no crece más de 2 KB respecto a hoy
  (medido con `heap[post-connect]`).
- **Tiempos.** Adopción visible en el dashboard en < 30 s; configuración
  aplicada en < 60 s; altavoz visto en red en < 10 s desde que tiene IP.
- **Compatibilidad.** Un altavoz con firmware anterior sigue funcionando contra
  un control room nuevo (poll y `/token` legado) hasta que se actualice; un
  control room antiguo ignora los campos nuevos del poll.
- **Observabilidad.** Cada adopción, olvido, regeneración de secreto y cambio
  de configuración deseada genera un evento de dominio (outbox) y una línea de
  log en el altavoz.

## 13. Fuera de alcance (por ahora)

- Usuarios, roles y permisos en el dashboard (el acceso al dashboard es el
  permiso).
- Actualización de firmware del altavoz desde el control room (OTA). Se
  reflashea por USB / instalador.
- Federación entre control rooms (que se vean entre sí y compartan inventario).
- Reflector mDNS en la infraestructura de red.
- Cambiar el canal del micro o los autotests sin reflashear.

## 14. Trazabilidad con el diseño

| Requisitos | Bloque del diseño |
|------------|-------------------|
| RF-01…06 | 0 — instalador en el dashboard |
| RF-12, RF-51, RF-53 | 1 — secreto por altavoz y `/v1/sessions` |
| RF-10, RF-13, RF-20…24, RF-60…62 | 2 — anuncio y vista en red |
| RF-30…39, RF-50, RF-52, RF-54, RF-63, RF-64, RF-67 | 3 — adopción |
| RF-11, RF-40…46, RF-65 | 4 — configuración deseada |
