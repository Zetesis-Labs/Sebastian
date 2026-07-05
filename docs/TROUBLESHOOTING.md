# Sebastian — Debugging Playbook

A distilled **symptom → root cause → solution** reference from a real, long debugging session of the Sebastian voice speaker (ReSpeaker XVF3800 + XIAO ESP32-S3, Zig firmware, LiveKit + OpenAI Realtime agent).

## Golden Rule

When something fails, **prioritize the datasheet and direct measurement over guesswork**. Record the actual audio, look at the RMS and the spectrum, and above all **LISTEN TO IT**. Numbers (RMS levels, peaks) describe *level*, not *intelligibility*: audio can have good RMS and sound tinny. And don't reimplement in software what the XVF3800 already does in hardware (AEC, beamforming, noise suppression): duplicating those stages almost always worsens the result instead of improving it. Most of the dead ends in this session came from guessing instead of measuring, and from "helping" the XVF via software.

---

## 1. The mic delivers all zeros (the agent never transcribes)

**Symptom:** `0x00000000` is constantly read on the I2S DIN; the agent doesn't transcribe anything the user says.

**Root cause:** two problems at once. The XVF3800 had the wrong firmware (the "non-master" / `i2s_dfu` 1.0.4, which **does not** stream mic audio). Additionally, the XVF **boots muted**.

**Solution:** perform a DFU of the XVF to the "inthost" I2S-master firmware (1.0.7) via I2C from the ESP firmware (`xvf_dfu.zig`), and unmute it via I2C on GPIO30. Verify in the boot log that XVF version 1.0.7 and `XVF mute readback: UNMUTED` appear.

---

## 2. In I2S slave mode the ESP reads 0 bytes / the read returns instantly without data

**Symptom:** in I2S slave mode, the ESP read returns immediately without data (there is no clock).

**Root cause:** the I2S RX channel was enabled in `board.init()` **before** the XVF was generating a clock, so it never locked onto the clock.

**Solution:** disable and re-enable the RX channel **after** the XVF is up (done on the first call to `read_frame()`). Confirmed with a "clock probe" that, once fixed, blocked for ~5 ms and returned a full frame of data.

---

## 3. `Guru Meditation Error ... find_desc_for_source ... LoadProhibited` and boot-loop

**Symptom:** `Guru Meditation Error ... find_desc_for_source ... LoadProhibited` during I2S-slave or I2C interrupt allocation; the board goes into a boot-loop.

**Root cause:** two things. (a) An intermittent instability in interrupt allocation. (b) Sharing a single duplex I2S channel between the mic read task and the speaker renderer corrupted the DMA.

**Solution:** use **two separate I2S ports** (mic RX on `I2S_NUM_1`, speaker TX on `I2S_NUM_0`), never a shared duplex channel. If it continues in a boot-loop, power-cycle it.

---

## 4. Boot-loop and esptool does not connect ("Failed to connect to ESP32-S3: No serial data received")

**Symptom:** the board enters a boot-loop and esptool fails to connect: `Failed to connect to ESP32-S3: No serial data received`.

**Root cause:** the firmware crashes or hangs so fast that the USB-JTAG cannot sync; additionally, a specific binary can shift the IRAM and expose a latent crash.

**Solution:** perform a physical power-cycle (disconnect USB for 3 s and reconnect); or hold down the BOOT button (the recessed one) while plugging in the USB (manual download mode); then flash a known-good binary. **The board is not bricked.**

---

## 5. `Guru Meditation ... vApplicationStackOverflowHook` (stack overflow)

**Symptom:** `Guru Meditation ... vApplicationStackOverflowHook`, stack overflow.

**Root cause:** a FreeRTOS task had a large local buffer (a ~2 KB I2S read buffer) on a stack that was too small (4 KB).

**Solution:** move large buffers to module-level statics and/or increase the task stack (stack size parameter in `xTaskCreatePinnedToCore`).

---

