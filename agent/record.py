"""Standalone mic recorder — NO agent, NO OpenAI (free).

Joins the LiveKit room, subscribes to the ESP32 mic track and writes it to a WAV
at 48 kHz mono so we can A/B the XVF LEFT vs RIGHT channel.

    uv run record.py /tmp/right.wav 12
"""

import asyncio
import os
import sys
import wave

from dotenv import load_dotenv
from livekit import api, rtc

load_dotenv()

URL = os.environ["LIVEKIT_URL"]
KEY = os.environ["LIVEKIT_API_KEY"]
SECRET = os.environ["LIVEKIT_API_SECRET"]
ROOM = "sebastian"

OUT = sys.argv[1] if len(sys.argv) > 1 else "/tmp/rec.wav"
SECS = int(sys.argv[2]) if len(sys.argv) > 2 else 12


async def main() -> None:
    token = (
        api.AccessToken(KEY, SECRET)
        .with_identity("recorder")
        .with_grants(api.VideoGrants(room_join=True, room=ROOM))
        .to_jwt()
    )
    room = rtc.Room()
    wav = wave.open(OUT, "wb")
    wav.setnchannels(1)
    wav.setsampwidth(2)
    wav.setframerate(48000)
    state = {"frames": 0, "stop": False}

    async def record(track: rtc.Track) -> None:
        stream = rtc.AudioStream(track, sample_rate=48000, num_channels=1)
        async for ev in stream:
            if state["stop"]:
                break
            wav.writeframes(bytes(ev.frame.data))
            state["frames"] += 1

    @room.on("track_subscribed")
    def _on(track: rtc.Track, pub: rtc.TrackPublication, participant: rtc.RemoteParticipant) -> None:
        if track.kind == rtc.TrackKind.KIND_AUDIO and "esp32" in participant.identity:
            print(f"[rec] subscribed to {participant.identity}")
            asyncio.create_task(record(track))

    await room.connect(URL, token)
    print(f"[rec] connected to '{ROOM}', recording {SECS}s -> {OUT}")
    await asyncio.sleep(SECS)
    state["stop"] = True
    await asyncio.sleep(0.3)
    wav.close()
    await room.disconnect()
    print(
        f"[rec] done: {state['frames']} frames ({state['frames'] * 0.01:.1f}s of 10ms frames) -> {OUT}"
    )


if __name__ == "__main__":
    asyncio.run(main())
