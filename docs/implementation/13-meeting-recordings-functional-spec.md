# Grabación de reuniones desde el altavoz — especificación funcional

> 2026-09-18. Especificación funcional de una capacidad nueva: usar el altavoz
> como grabadora de reuniones, con la grabación y su transcripción en el
> control room. Este documento dice **qué** hace el sistema y cómo lo ven las
> personas; el diseño técnico está en [14](14-meeting-recordings-technical-design.md). Estado:
> **pendiente de aprobación por Rubén**. Cada requisito lleva identificador
> (`RM-…`) y criterios de aceptación para que los tests verticales se escriban
> contra ellos antes de implementar. Decisiones de §11 tomadas. Se apoya en lo
> que ya existe:
> sesiones LiveKit del altavoz, catálogo de grabaciones (`recordings`) y la
> flota gobernada desde el control room
> ([12-fleet-adoption-functional-spec.md](12-fleet-adoption-functional-spec.md)).

> Seguimiento 2026-09-19: A integrado en #49; B–F implementados en #50 abierto.
> La prueba en placa de voz y la reproducción manual siguen pendientes.
> Consultar [STATUS](../STATUS.md) para evidencia y límites; esta nota no
> declara aceptados todos los requisitos ni modifica su aprobación funcional.

## 1. Propósito y alcance

Hoy el altavoz solo graba lo que ocurre en una conversación con el agente
(pistas micro, agente, compuesta y entrada al modelo). Queremos que **cualquier
persona en la sala pueda pedirle al altavoz que grabe una reunión**, que sea
evidente que está grabando, que se pueda parar con la misma facilidad, y que
al parar la grabación aparezca en el control room con su **transcripción por
hablantes**, lista para oír, leer, buscar y descargar.

Dentro del alcance: disparo y parada (gesto, dashboard, voz), comportamiento
del altavoz durante la grabación, almacenamiento, transcripción, vista en el
control room, retención y permisos. Fuera del alcance: §10.

## 2. Actores

- **Asistente a la reunión**: quien está en la sala. Empieza y para la
  grabación con el botón o con la voz. No necesita el dashboard.
- **Operador del control room**: administra los altavoces. Empieza y para
  grabaciones desde la ficha, y consulta, escucha, lee y descarga las
  grabaciones y sus transcripciones.
- **El agente (Sebastian)**: solo hace de mensajero de la orden de voz y
  confirma en voz alta. Nunca decide grabar por su cuenta.
- **El altavoz**: ejecuta la grabación y la señaliza con el anillo.

## 3. Conceptos

### 3.1 Grabación de reunión

Una grabación de reunión es un **tipo nuevo de grabación** en el catálogo
(`kind = meeting`), junto a las cuatro pistas de conversación existentes
(`microphone`, `agent`, `composite`, `model`). Tiene: audio (una pista, el
micro del altavoz), duración, hora de inicio y fin, altavoz de origen,
control room, quién la inició y cómo (gesto, dashboard, voz), transcripción
por hablantes y, opcionalmente, un resumen.

### 3.2 Sesión de reunión

Mientras graba, el altavoz mantiene una **sesión de tipo reunión**: publica su
micro y no reproduce nada. Es distinta de una conversación con el agente: sin
corte por silencio corto, sin turnos, sin voz de Sebastian, salvo la
confirmación de inicio y de parada.

### 3.3 Estados de la grabación

| Estado | Definición |
|--------|------------|
| **Pedida** | alguien la ha iniciado; el altavoz aún no ha confirmado (≤ 3 s por UDP, ≤ 30 s si solo hay poll) |
| **Grabando** | el altavoz publica audio y el control room lo está guardando; anillo rojo |
| **Cerrando** | se ha pedido parar; se cierra el fichero |
| **Transcribiendo** | el audio está guardado; la transcripción está en curso |
| **Lista** | audio y transcripción disponibles |
| **Sin transcripción** | audio guardado; la transcripción falló o se desactivó. Se puede reintentar |
| **Cortada** | el altavoz dejó de enviar audio (se apagó, se fue la red): la grabación queda hasta ese punto y pasa a transcribirse |

### 3.4 Solo en el perfil agente

La grabación usa la tubería de audio por red del perfil **agente**. En el
perfil **micro-usb** el altavoz es un micrófono del ordenador y la reunión la
graba el ordenador con la herramienta que quiera; este documento no aplica.
El control room lo dice claramente (RM-44).

