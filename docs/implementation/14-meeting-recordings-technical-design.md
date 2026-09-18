# Grabación de reuniones — diseño técnico

> 2026-09-18. Diseño técnico de
> [13-meeting-recordings-functional-spec.md](13-meeting-recordings-functional-spec.md).
> Dice **cómo** se construye cada requisito `RM-xx`, en qué bloque vertical
> cae y qué tests se escriben antes del código. Se apoya en lo que hoy existe
> y funciona en hardware: sesiones LiveKit por unidad (`POST /v1/sessions`),
> el canal UDP firmado de adopción (`adopt.c`, `adoption.Client`), el poll de
> configuración deseada, el catálogo `recordings`, el outbox y la ficha del
> altavoz.

## 0. Hechos que condicionan el diseño (verificados hoy)

| Hecho | Consecuencia |
|-------|--------------|
| El XVF3800 expone el estado crudo del botón: `GPI_READ_VALUES` (resid 36, cmd 0, 3 bytes). El propio XVF conmuta el mute (GPO 30) en cada pulsación. | La pulsación larga (RM-02) se mide leyendo el GPI desde la tarea del anillo (80 ms). Tras el gesto, el firmware restaura el mute que había (escritura GPO 30). **Spike 0**: confirmar en placa qué bit del GPI es el botón y su nivel activo. |
| El catálogo `recordings` existe (kind, object_url, transcript) pero **nadie lo alimenta**: el agente guarda WAV en su disco local y no registra nada. No hay object storage en Sebastian. | El audio de reunión lo custodia el **server** en su volumen: el agente se lo envía en streaming. Nueva PVC del server en el chart. |
| OpenAI: `gpt-4o-transcribe-diarize` con `response_format=diarized_json` devuelve segmentos `{start, end, speaker, text}`; `chunking_strategy=auto`; `known_speaker_names/references` permiten fijar hablantes. Tope **25 MB por petición**; formatos `mp3 mp4 mpeg mpga m4a wav webm` (Ogg no figura). | El fichero se guarda en Ogg/Opus (decisión §11 de la spec) y el job de transcripción lo **remultiplexa a WebM sin recodificar** (`ffmpeg -c copy`), troceado en piezas de ≤ 20 MB (≈ 55 min a 48 kbit/s). Los hablantes se mantienen entre piezas pasando como referencia unos segundos de cada hablante de la pieza anterior. |
| El agente recibe el micro como `AudioFrame` PCM (no paquetes Opus). | El agente codifica con `ffmpeg` (subproceso, `libopus 48k mono`) y escribe la salida por streaming al server. `ffmpeg` entra en la imagen del agente y en la del server. |
| La sesión LiveKit la abre siempre la placa (`token.zig` → `POST /v1/sessions`), y el server despacha al agente con metadatos. | La sesión de reunión es una sesión normal con `kind=meeting` en la petición y en los metadatos del despacho; el agente entra en **modo reunión** por esos metadatos. |
| El canal UDP de adopción ya tiene nonce + HMAC con el secreto de organización o el de altavoz; el server solo guarda el **hash** del secreto de altavoz. | Las órdenes en red se firman con el **secreto de organización** (RM-07). Sin él configurado, la orden va solo por el poll. |

## 1. Arquitectura

```
 asistente ──gesto──▶ placa ──────────────┐
 operador ──ficha──▶ server ──UDP cmd/poll▶ placa ──POST /v1/sessions{kind:meeting}──▶ server
 asistente ──voz───▶ agente ──POST /v1/admin/meetings/{id}/stop ▶ server            │
                                                                                     ▼
                     agente ◀── dispatch{mode:meeting, meetingId} ── LiveKit ◀── token
                       │ micro (PCM) → ffmpeg → Ogg/Opus
                       └──PUT /v1/meetings/{id}/audio (chunked, streaming)──▶ server ──▶ volumen
                                                                                     │
                     job transcribe (server): ffmpeg -c copy → webm ≤20 MB → OpenAI ──┘
                     job summary (server): chat completions (modelo mini)
                     retención (server): borra audio+transcripción > N días
 dashboard ──/v1/admin/meetings… (admin)──▶ server ; audio por proxy del dashboard (RM-47)
```

