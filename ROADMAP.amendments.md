# ROADMAP — Enmienda / discrepancias a verificar (2026-07-09)

> **Qué es esto:** una lista de **posibles discrepancias** encontradas al revisar la
> documentación el 2026-07-09. **No modifica** `ROADMAP.md`, la memoria ni las
> descripciones de PR — esos quedan intactos. Aquí solo se **anotan** para revisar
> con calma y decidir después qué es cierto.
>
> **Estado:** NADA de esto está confirmado ni aplicado. Son observaciones con su
> evidencia y su nivel de confianza. Ante la duda, gana la doc original hasta que
> se verifique.

---

## 1. "Bug 1": announce-tras-conversación no re-suscribe la pista

- **Dónde se afirma:** memoria `sebastian-renderer-rebind.md` ("Known still-open:
  an announce fired right after a conversation doesn't re-subscribe…"); descripciones
  de los drafts **PR #11** y **PR #12**.
- **Qué lo pone en duda:**
  - **No aparece en `ROADMAP.md`** (ninguna mención a re-subscribe / announce-tras-conversación).
  - `ROADMAP.md` §9 afirma lo contrario: *"Proactive announce to a sleeping device works (heard)"*.
  - El usuario lo validó por oído en la sesión ("se ha oído todo validado").
  - La única evidencia fue **un** caso (2026-07-08 ~20:59): un **playout timeout**
    (que es el quirk *conocido* de `wait_for_playout` con RealtimeModel — no señaliza
    fin; lo maneja el fix `ANNOUNCE_PLAYOUT_TIMEOUT_S` por diseño) + un **WAV de 524 B**
    (posible artefacto del recorder). La captura de serial **no era continua**, así que
    "no salió línea `Subscribing`" no es concluyente.
- **Confianza en que sea un bug real:** BAJA.
- **A verificar:** repro limpia con **serial continuo** — announce en reposo →
  conversación → announce otra vez → ver si sale `Subscribing to audio track` y
  `render_peak` en millones. Confirmar o enterrar.
- **Resolución propuesta (sin aplicar):** degradar de "bug" a "observación sin confirmar"
  en memoria y en #11/#12.

## 2. "Bug 2": reconexión / orfanato (#186)

- **Dónde se afirma:** descripciones de **PR #11** y **PR #12** (como bloqueante).
- **Matices encontrados:**
  - **Sí es real y ya está documentado** en `ROADMAP.md` §9: *"the client SDK lies
    about room state after a server-side room delete … TODO: app-level liveness
    ping/pong"*. → No es un descubrimiento nuevo.
  - El orfanato concreto de hoy lo **causó `end_session` borrando la room**, y el fix
    `end_session` endpoint-aware del **PR #12** elimina ese disparador.
  - El residual solo muerde si el **job del agente muere/redeploya**, no en uso normal.
- **Confianza:** el fenómeno es real; su **caracterización como bloqueante nuevo** es
  imprecisa.
- **Resolución propuesta (sin aplicar):** en #11/#12 reformular a "limitación #186
  conocida (ROADMAP §9), muy mitigada por el fix de `end_session`; pendiente el
  ping/pong de liveness".

## 3. El ROADMAP sobre-afirma el estado de *merge* (la discrepancia mayor)

- **Dónde:** `ROADMAP.md` — endpoint *"SHIPPED v1, ear-validated (2026-07-08)"*
  (§9, ~L765); announce *"HTTP face SHIPPED + validated"* (~L172 / ~L763).
- **El problema:** en **main NO existe** ninguno de los dos:
  - Endpoint mode → en **drafts PR #11 (firmware) y #12 (agente)**, sin mergear.
  - Announce / `control_plane.py` → en **PR #8**, abierto sin mergear.
- "SHIPPED" ahí significa "construido y validado **en una rama**", no "en main". Y como
  el ROADMAP **ya está en main** (vía PR #10), afirma desde main cosas que main no contiene.
- **Confianza:** ALTA (es verificable con `git`/`gh pr list`).
- **Resolución propuesta (sin aplicar):** marcar esas líneas como *"validado en rama,
  pendiente de merge (PRs #8 / #11 / #12)"* en vez de "SHIPPED".

## 4. Tono opuesto para la misma feature

- `ROADMAP.md` §9: endpoint = *"SHIPPED v1, ear-validated"*.
- Descripciones de **PR #11 / #12**: endpoint = *"bloqueado por 2 bugs, no mergear"*.
- Misma feature, marcos contradictorios. Depende de resolver los puntos 1–3.

## 5. (Menor) Posible drift código-vs-doc en el watchdog

- **Dónde:** `ROADMAP.md` §9 dice el firmware endpoint tiene *"reconnect on room death
  + a 15 s idle health check (CONNECTED + agent present)"*.
- **Observación:** en `firmware/main/app.zig` (rama `feat/control-plane-announce`) se ve
  un *session watchdog* que "restarts / reboots to listening" (≈ L392–L417) y checks de
  `state == DISCONNECTED` / `!= CONNECTED` (≈ L527–L533), pero **no** encontré con grep
  los términos exactos "unhealthy / recycle / 15 / ping / pong / reconnect".
- **Confianza:** BAJA — puede ser solo diferencia de nomenclatura, no una discrepancia real.
- **A verificar:** leer el bloque del watchdog en `app.zig` y cotejar que hace lo que el
  ROADMAP describe (o ajustar la redacción del ROADMAP).

---

## Cómo se generó esta enmienda

Revisión del 2026-07-09 cruzando: `ROADMAP.md`, memoria (`sebastian-renderer-rebind.md`),
descripciones de PRs (#8, #11, #12), logs de la sesión del 2026-07-08
(`agent_endpoint7.log`, capturas de serial) y estado de merge (`gh pr list`, `git`).

**Ninguna de estas resoluciones se ha aplicado.** Requieren tu visto bueno y, para el
punto 1, una repro limpia en hardware.
