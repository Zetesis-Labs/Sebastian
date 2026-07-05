"""Token server for the Sebastian device.

Mints a short-lived LiveKit access token on demand so the firmware doesn't carry
a static JWT that expires (720h → reflash). The token also embeds an explicit
agent dispatch (RoomConfiguration.agents), so when the device joins the room the
named agent ("sebastian") is dispatched to it automatically — no more "delete the
room + reset the board" dance that plain auto-dispatch required.

The device does a plain HTTP GET at each session open and gets back two lines:

    <serverUrl>\n<token>

Plaintext (not JSON) keeps the firmware parser trivial: split on the first
newline. The URL is a wss:// address and the token is a dot-separated JWT, so
neither contains a newline.

Run:
    uv run token_server.py                              # binds 127.0.0.1:8787
    SEBASTIAN_TOKEN_HOST=0.0.0.0 uv run token_server.py  # serve the device on the LAN
    SEBASTIAN_TOKEN_PORT=9000 uv run token_server.py

Reads LIVEKIT_URL / LIVEKIT_API_KEY / LIVEKIT_API_SECRET from .env (same as the
agent). Serve it on the LAN the device is on; point the firmware at it via
`token_server_url` in secrets.zig.
"""

import os
from datetime import timedelta
from typing import AsyncIterator

from aiohttp import web
from dotenv import load_dotenv
from livekit import api

load_dotenv()

ROOM = "sebastian"
IDENTITY = "esp32-respeaker"
AGENT_NAME = "sebastian"
TOKEN_TTL = timedelta(hours=1)  # refetched per session, so short is fine

# Bind to loopback by default: this mints LiveKit tokens, so don't expose it to
# the whole LAN unless asked. Set SEBASTIAN_TOKEN_HOST=0.0.0.0 to serve the device.
HOST = os.getenv("SEBASTIAN_TOKEN_HOST", "127.0.0.1")
PORT = int(os.getenv("SEBASTIAN_TOKEN_PORT", "8787"))
LIVEKIT_URL = os.environ["LIVEKIT_URL"]
API_KEY = os.environ["LIVEKIT_API_KEY"]
API_SECRET = os.environ["LIVEKIT_API_SECRET"]


def _mint() -> str:
    grant = api.VideoGrants(room_join=True, room=ROOM)
    dispatch = api.RoomAgentDispatch(agent_name=AGENT_NAME)
    room_config = api.RoomConfiguration(agents=[dispatch])
    jwt: str = (
        api.AccessToken(API_KEY, API_SECRET)
        .with_identity(IDENTITY)
        .with_name(IDENTITY)
        .with_grants(grant)
        .with_room_config(room_config)
        .with_ttl(TOKEN_TTL)
        .to_jwt()
    )
    return jwt


async def _dispatch_agent(lk: api.LiveKitAPI) -> None:
    # RoomConfiguration in the token only applies when the room is CREATED.
    # Joining a still-alive empty room (re-wake within the ~5min empty timeout)
    # silently skips the agent dispatch — the "says nothing after a quick
    # re-wake" bug. Dispatching explicitly here covers both cases; awaiting it
    # BEFORE returning the token means the device can never join first and
    # trigger a duplicate via the token's own dispatch.
    #
    # But there must be EXACTLY ONE agent in the room. If an agent is already
    # present (a prior fetch this session, or a reconnect re-fetch), a second
    # create_dispatch adds a second agent that talks over the first ("habla
    # solo"). So dispatch only when the room currently has no agent participant;
    # if the room doesn't exist yet (fresh wake) the lookup fails and we fall
    # through to dispatch, which is exactly what we want.
    try:
        parts = await lk.room.list_participants(
            api.ListParticipantsRequest(room=ROOM)
        )
        if any(p.kind == api.ParticipantInfo.Kind.AGENT for p in parts.participants):
            print("[token-server] agent already in room — skipping dispatch", flush=True)
            return
    except Exception as e:  # room not created yet → dispatch below
        print(f"[token-server] no room yet ({e!r}) — dispatching", flush=True)
    await lk.agent_dispatch.create_dispatch(
        api.CreateAgentDispatchRequest(agent_name=AGENT_NAME, room=ROOM)
    )


async def handle_token(request: web.Request) -> web.Response:
    try:
        await _dispatch_agent(request.app["lk"])
    except Exception as e:  # fresh rooms still dispatch via the token config
        print(f"[token-server] explicit dispatch failed: {e!r}", flush=True)
    else:
        print("[token-server] token + dispatch issued", flush=True)
    body = f"{LIVEKIT_URL}\n{_mint()}"
    return web.Response(text=body, content_type="text/plain")


async def handle_health(request: web.Request) -> web.Response:
    return web.Response(text="ok")


async def _lk_client(app: web.Application) -> AsyncIterator[None]:
    app["lk"] = api.LiveKitAPI()
    yield
    await app["lk"].aclose()


def main() -> None:
    app = web.Application()
    app.cleanup_ctx.append(_lk_client)
    app.add_routes([web.get("/token", handle_token), web.get("/health", handle_health)])
    print(
        f"[token-server] {LIVEKIT_URL} room={ROOM} agent={AGENT_NAME} on {HOST}:{PORT}",
        flush=True,
    )
    web.run_app(app, host=HOST, port=PORT)


if __name__ == "__main__":
    main()
