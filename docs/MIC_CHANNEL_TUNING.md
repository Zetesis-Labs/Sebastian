# Ajuste del canal de micrófono (LEFT vs RIGHT) y nivel

Conclusiones de las pruebas A/B de los dos canales de salida del XVF3800, hechas
grabando la voz publicada a LiveKit a 48 kHz y evaluándola por métricas + escucha.

## TL;DR

- **Canal por defecto: `RIGHT` (ASR).** Es la elección para STT.
- **Ganancia por canal:** `SHIFT = 14` en RIGHT, `SHIFT = 15` en LEFT.
- **Limitador suave (soft-clip)** en `mic_src.zig` para domar picos sin recorte duro.
- El canal se elige en la instalación en `firmware/main/config.zig` (`mic_channel`).

## Los dos canales del XVF3800

| | RIGHT (ASR) | LEFT (comms) |
|---|---|---|
| Origen | beam del beamformer, **salta el post-procesado** | salida **totalmente procesada** |
| Procesado | post-AEC, **sin NS ni supresión de eco residual** | de-reverb + NS + supresión de eco residual + limitador |
| Pensado para | motores de reconocimiento (ASR/STT) | oído humano (llamadas full-duplex) |
| Limitador en el XVF | **no** | sí |

## Metodología

- Grabador puro `agent/record.py` (sin agente ni OpenAI → gratis): se une a la sala,
  se suscribe al track del micro y lo vuelca a WAV 48 kHz mono.
- Misma frase, mismo volumen/distancia en cada toma.
- Análisis: RMS, pico, % clipping, SNR, suelo de ruido, % de muestras en la zona del
  limitador. Escucha a ciegas con las tomas **normalizadas al mismo pico** (para juzgar
  calidad sin sesgo de volumen).

## Resultados medidos (tomas finales, con limitador)

| Métrica | RIGHT @ SHIFT=14 | LEFT @ SHIFT=15 |
|---|---|---|
| voz (RMS p80) | 7534 | 8052 |
| suelo de ruido (RMS p20) | ~250 | ~96 |
| SNR | 30.7 dB | 38.5 dB |
| limitador entrando (≥30k) | 0.17 % | 0.20 % |
| clipping duro | ~0 (residuo = overshoot de Opus) | ~0 |

Escucha (juez final = el oído):
- **RIGHT: más natural.** A distancia normal, limpio.
- **LEFT: suena más "enlatado"/procesado** — es el carácter del NS/comms del XVF,
  intrínseco al canal; no se quita bajando el nivel.

## Decisiones y por qué

### Canal para STT → RIGHT

1. Es el **canal de ASR por diseño** de XMOS: se saca antes del post-procesado porque
   los motores de reconocimiento prefieren voz natural y mínimamente procesada.
2. El procesado de LEFT (NS agresivo, AGC) **borra pistas espectrales** (formantes,
   transiciones) que el STT usa → "más limpio para el oído" ≠ "mejor para el STT".
3. Los STT modernos (Whisper, OpenAI Realtime) son robustos a ruido → el suelo algo
   más alto de RIGHT no molesta.
4. Evita **doble procesado** (NS del XVF encadenado con el del motor → sonaba a lata).
5. Con **push-to-talk** (mic cerrado mientras habla el agente) no necesitamos la
   supresión de eco de LEFT, que era su única ventaja real aquí.

LEFT sería la elección si el destino fuese un **oído humano** (llamada), no una máquina
que transcribe.

### Nivel (SHIFT) por canal

El rango dinámico de la voz según distancia es amplio y **no tenemos AGC propio**, así
que el `SHIFT` fijo es un compromiso:

- **RIGHT = 14.** A 13 clippeaba (metálico); a 14 se sienta bien.
- **LEFT = 15.** Es un canal más caliente (lleva AGC); a 14 machacaba el limitador
  (1.18 % de muestras comprimidas → coloración) y quedaba a doble nivel que RIGHT. A 15
  se iguala a RIGHT (~8000) y el limitador solo actúa de red de seguridad (0.20 %).

### Limitador suave (`softClip` en `mic_src.zig`)

Por debajo de la rodilla (24000) la muestra pasa lineal; por encima, los picos se
comprimen suavemente con `tanh` hacia (sin llegar a) fondo de escala, en vez de recortar
en seco. El recorte duro es lo que suena metálico en las sílabas fuertes.

**Caveat de RIGHT:** a bocajarro sigue clippeando **dentro del XVF** (el beam ASR no
tiene limitador de origen), y eso no se puede deshacer aguas abajo. A distancia normal de
un altavoz de sala no ocurre. LEFT no tiene ese problema (limitador propio del XVF).

## Configuración de instalación

`firmware/main/config.zig`:

```zig
pub const mic_channel: MicChannel = .right; // o .left
```

Se resuelve en comptime (coste cero) y ajusta el slot I2S **y** el `SHIFT` por canal.
Cambiar la línea y reflashear para instalar una unidad con el otro canal.

## Nota aparte: LED central de mute

Investigado en esta sesión: el LED central **no es controlable desde el host**. En cada
pulsación solo cambia `gpo1` (=mute/GPIO30); escribir GPIO30 mutea el mic pero no mueve
el LED. El recurso `RESID_LED` (0x0C) no es un servicer funcional en el firmware
`inthost 1.0.7` (devuelve status uniforme haciendo eco del comando). El LED lo maneja el
firmware interno del XVF (handler del botón) o está latcheado en hardware. Para tocarlo
haría falta recompilar el config del firmware del XVF.