## 4. Empezar a grabar

**RM-01 Tres formas equivalentes de empezar.** Una grabación se inicia desde
el altavoz (gesto), desde la ficha del altavoz en el dashboard, o por voz. Las
tres producen exactamente la misma grabación; solo cambia el campo "iniciada
por".

**RM-02 Gesto en el altavoz.** Una **pulsación corta seguida de una larga**
de MUTE (corta ≤ 0,5 s, larga ≥ 1,5 s, con menos de 1 s entre ambas) en un
altavoz en perfil agente y sin grabación en curso inicia la grabación. Una
pulsación corta sola sigue siendo el mute de siempre; una larga sola no hace
nada (así una pulsación torpe no dispara una grabación). La corta inicial no
cambia el estado de mute.

- *Aceptación:* corta + larga → el anillo pasa a rojo en menos de 1 s desde
  que se completa la larga y la ficha muestra "Grabando" en menos de 5 s.
  Corta sola → mute normal, sin grabación. Larga sola → nada.

**RM-03 Desde la ficha.** En la ficha de un altavoz adoptado aquí, en perfil
agente y contactando, un botón **Grabar reunión** inicia la grabación. Mientras
está *Pedida* el botón muestra "Pidiendo…"; si el altavoz no confirma en 30 s,
la ficha lo dice y ofrece reintentar.