Principios: la **placa** solo sabe "grabando sí/no" y lo señaliza; el
**agente** solo captura y entrega; el **server** es el dueño del estado, del
fichero y de los jobs; el **dashboard** es vista. Toda decisión de estado
(§3.3 de la spec) vive en funciones puras del server (`meeting_core.go`) con
tests de tabla, y la cáscara (HTTP, ficheros, OpenAI, cron) alrededor.

## 2. Modelo de datos (server, Postgres)

Nueva tabla `meetings`, separada de `recordings` porque su ciclo de vida es
distinto (una `recording` es un fichero cerrado; una reunión pasa por seis
estados y tiene transcripción estructurada). `recordings` gana `kind=meeting`
solo como vista unificada (RM-40), sin duplicar datos: la fila de `recordings`
se crea al cerrar la reunión y apunta al mismo fichero.

```sql
CREATE TABLE meetings (
  id uuid PRIMARY KEY,
  device_id varchar NOT NULL REFERENCES devices(id),
  session_id uuid REFERENCES sessions(id),          -- la sesión LiveKit que la sirvió
  state varchar NOT NULL,                            -- requested|recording|closing|transcribing|ready|no_transcript|cut
  requested_by varchar NOT NULL,                     -- gesture|dashboard|voice
  requested_at timestamptz NOT NULL,
  started_at timestamptz,                            -- la placa confirmó
  ended_at timestamptz,
  end_reason varchar,                                -- gesture|dashboard|voice|silence|max_duration|device_lost|room_lost
  audio_path varchar,                                -- relativo al volumen del server
  audio_bytes bigint NOT NULL DEFAULT 0,
  duration_ms bigint NOT NULL DEFAULT 0,
  transcript jsonb,                                  -- {language, segments:[{start,end,speaker,text}], speakers:{"A":"Ana"}}
  transcript_error varchar,
  summary jsonb,                                     -- {text, agreements:[], actions:[], model, generated_at}
  keep boolean NOT NULL DEFAULT false,               -- RM-45 "conservar"
  deleted_at timestamptz,                            -- RM-46: queda la constancia sin contenido
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX meetings_device_started ON meetings (device_id, started_at DESC);
CREATE INDEX meetings_state ON meetings (state) WHERE state IN ('requested','recording','closing','transcribing');
CREATE INDEX meetings_transcript_fts ON meetings USING gin (to_tsvector('spanish', coalesce(transcript->>'text','')));
```

`devices` gana `meeting_silence_min int DEFAULT 10` y `meeting_max_hours int
DEFAULT 3` (RM-23/24, configurables desde la ficha). El control room gana
`meeting_summary_enabled` y `meeting_retention_days` (config del server, no
tabla: `SEBASTIAN_MEETING_SUMMARY=true`, `SEBASTIAN_MEETING_RETENTION_DAYS=90`).

Eventos de dominio (outbox): `meeting.requested`, `meeting.started`,
`meeting.ended`, `meeting.transcribed`, `meeting.deleted`.

### 2.1 Máquina de estados (`meeting_core.go`, pura)

```
requested ──placa confirma──▶ recording ──stop (gesto|ficha|voz|silencio|máximo)──▶ closing ──fichero cerrado──▶ transcribing
requested ──30 s sin confirmar──▶ (borrada, la ficha ofrece reintentar)                       │
recording ──30 s sin audio──▶ cut ──────────────────────────────────────────────────────────▶ transcribing
transcribing ──ok──▶ ready        transcribing ──error──▶ no_transcript ──reintentar──▶ transcribing
```

`Next(state, event, now) (state, actions, error)`: cada transición devuelve
las acciones de cáscara (mandar orden a la placa, cerrar fichero, encolar
transcripción, emitir evento). Tests de tabla contra §3.3, RM-05, RM-23…26.

## 3. Contratos

### 3.1 Server ↔ dashboard (admin)