## 6. The voice sounds like a "helicopter" and/or "from beyond the grave" (ghostly, with warble), but the RMS shows no gaps

**Symptom:** the voice sounds like a helicopter and/or from beyond the grave (ghostly, warbling), although the RMS shows no drops or gaps.

**Root cause:** three, all fixed.
- (a) A free-running producer task + ring buffer drifted against the consumer's clock domain (XVF at 48 kHz vs. LiveKit consumer); the ring periodically emptied and repeated/held samples → periodic warble.
- (b) A per-sample AGC that scaled each sample according to its own magnitude **deformed** the waveform → robotic/ghostly voice.
- (c) A race condition between cores: both the producer and the consumer were writing the read index of the ring buffer.

**Solution:** completely eliminate the ring buffer and read the I2S **directly** inside `read_frame()` (paced by the consumer — the pipeline demand *is* the clock, no drift); use **fixed gain**, never a per-sample AGC; and if a ring is ever used, make it strictly single-producer/single-consumer (only the consumer writes the read index).

---

## 7. The voice is "choppy" (chunks of silence)

**Symptom:** the voice is choppy, with fragments of silence.

**Root cause:** the ring buffer suffered an underrun and an overly aggressive "rebuild the cushion" path inserted ~256 ms of silence on every small underrun.

**Solution:** direct reading paced by the consumer (no ring) eliminates this. If buffering is used, on a brief underrun **hold the last sample** instead of inserting a cushion of silence.

---

## 8. The voice is intelligible but sounds "tinny" (metallic)

**Symptom:** intelligible voice but with a tinny/metallic timbre.

**Root cause:** **double noise suppression**. The LEFT output of the XVF already carries the XVF's on-chip NS, and the agent applies its own BVC noise cancellation on top; two passes of NS generate musical-noise artifacts.

**Solution:** use the **RIGHT** I2S slot (the raw ASR beam, without on-chip NS) and let the agent's BVC do the **only** NS pass. Honest caveat: XMOS does not explicitly require this; it was an empirical choice and it's advisable to do an A/B per environment.

---

## 9. The voice is "saturated/distorted" (clipping)

**Symptom:** saturated voice, with clipping distortion.

**Root cause:** the 32→16 bit fixed gain (right shift) was too small, so loud speech clamped at 32768.

**Solution:** calibrate the shift based on the RMS/peak of the recorded WAV — target ~-18 dBFS RMS with peaks below full scale (in this project `SHIFT=13` for the RIGHT/ASR channel). Note: the voice level varies ~4× between sessions, so a fixed gain is a compromise; the professional solution would be a **slow-envelope AGC** (constant gain within a block, adapted slowly). A **per-sample AGC MUST NOT be used** (it distorts, see entry 6).

---

## 10. The AI "talks to itself" / ignores the user / responds incoherently

**Symptom:** the AI talks to itself, ignores the user, or responds with nonsense.

**Root cause:** **multiple agent processes** running at the same time (uv launches a wrapper + a worker; zombie processes accumulate).

**Solution:** `pkill -9 -f "agent.py"` and start exactly **one**: `uv run agent.py dev`.

---

## 11. The agent never enters the room (the room shows 1 participant)

**Symptom:** the agent does not join the room; `lk room list` shows only 1 participant.

**Root cause:** the agent only auto-dispatches to a **new** room.

**Solution:** `lk room delete sebastian`, then reset the device to join a fresh room; confirm that `lk room list` shows 2 participants.

---

## 12. You can't tell if the audio is good just from the numbers

**Symptom:** impossible to judge audio quality from the figures.

**Root cause:** audio **levels** (RMS/peak) are not **intelligibility**.

**Solution:** the agent records the received mic in `/tmp/sebastian_rx.wav` — **LISTEN TO IT** with `afplay` and calculate an approximate spectrum (energy per frequency). For example, a "tinny" voice shows almost no energy above ~2 kHz.