**RM-04 Por voz.** "Sebastián, graba la reunión" (y variantes razonables:
"empieza a grabar", "graba esto") inicia la grabación si el altavoz está en
perfil agente. El agente entiende la orden, la traslada al control room y
**confirma en voz alta** ("Grabando. Para parar, di 'Sebastián, para la
grabación' o pulsa el botón: corta y luego larga"). Si no puede iniciarla, lo dice
("No puedo grabar ahora: …").

**RM-05 Una sola grabación por altavoz.** Si ya hay una en curso, un nuevo
intento no crea otra: el gesto la para (RM-20), la ficha ofrece *Parar*, y la
voz responde "Ya estoy grabando desde las HH:MM".

**RM-06 Inmediatez.** La orden desde la ficha o por voz llega al altavoz por el
canal de mando en red (el mismo canal firmado de la adopción), en menos de 3 s.
Si ese canal no alcanza al altavoz (otra subred), la orden viaja en su
siguiente poll (≤ 30 s) y la ficha lo avisa ("llegará en el próximo contacto").

**RM-07 Autorización.** Solo el control room que tiene adoptado el altavoz
puede mandarle grabar; la orden va firmada como la adopción. El gesto físico
siempre vale (quien está en la sala manda).

## 5. Mientras graba

**RM-10 Señal inequívoca.** Mientras graba, el anillo está en **rojo fijo**.
Ningún otro estado usa rojo fijo. Al parar, el anillo vuelve a su estado
normal en menos de 1 s. Un altavoz que se reinicia en mitad de una grabación
arranca **sin** grabar y sin rojo (no se retoma sola; RM-23).

**RM-11 Audio.** Se graba una pista mono del micro del altavoz con el
procesado del XVF3800 y **haz automático** (no fijo: varias personas hablan
desde sitios distintos). Formato de almacenamiento: **Ogg/Opus mono, 48 kbit/s**
(≈ 20 MB por hora). Es lo que LiveKit ya produce al volcar una pista, así que
no hay transcodificación; el navegador lo reproduce, el proveedor de
transcripción lo acepta tal cual, y una hora pesa lo que un minuto de WAV.

**RM-12 Sin agente en medio.** Durante la grabación el altavoz no reproduce
nada y el agente no conversa. El agente permanece a la escucha **solo** de la
palabra de activación para poder parar por voz (RM-21).

**RM-13 Mute durante la grabación.** Una pulsación corta de MUTE silencia el
micro también en la grabación (se graba silencio) y el anillo lo indica
(rojo con el patrón de mute). La grabación no se para.

**RM-14 Duración visible.** La ficha muestra "Grabando desde HH:MM (mm:ss)" en
vivo, y el listado de grabaciones muestra la que está en curso con ese estado.

**RM-15 Guardado continuo.** El audio se guarda según llega, no al final: si
el altavoz o la red caen, lo grabado hasta ese momento no se pierde (RM-24).

## 6. Parar y redes de seguridad

**RM-20 Parar con el gesto.** El mismo gesto (corta + larga) con grabación en
curso la para. El anillo deja el rojo en menos de 1 s.

**RM-21 Parar por voz.** "Sebastián, para la grabación" (y variantes: "deja de
grabar", "termina la grabación") la para; el agente confirma ("Grabación
guardada, HH minutos"). Solo esta orden y las de inicio se atienden durante la
grabación; cualquier otra petición recibe "Estoy grabando; dime que pare si
quieres hablar".

**RM-22 Parar desde la ficha.** Botón **Parar** en la ficha y en la fila de la
grabación en curso.

**RM-23 Corte automático por silencio.** Si no hay voz durante N minutos
(por defecto 10, configurable por altavoz desde la ficha, 5–60), la grabación
se para sola y queda marcada "parada por silencio". El anillo avisa 30 s antes
(rojo parpadeando lento).

**RM-24 Máximo de duración.** Una grabación nunca supera M horas (por defecto
3, configurable 1–8). Al llegar, se para y queda marcada "parada por límite".

**RM-25 Si se pierde el altavoz.** Si deja de llegar audio durante más de 30 s
(se apagó, se fue la red), la grabación se cierra como *Cortada* con lo que
había, y pasa a transcribirse. Cuando el altavoz vuelve, no retoma: arranca
limpio (RM-10).

**RM-26 Si se pierde el control room.** Si el altavoz no puede entregar audio
(el control room no responde), lo señaliza (rojo parpadeando rápido) y para a
los 30 s. No guarda audio en la placa: no hay sitio ni sentido.

## 7. Transcripción

**RM-30 Automática.** Al cerrar una grabación se transcribe sola, en segundo
plano, en el idioma detectado (español por defecto).

**RM-31 Por hablantes.** La transcripción distingue hablantes (Hablante 1,
Hablante 2…), con marcas de tiempo por intervención. El operador puede
renombrar a los hablantes en el control room (Hablante 1 → "Ana") y el nombre
se aplica a toda la transcripción.

**RM-32 Resumen.** Tras la transcripción se genera un resumen breve y una
lista de acuerdos y acciones, en el idioma de la reunión, con un modelo de
OpenAI de la gama económica (el "mini" vigente cuando se implemente; lo fija
el diseño). Activado por defecto; se puede desactivar por control room. Se
marca como generado automáticamente y se puede regenerar.

**RM-33 Fallo visible y reintentable.** Si la transcripción falla, la grabación
queda *Sin transcripción* con el motivo, y un botón **Transcribir de nuevo**.
El audio nunca se pierde por un fallo de transcripción.

**RM-34 Proveedor.** Transcripción y resumen con **OpenAI**, la cuenta y la
clave que ya usa el agente. Transcripción con el modelo de transcripción con
diarización de OpenAI (el vigente cuando se implemente; lo fija el diseño),
con `whisper-1` como reserva sin hablantes si aquel no está disponible. La
opción autoalojada (Whisper en cortes) queda fuera de esta versión.

**RM-35 Privacidad del proceso.** El audio solo sale hacia el proveedor de
transcripción configurado; no se usa para nada más y no se conserva allí (se
usa la modalidad sin retención del proveedor cuando exista).

## 8. En el control room

**RM-40 Listado.** En *Grabaciones*, las reuniones se ven junto a las
conversaciones con un filtro por tipo. Cada fila: altavoz, fecha y hora,
duración, estado (§3.3), quién la inició y cómo, y si tiene transcripción.

**RM-41 Detalle.** Al abrir una reunión: reproductor, transcripción por
hablantes **sincronizada** con el audio (pulsar una intervención salta a ese
punto; el texto se resalta al reproducir), resumen si lo hay, y botones
**Descargar audio**, **Descargar transcripción** (texto y SRT) y **Borrar**.

**RM-42 Búsqueda.** Un buscador sobre el texto de las transcripciones devuelve
las reuniones y las intervenciones que contienen la frase, y abre el detalle
en ese punto.

**RM-43 Ficha del altavoz.** La ficha muestra el estado de grabación (RM-14),
los botones *Grabar reunión* / *Parar*, la configuración de corte por silencio
y máximo (RM-23, RM-24) y las últimas reuniones grabadas con ese altavoz.

**RM-44 Perfil micro-usb.** En un altavoz en perfil micro-usb, la ficha no
ofrece grabar y explica: "En perfil micro USB el altavoz es un micrófono del
ordenador; graba la reunión desde el ordenador o cambia el perfil a agente".

**RM-45 Retención.** El control room tiene una retención configurable
(por defecto 90 días): las reuniones más antiguas se borran solas, audio y
transcripción. Se puede marcar una reunión como "conservar" para excluirla.

**RM-46 Borrado.** Borrar una reunión elimina audio y transcripción de forma
irreversible tras confirmar. Queda constancia en el historial de sesiones de
que existió (fecha, duración), sin contenido.

**RM-47 Permisos.** Ver, escuchar, descargar y borrar requiere la sesión de
administración del dashboard, como el resto del control room. No hay enlaces
públicos.

## 9. Comportamiento del altavoz (resumen)

**RM-50** Corta + larga de MUTE alterna grabar/parar (RM-02, RM-20); corta
sola = mute (RM-13); larga sola = nada.
**RM-51** Anillo: rojo fijo grabando; rojo parpadeo lento = va a cortar por
silencio; rojo parpadeo rápido = sin control room; nunca rojo fuera de una
grabación.
**RM-52** Acepta las órdenes *record on/off* por el canal firmado en red y
por el poll; las confirma al control room.
**RM-53** Arranca siempre sin grabar.
**RM-54** En perfil micro-usb ignora las órdenes de grabar y responde
"perfil sin grabación".

## 10. Fuera de alcance

- Grabar desde el perfil micro-usb (lo hace el ordenador).
- Varias pistas (una por hablante) o varios altavoces en la misma reunión.
- Grabación en la placa sin red.
- Compartir grabaciones fuera del control room (enlaces, correo).
- Traducción de la transcripción.

## 11. Decisiones tomadas (2026-09-18)

1. **Proveedor de transcripción y resumen** (RM-32, RM-34): OpenAI, con la
   cuenta del agente. El autoalojado queda fuera de esta versión.
2. **Gesto** (RM-02): corta + larga de MUTE.
3. **Resumen** (RM-32): activado por defecto, desactivable por control room.
4. **Formato del audio** (RM-11): Ogg/Opus mono 48 kbit/s, sin transcodificar.

## 12. Factibilidad

Alta, porque casi todo se apoya en piezas que ya existen y funcionan en
hardware:

| Pieza | Base existente | Trabajo nuevo |
|-------|----------------|---------------|
| Orden en red firmada | canal UDP de adopción (`adopt.c`, `adoption.Client`) | mensaje `record`, confirmación |
| Sesión de audio del altavoz | sesión LiveKit del perfil agente | variante *reunión*: sin reproducción, sin corte corto, haz automático |
| Guardado del audio | catálogo `recordings` + object storage | Egress de LiveKit (o el agente escribiendo) con guardado continuo; `kind=meeting` |
| Anillo y botón | `xvf_ui.zig` (mute, ámbar de consentimiento) | rojo y sus patrones; pulsación larga |
| Voz | agente LiveKit con herramientas | dos intenciones (grabar / parar) y el modo "solo palabra de activación" |
| Transcripción | Whisper ya usado para verificar la palabra de activación | job por lotes con diarización, tabla de transcripción, reintentos |
| Dashboard | página de grabaciones, ficha del altavoz | filtro por tipo, detalle con transcripción sincronizada, búsqueda, botones |

Riesgos reales: (1) el **modo "solo palabra de activación" del agente** durante
la grabación es lo más delicado (el agente debe callar y no reaccionar a nada
más); si se complica, la primera versión puede prescindir del parado por voz y
dejar gesto + ficha. (2) **RAM interna** del altavoz: la sesión de reunión es
más ligera que una conversación (sin reproducción), así que no debería
empeorar; se mide. (3) **Diarización**: la calidad depende del proveedor; con
un solo micro y varias personas cerca, esperar errores de atribución que el
operador corrige renombrando.

Estimación: firmware 1 día (orden, sesión reunión, anillo, botón), server
1 día (orden, egress, catálogo, transcripción, retención), agente ½ día,
dashboard 1 día. **Tres días y medio**, más pruebas en la sala.