| Método | Ruta | RM |
|--------|------|----|
| `POST /v1/admin/devices/{id}/meetings` `{requestedBy:"dashboard"}` → 202 `{meeting}` | inicia (RM-03); 409 si hay una en curso (RM-05); 422 si el perfil no es agente (RM-44) |
| `POST /v1/admin/meetings/{id}/stop` → 202 | RM-22 |
| `GET /v1/admin/meetings?deviceId=&state=&q=&limit=` → lista con estado, duración en vivo, quién y cómo | RM-14, RM-40, RM-42 |
| `GET /v1/admin/meetings/{id}` → detalle con transcript y summary | RM-41 |
| `GET /v1/admin/meetings/{id}/audio` (Range) | RM-41; el dashboard lo sirve por proxy en `/meetings/{id}/audio` para que `<audio>` no lleve el secreto (RM-47) |
| `GET /v1/admin/meetings/{id}/transcript?format=txt|srt` | RM-41 |
| `PATCH /v1/admin/meetings/{id}` `{speakers:{"A":"Ana"}, keep:true}` | RM-31, RM-45 |
| `POST /v1/admin/meetings/{id}/transcribe` | RM-33 reintento; `…/summarize` regenera (RM-32) |
| `DELETE /v1/admin/meetings/{id}` | RM-46 (borra fichero y contenido; deja la fila con `deleted_at`) |
| `PATCH /v1/admin/devices/{id}` gana `meetingSilenceMin`, `meetingMaxHours` | RM-23/24 |

### 3.2 Server ↔ placa

- **Orden en red** (RM-06/07), mismo socket UDP 5688 y mismo `hello→nonce`:
  `{"t":"cmd","n":nonce,"cmd":"record-start"|"record-stop","id":meetingId,"mac":HMAC-SHA256(orgSecret, nonce+"."+cmd+"."+id)}`
  → `{"t":"ok"}` | `{"t":"err","why":"auth|profile|busy|idle"}`. `busy` =
  ya grabando (RM-05), `idle` = stop sin grabación, `profile` = micro-usb
  (RM-54).
- **Orden por poll** (RM-06 sin LAN): la respuesta de `desired-profile` gana
  la cabecera `X-Meeting: start:<id>` | `stop:<id>`; la placa la atiende como
  la orden UDP. El server la retira cuando la placa confirma.
- **Confirmación** (RM-52): `PUT /v1/devices/{id}/meeting` con
  `X-Device-Secret`, cuerpo `{meetingId, state:"recording"|"stopped", reason}`.
  `recording` pasa la reunión a *Grabando* (fija `started_at`); `stopped` con
  `reason` (`gesture|silence|room_lost`) la cierra (RM-20, RM-23, RM-26).
- **Sesión**: `POST /v1/sessions` gana `{"kind":"meeting","meetingId":…}` en
  el cuerpo; el server despacha al agente con `mode:"meeting"` y `meeting_id`
  en los metadatos. El token es el de siempre.

### 3.3 Server ↔ agente

- `PUT /v1/meetings/{id}/audio` con `X-Agent-Secret` (nuevo secreto compartido
  server↔agente, `SEBASTIAN_AGENT_SECRET`), `Transfer-Encoding: chunked`,
  `Content-Type: audio/ogg`. El server **añade al fichero según llega**
  (RM-15) y actualiza `audio_bytes`; si el cuerpo se corta, lo que hay queda
  (RM-25). Una sola conexión abierta durante toda la reunión.
- `POST /v1/admin/meetings/{id}/stop` también lo usa el agente para la parada
  por voz (RM-21), con `X-Agent-Secret`.

## 4. Bloques verticales y sus suites

Cada bloque es entregable y probable por sí solo (memoria: *tests primero,
verticales*). Los identificadores `T-Bn` son los tests que se escriben antes
del código del bloque.

### Bloque A — el server gobierna la reunión (sin OpenAI, sin placa, sin agente)

Alcance: tabla `meetings`, `meeting_core.go`, contratos §3.1 y §3.2 (server
side), orden UDP `cmd` en `adoption.Client`, cabecera `X-Meeting` en el poll,
confirmación de la placa, recepción del audio en streaming, redes de
seguridad temporizadas (requested→borrada a 30 s; recording→cut a 30 s sin
audio; máximo de duración), eventos al outbox.

Tests (Go):
- `T-A1` tabla de transiciones de §3.3 y RM-05/23/24/25 sobre `Next` (puro).
- `T-A2` `POST …/meetings` con una en curso → 409; perfil micro-usb → 422 (RM-05, RM-44).
- `T-A3` orden UDP `cmd` firmada: la placa falsa (como `fakeDevice` de `protocol_test.go`) acepta con el secreto de organización y rechaza `auth`; `busy`/`idle`/`profile` se mapean a mensajes (RM-06/07, RM-54).
- `T-A4` sin LAN: la orden viaja en `X-Meeting` del siguiente poll y desaparece tras la confirmación (RM-06).
- `T-A5` `PUT /v1/devices/{id}/meeting` exige el secreto de altavoz; `recording` fija `started_at`; `stopped:silence` cierra con motivo (RM-52, RM-23).
- `T-A6` audio en streaming: un cuerpo chunked que se corta a mitad deja el fichero con lo recibido y la reunión pasa a *cut* a los 30 s (RM-15, RM-25) — integración con Postgres y disco temporal.
- `T-A7` outbox: `meeting.requested/started/ended` en la misma transacción (integración, como `TestDeviceLifecycle…`).
- `T-A8` listado con duración en vivo y filtro por estado (RM-14, RM-40).

