"""Plays an audio file on a Sebastian speaker through a playback room.

The test source of roadmap 2a: it asks the control room for a playback on
the unit, waits for the unit to join the room and publishes the file there
(anything ffmpeg can read, resampled to 48 kHz). Music settings by default:
stereo Opus at 128 kbps without DTX (DTX cuts quiet passages); --mono and
--bitrate to compare. Ctrl+C or the end of the file stops the playback;
--keep leaves it on.

    SEBASTIAN_ADMIN_SECRET=… uv run --project agent python tools/play.py \\
        --api http://localhost:18787 --device e072a1f96ef0 cancion.mp3
"""

import argparse
import asyncio
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

from livekit import rtc

RATE = 48000
FRAME = RATE // 50  # 20 ms per channel
JOIN_WAIT_S = 40
SUBSCRIBE_WAIT_S = 15


def admin(method: str, url: str, secret: str) -> dict:
    req = urllib.request.Request(url, method=method, headers={"X-Admin-Secret": secret})
    try:
        with urllib.request.urlopen(req, timeout=10) as res:
            body = res.read()
    except urllib.error.HTTPError as e:
        detail = e.read().decode(errors="replace")
        sys.exit(f"{method} {url} → HTTP {e.code}: {detail}")
    return json.loads(body) if body else {}


async def wait_for_unit(room: rtc.Room, device: str) -> bool:
    for _ in range(JOIN_WAIT_S * 10):
        if any(p.identity == device for p in room.remote_participants.values()):
            return True
        await asyncio.sleep(0.1)
    return False


async def publish(path: str, server_url: str, token: str, device: str, channels: int, bitrate: int) -> None:
    room = rtc.Room()
    subscribed = asyncio.Event()
    room.on("local_track_subscribed", lambda _track: subscribed.set())
    await room.connect(server_url, token)
    try:
        print(f"waiting for {device} to join (up to {JOIN_WAIT_S} s)…")
        if not await wait_for_unit(room, device):
            print("the unit did not join: publishing anyway", file=sys.stderr)
        source = rtc.AudioSource(RATE, channels)
        track = rtc.LocalAudioTrack.create_audio_track("playback", source)
        options = rtc.TrackPublishOptions(source=rtc.TrackSource.SOURCE_MICROPHONE, dtx=False)
        options.audio_encoding.max_bitrate = bitrate
        await room.local_participant.publish_track(track, options)
        # The unit is in the room seconds before its media path is up: start
        # the file once it listens, or its first seconds are lost.
        try:
            await asyncio.wait_for(subscribed.wait(), SUBSCRIBE_WAIT_S)
        except TimeoutError:
            print("the unit did not subscribe: playing anyway", file=sys.stderr)
        decoder = subprocess.Popen(
            ["ffmpeg", "-loglevel", "error", "-i", path, "-f", "s16le", "-ac", str(channels), "-ar", str(RATE), "-"],
            stdout=subprocess.PIPE,
        )
        assert decoder.stdout is not None
        frame_bytes = FRAME * channels * 2
        print(f"playing {path} ({'stereo' if channels == 2 else 'mono'}, {bitrate // 1000} kbps)")
        try:
            while chunk := decoder.stdout.read(frame_bytes):
                if len(chunk) < frame_bytes:
                    chunk += bytes(frame_bytes - len(chunk))
                await source.capture_frame(rtc.AudioFrame(chunk, RATE, channels, FRAME))
            await source.wait_for_playout()
        finally:
            decoder.kill()
    finally:
        await room.disconnect()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("file")
    parser.add_argument("--api", default=os.environ.get("SEBASTIAN_API_URL", "http://localhost:8787"))
    parser.add_argument("--device", required=True)
    parser.add_argument("--keep", action="store_true", help="leave the playback on when the file ends")
    parser.add_argument("--mono", action="store_true", help="publish one channel instead of two")
    parser.add_argument("--bitrate", type=int, default=128_000, help="Opus bitrate in bit/s (default 128000)")
    args = parser.parse_args()
    secret = os.environ.get("SEBASTIAN_ADMIN_SECRET") or sys.exit("SEBASTIAN_ADMIN_SECRET is not set")
    base = f"{args.api.rstrip('/')}/v1/admin/devices/{args.device}/playback"

    started = admin("POST", base, secret)
    print(f"playback {started['id']} on {args.device} (order: {started['delivery']})")
    try:
        asyncio.run(publish(args.file, started["serverUrl"], started["sourceToken"], args.device, 1 if args.mono else 2, args.bitrate))
    except KeyboardInterrupt:
        pass
    finally:
        if not args.keep:
            admin("DELETE", base, secret)
            print("playback stopped")


if __name__ == "__main__":
    main()
