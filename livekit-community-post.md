# I put LiveKit on an ESP32-S3 + ReSpeaker XVF3800 — a full-duplex smart speaker that listens while it talks

Hi all 👋

I've been building **Sebastian**, a real, standalone **smart speaker** you can put on your desk — a hands-free voice assistant that runs **LiveKit's WebRTC stack on a bare ESP32-S3 microcontroller** (not a Pi, not a phone — an MCU with 512KB SRAM + PSRAM). You say a wake word, talk to it, and it talks back through its own loudspeaker. It talks to a LiveKit Cloud room, and a Python `agents` worker drives the conversation with a speech-to-speech realtime model.

> _[📷 attach a photo of the device here — for a show & tell, the hardware sells it. It's a ReSpeaker XVF3800 4-mic array + speaker with a XIAO ESP32-S3 as the brain.]_

Most LiveKit projects I see are web/mobile/server. Running the **`client-sdk-esp32`** on real embedded hardware turned up a bunch of LiveKit-specific patterns and gotchas that I think are worth sharing — some clever, some painful. Would love feedback from anyone doing embedded or agents work.

## The stack

- **Device**: XIAO ESP32-S3 + a ReSpeaker XVF3800 (XMOS) doing beamforming/AEC on-chip. Firmware in Zig over ESP-IDF, using `client-sdk-esp32` (Developer Preview).
- **Transport**: LiveKit Cloud (WebRTC, Opus, SRTP).
- **Agent**: Python `livekit-agents` ~1.6, a `RealtimeModel` (Gemini Live / OpenAI Realtime, swappable), + `noise-cancellation` (BVC).
- **Wake word** runs on-device (microWakeWord); a session only opens *after* the wake word, so it's idle/cheap the rest of the time.

## The LiveKit bits I found most interesting

### 1. Pre-roll over a data byte-stream, not the media track
The hardest UX problem: when you say the wake word, the first ~2 seconds of your command happen *before* the room is even connected. You can't inject that audio into the media track after the fact (pts/jitter/ordering make it hopeless).

Solution that worked great: the device records a **12s ring buffer in PSRAM**, and at the mic hand-off it ships the pre-roll as a **framed byte stream** over a LiveKit topic (`register_byte_stream_handler` on the agent side). The agent parses it, resamples it, and **injects those frames into a custom `AudioInput` ahead of the live audio**. Nothing is lost, and the model hears the full utterance including the part before "connected".

### 2. Custom `agents.io.AudioInput`
Instead of the agent subscribing to the track directly, I feed the session a custom `AudioInput` subclass. It merges the pre-roll frames + the live track, exposes a `preroll_ready` event so the entrypoint can skip the greeting, and owns its own resampling. This turned out to be a really clean extension point for embedded, where the audio timeline isn't "just a track".

### 3. Explicit agent dispatch via the token — and a gotcha
The device does a plain HTTP GET to a tiny token server that mints a short-lived JWT with **`RoomConfiguration(agents=[RoomAgentDispatch(agent_name=...)])`** embedded. So the named agent auto-dispatches when the device joins — no separate dispatch call.

**Gotcha that cost me hours**: `RoomConfiguration` in the token only applies when the room is **created**. On a re-wake into an existing room, the dispatch is silently ignored → "the agent never joins the second time". Minting per-session + letting the room close between sessions fixed it.

### 4. RealtimeModel provider factory + a real-world fallback lesson
I use a provider factory so `SEBASTIAN_MODEL_PROVIDER` swaps between `google.realtime.RealtimeModel` and `openai.realtime.RealtimeModel`, with turn detection owned by the model (`TurnHandlingOptions(turn_detection="realtime_llm")`).

Two lessons:
- The Gemini **native-audio preview** model rejected explicit BCP-47 language codes (`es-ES` → `APIError 1007`, session closes, agent goes silent while the device session stays open). Fix: let it auto-detect.
- It also throws transient `1011 internal error`s. Having the OpenAI fallback wired as **one env var** saved me — highly recommend the factory pattern if you're on a preview model.

### 5. Background Voice Cancellation for a TV in the room
`noise_cancellation.BVC()` on the incoming track is the reason a TV playing in the background doesn't get transcribed as user speech. Plain NS won't strip *voices*; BVC does. Combined with the on-device beamforming, background chatter basically disappears.

### 6. Barge-in via a data channel
Wake-word-over-agent-speech = interrupt. The device publishes a `sebastian.barge_in` data packet; the agent interrupts the current generation. A second `agent_state` data topic mirrors speaking/listening so the firmware knows when to gate the mic (half-duplex fallback).

### 7. Playback: Opus off the wire, straight to the speaker
The output side matters too — it *is* a speaker after all. The agent's TTS comes back as an **Opus track over WebRTC**; the device decodes it on the MCU and plays it through its loudspeaker via `av_render`. Two things fell out of this nicely:
- The speaker's **render-reference callback** (the exact PCM going to the driver) doubles as my "is the agent talking?" signal — the data-channel-independent keepalive I mention below. No need to trust the data channel to know when audio is flowing.
- That same playback is the **AEC far-end reference** for the XVF3800 — which is what makes it genuinely full-duplex (more on that below).

## Two things that aren't really LiveKit — but people keep asking

### It listens while it talks (on-device AEC → real full-duplex)
The one I'm proudest of: it's genuinely **full-duplex**. The XVF3800 takes the speaker's playback as the **AEC far-end reference** and cancels the device's own voice out of the 4-mic array — so you can interrupt it **mid-sentence** and it hears you *over its own output*, on a bare microcontroller. Two things I learned the hard way:
- Getting the canceller to converge meant **freezing the beamformer** to a fixed direction. An adaptive beam that tracks the talker is a *non-stationary* echo path, and the AEC can never lock onto it — it just chases a moving target forever. Fixed beam → converges in ~1s.
- A neat side effect: because the TV in the room is off the beam axis, it's attenuated *before* the wake word even sees it. Background chatter mostly never crosses the "someone is speaking" threshold on-device — the cloud BVC is a second line of defense, not the first.

### The firmware is Zig, not C/C++
The whole device side is **Zig on top of ESP-IDF** — I wanted memory safety and compile-time config without dragging in a heavier runtime. What made it work:
- The ESP32-S3 is **Xtensa**, which isn't a mainline LLVM target, so I build against Espressif's Zig fork (`kassane/zig-espressif-bootstrap`) wired into the ESP-IDF cmake build.
- Instead of translating the IDF headers with Zig's `cImport` builtin (it chokes on the giant structs), I hand-declare only the `extern fn`s I use, plus thin C shims where the C API is more ergonomic.
- `comptime` handles per-install config (which mic slot, fixed beam, etc.) at zero runtime cost.
- Footgun for anyone trying this: Zig's `@`-builtins like `min` **narrow the result type** to a `comptime_int` argument's range — it bit me with five identical overflow crashes before I spotted it.

## The painful one: an SCTP storm on the ESP32

This is the one I'd most love input on. Mid-session, the device's `esp_peer` (the vendored WebRTC core under `client-sdk-esp32`) starts flooding `SCTP: Send INIT chunk` hundreds of times, never associates, and ends in `SCTP_ABORT` — **while the media (SRTP) keeps working fine on its separate transport**. So audio survives, but the **data channel dies mid-conversation**.

Because I was (initially) relying on the data channel to keep the session alive, sessions got cut off. Two things helped:
- A **data-channel-independent keepalive**: I tap the speaker's render-reference callback (the PCM being played) to know the agent is talking, instead of trusting the data channel.
- Reported it upstream: **[espressif/esp-webrtc-solution#186](https://github.com/espressif/esp-webrtc-solution/issues/186)**. There's a `no_auto_reconnect` knob in `esp_peer.h` that would likely fix it, but the `livekit__livekit` wrapper (0.3.10) doesn't expose it yet.

If anyone here has run `client-sdk-esp32` in production and tamed the SCTP/data-channel reconnect behavior, I'd love to compare notes.

## Observability

The agent exports OTLP (traces/metrics/logs) and I stream device telemetry (serial → OTLP) into the same Grafana stack, including **per-turn transcripts**. Debugging "why didn't it respond" became "read what it actually heard" — which is how I found both the language-code and SCTP issues. Highly recommend instrumenting the agent early.

## Try it / links

- **Repo** (firmware in Zig, the Python agent, token server, everything): https://github.com/Zetesis-Labs/Sebastian
- **Web installer** — flash the firmware onto an ESP32-S3 **straight from your browser** (ESP Web Tools, no toolchain, WiFi/token provisioned over Web Serial): https://zetesis-labs.github.io/Sebastian/
- **The SCTP issue** upstream: https://github.com/espressif/esp-webrtc-solution/issues/186

## Honestly, this project is bigger than me

I'll be upfront: I stitched this together well past my comfort zone. Embedded WebRTC, real-time DSP, and agent orchestration are not my home turf, and I'm sure there are things here I've done naively, worked around when I should have fixed them properly, or just plain gotten wrong. That's exactly why I'm posting it rather than quietly shipping it.

So what I'd genuinely value is **honest, critical feedback** — tell me what you'd have done differently, where the sharp edges are, what I've misunderstood about LiveKit, WebRTC on constrained devices, or the agents pipeline. Be blunt; I'd much rather learn than be reassured.

Happy to go deep on any part (the wake→dispatch→pre-roll handoff, the token server, the custom `AudioInput`). Thanks for reading — and thanks for the docs and examples that got me this far 🙏