### Bloque B — la placa graba y lo señaliza

Alcance firmware: lectura del GPI del botón y detector de gesto en
`core/gesture_core.zig` (puro: eventos `press/release` con marca de tiempo →
`mute_tap | record_toggle | nada`); modo **reunión** en la sesión
(`session_core`: sin corte corto, sin reproducción, sin turnos; el silencio
lo mide el server, no la placa); anillo rojo fijo / lento / rápido en
`xvf_ui.zig`; orden `cmd` en `adopt.c`; cabecera `X-Meeting` en
`control.zig`; confirmación `PUT …/meeting`; arranque siempre sin grabar
(RM-53); rechazo en micro-usb (RM-54).

Tests (Zig, host):
- `T-B1` `gesture_core`: corta+larga con los umbrales de RM-02 → `record_toggle`; corta sola → `mute_tap`; larga sola → nada; corta+corta → dos `mute_tap`; más de 1 s entre ambas → no es gesto.
- `T-B2` `session_core` en modo reunión: no cierra por silencio corto ni por `agent_close` de conversación; cierra por `record-stop`, por `room_lost` a los 30 s y por `max_duration`.
- `T-B3` estado del anillo (puro): grabando → rojo fijo; aviso de silencio → rojo lento; sin control room → rojo rápido; mute durante grabación → rojo con patrón de mute; fuera de grabación nunca rojo (RM-10/13/51).
- `T-B4` `adopt.c` (test de protocolo desde Go, `protocol_test.go`): `cmd` con firma buena → `ok`; mala → `err auth`; en micro-usb → `err profile`.
- Hardware (manual, con syslog): RM-02 tiempos, RM-10 < 1 s, RM-13, RM-26.

### Bloque C — el agente captura y entrega

Alcance: `meeting_mode.py` (modo reunión: sin LLM, sin TTS salvo las dos
frases de confirmación; `ffmpeg` subproceso PCM→Ogg/Opus 48k mono; envío
chunked al server con reconexión que **no** reinicia el fichero: si la
conexión cae, el agente reintenta con `Content-Range` desde `audio_bytes`);
`ffmpeg` en la imagen; `SEBASTIAN_AGENT_SECRET`.

Tests (Python):
- `T-C1` el modo reunión se decide por los metadatos del job (puro, como `device_identity.py`): `mode=meeting` + `meeting_id` → modo reunión; sin ellos → conversación.
- `T-C2` el encoder produce Ogg/Opus válido a partir de PCM sintético y el flujo es incremental (páginas Ogg cada ≤ 1 s) — con `ffmpeg` real, se salta si no está.
- `T-C3` la subida reanuda desde el byte que el server declara tener (fake HTTP).
- `T-C4` en modo reunión el agente no genera respuestas ni reproduce nada salvo la confirmación inicial (fake `AgentSession`).

### Bloque D — transcripción, resumen y retención (server)

Alcance: job de transcripción (`ffmpeg -c copy` a WebM en piezas ≤ 20 MB,
`gpt-4o-transcribe-diarize` + `diarized_json`, `known_speaker_references`
entre piezas, `whisper-1` sin hablantes como reserva), normalización de
hablantes (`A, B…` → `Hablante 1, 2…`), resumen con el modelo mini vigente
(`SEBASTIAN_SUMMARY_MODEL`, por defecto `gpt-5-mini`; se fija al implementar
consultando `GET /v1/models`), reintentos, retención diaria, borrado.

