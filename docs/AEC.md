# AEC del XVF3800: diagnóstico y fix

**Resuelto 2026-07-02.** El AEC del XVF3800 nunca funcionó en este proyecto —
no por delay, ni por cableado, ni por formato I2S: **el build de fábrica
`inthost 1.0.7` de ReSpeaker trae `AEC_FAR_EXTGAIN = 0.0`** (escala lineal).
Con ese valor el AEC asume que el altavoz reproduce silencio y **no adapta
jamás**. El fix es una escritura I2C de 4 bytes en el boot:
`FAR_EXTGAIN = 1.0` (`xvf_aec.applyConfig()`).

Verificado en hardware: `AECCONVERGED` pasó de 0 permanente a 1 en <5 s de
tono; en sesión real el agente ya no se transcribe a sí mismo con el altavoz a
plena escala.

**Hallazgo colateral (volumen):** `esp_codec_dev_set_out_vol` fue siempre un
no-op — el `noop_codec_if` implementaba `set_vol`, lo que suprime el volumen
software de esp_codec_dev (solo se crea si `codec->set_vol == NULL`). El
histórico "bajamos a 35 para no acoplar" era placebo: el altavoz siempre sonó a
full-scale. Arreglado quitando `set_vol` del noop; ahora `set_out_vol(100)` =
0 dB reproduce la sonoridad de siempre y el mando es real (base para whisper
mode y control de volumen por RPC).

## Por qué falló el intento anterior

La sesión pasada intentamos auto-calibrar `SYS_DELAY` barriendo valores en
runtime y midiendo el residual. Nunca convergía porque **estábamos afinando el
delay de un AEC que no adaptaba**: con FAR_EXTGAIN=0.0 no hay ningún valor de
delay que funcione. Lección: verificar primero que el cancelador tiene señal y
ganas de trabajar, después afinar.

## El método (reproducible)

1. **Conseguir la tabla de comandos con IDs de wire.** La doc de XMOS lista los
   comandos sin resid/cmd numéricos; el mapa real está compilado en
   `libcommand_map.dylib` del repo `respeaker/reSpeaker_XVF3800_USB_4MIC_ARRAY`
   (`host_control/mac_arm64/`). Los accessors son `extern "C"`
   (`get_num_commands`, `get_cmd_id_info`, `get_cmd_val_info`, `get_cmd_name`):
   un shim C++ de 30 líneas con `dlopen` vuelca los 147 comandos →
   [`xvf3800_command_map.txt`](xvf3800_command_map.txt). Las funciones
   `print_enum_*` del mismo dylib decodifican los enums del mux de audio.

2. **Leer el estado del AEC por I2C** (`xvf_aec.zig`, protocolo device_control:
   write `[resid, cmd|0x80, n+1]` → read `n+1` con status en el byte 0;
   status `0x40` = retry, reintentar la transacción entera):
   - `AEC_AECCONVERGED` (33/3, i32 RO) — latched; el dato clave
   - `AEC_RT60` (33/9, f32 RO), `AEC_AECPATHCHANGE` (33/0)
   - `AUDIO_MGR_SYS_DELAY` (35/26), `REF_GAIN` (35/1), `MIC_GAIN` (35/0)
   - `AEC_FAR_EXTGAIN` (33/5, f32 RW) ← **el culpable**

3. **Sonda de referencia in-band** (`xvf_aec.probeReference()`, desactivada en
   el boot normal): el mux de salida del audio manager (`AUDIO_MGR_OP_R`, 35/19)
   permite rutear señales internas a la salida I2S. Ruteando
   `MUX_FAR_END[4]`/`MUX_FAR_END_W_GAIN[12]` al slot derecho y reproduciendo un
   tono, medimos desde el ESP si la referencia llega al XVF. Resultado: llegaba
   perfectamente (con el ×8 de REF_GAIN exacto) — descartó cableado/formato y
   dejó solo la config del AEC como sospechosa.

## Estado de fábrica del build ReSpeaker (referencia)

```
farends=1  sys_delay=-30  ref_gain=8.0  mic_gain=90.0  far_extgain=0.0  ← el bug
shf_bypass=0  far_end_dsp_enable=0  i2s_dac_dsp_enable=0
op_all = [USER_CHOSEN 0] [RAW_MICS 0] [RAW_MICS 2] [AEC_RESIDUALS 3] [RAW_MICS 1] [RAW_MICS 3]
```

Dato colateral valioso: el canal **RIGHT** que consumimos para wake word y STT
es `MUX_AEC_RESIDUALS` ch3 (`OP_R=[7,3]`) — literalmente el residual del AEC,
sin supresión de eco residual ni NS. Coherente con lo observado en
`MIC_CHANNEL_TUNING.md`.

## Categorías del mux de salida (decodificadas del dylib)

```
0 MUX_SILENCE          1 MUX_RAW_MICS        2 MUX_UNPACKED_MICS
3 MUX_MICS_W_GAIN      4 MUX_FAR_END         5 MUX_FAR_END_SYSDELAY
6 MUX_PROCESSED_MICS   7 MUX_AEC_RESIDUALS   8 MUX_USER_CHOSEN_CHANNELS
9 MUX_ALL_USER_CHANNELS  10 MUX_FAR_END_NATIVE  11 MUX_DELAYED_MICS
12 MUX_FAR_END_W_GAIN
```

## Pendiente / siguientes afinados

- `SYS_DELAY`: el default -30 funciona (converge); medir el delay real con
  chirp + correlación si queremos exprimir ERLE.
- El residual con tono puro a vol 60 cancela ~6 dB en pico — los armónicos del
  altavocito no son cancelables linealmente; con voz real el comportamiento
  end-to-end es bueno (sin auto-transcripción). Si hiciera falta más:
  `PP_ECHOONOFF`/supresión residual en el canal comms, o el modelo NL del PP.
- Subir volumen por encima de 60 cuando haya más horas de uso sin acople.
- `AEC_RT60` sigue en 0 con tono estacionario; con voz debería estimar. Ojo si
  nunca se mueve.