Tests (Go):
- `T-D1` troceado (puro): duración y tamaño → lista de cortes ≤ 20 MB con solape de 2 s; una reunión de 40 min → una pieza.
- `T-D2` fusión de piezas (puro): desplazamiento de tiempos, deduplicación del solape, hablantes coherentes por referencia; sin diarización (reserva) → un solo hablante.
- `T-D3` cliente OpenAI contra un servidor HTTP falso: multipart correcto, `response_format=diarized_json`, error 4xx → `no_transcript` con motivo, 5xx → reintento con backoff (RM-33).
- `T-D4` resumen: el prompt lleva el idioma detectado; desactivado por control room → no se llama (RM-32).
- `T-D5` retención (puro): qué reuniones caducan a fecha dada respetando `keep` (RM-45); el borrado deja fila con `deleted_at` y sin contenido (RM-46).
- `T-D6` privacidad: la petición a OpenAI no lleva metadatos del control room ni el id del altavoz (RM-35).

### Bloque E — el control room

Alcance dashboard: filtro por tipo en *Grabaciones* (RM-40), detalle con
reproductor + transcripción sincronizada (RM-41; `timeupdate` → segmento
activo; clic → `currentTime`), descargas txt/SRT, renombrar hablantes,
búsqueda (RM-42), botones y estado en la ficha (RM-43), mensaje en micro-usb
(RM-44), configuración de silencio/máximo, conservar y borrar con
confirmación. Proxy de audio `/meetings/$id/audio` con Range.

Tests (vitest, `meetings.ts` puro):
- `T-E1` `meetingLabel/state → texto y tono` para los siete estados y los motivos de parada.
- `T-E2` `activeSegment(segments, t)` y `srt(segments, speakers)` / `txt(...)`.
- `T-E3` `liveDuration(startedAt, now)` y "Grabando desde HH:MM (mm:ss)".
- `T-E4` la ficha oculta *Grabar* en micro-usb y explica (RM-44); muestra *Parar* con una en curso (RM-05).
- `T-E5` `renameSpeaker` aplica a todos los segmentos.

### Bloque F — voz (opcional, el riesgo (1) de la spec)

Alcance agente: dos intenciones (`grabar`, `parar`) como `function_tool`;
en modo reunión el agente solo escucha la palabra de activación y esa
intención (RM-12, RM-21); confirmaciones habladas (RM-04). Si el modo
"solo palabra de activación" se complica, el bloque se pospone y la v1 sale
con gesto + ficha, como prevé la spec.

Tests: `T-F1` clasificación de frases (puro, `text_match.py` ya existe) para
las variantes de RM-04/21; `T-F2` en modo reunión cualquier otra petición
recibe la frase fija de RM-21.

## 5. Orden y estimación

A → B → C (con A+B+C ya se graba de verdad y se ve el fichero en el server)
→ D → E → F. Estimación: A 1 d, B 1 d, C ½ d, D 1 d, E 1 d, F ½ d; **5 días**
frente a los 3,5 de la spec: la diferencia es la transcripción por piezas
(tope de 25 MB) y la subida reanudable, que la spec no contemplaba.

## 6. Decisiones que fija este diseño

1. El audio vive en el **volumen del server**, no en object storage; el
   agente lo envía en streaming. (Cuando haya MinIO en Sebastian, el server
   lo mueve allí sin cambiar contratos.)
2. `ffmpeg` en las imágenes del agente (codificar) y del server (remultiplexar
   y trocear). Sin recodificación en ningún punto.
3. Transcripción en el **server** (Go), no en el agente: el job sobrevive al
   fin de la sesión y se reintenta desde el dashboard.
4. Las órdenes en red se firman con el secreto de organización; sin él, solo
   poll (≤ 30 s), y la ficha lo dice.
5. La placa no mide el silencio ni el máximo: lo hace el server sobre el
   audio que recibe (una sola fuente de verdad) y manda `record-stop`; la
   placa solo avisa con el anillo cuando recibe `X-Meeting: warn` 30 s antes.
6. Modelo de transcripción `gpt-4o-transcribe-diarize` (reserva `whisper-1`);
   modelo de resumen configurable, por defecto el mini vigente.

## 7. Abierto (se decide en el spike 0 o al implementar)

- Qué bit de `GPI_READ_VALUES` es el botón y su nivel activo (spike 0 en
  placa, 1 h).
- Safari y Ogg/Opus en `<audio>`: si falla, el proxy del dashboard sirve el
  WebM remultiplexado (mismo `-c copy`), sin tocar el almacenamiento.
- Nombre exacto del modelo mini de resumen en la fecha de implementación.
