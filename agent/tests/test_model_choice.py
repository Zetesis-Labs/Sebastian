"""The realtime model a session uses comes from the speaker's agent profile."""
import asyncio
import json

import aiohttp
import pytest
from aiohttp import web

from model_choice import (
    CredentialsUnavailable,
    ModelChoice,
    ProfileCredentials,
    UnknownProvider,
    api_key_for,
    choice_from_metadata,
    fetch_profile_credentials,
    panel_key_for,
)


def metadata(config: dict | None = None, profile_id: str = "018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8") -> str:
    body: dict = {"device_id": "e072a1f96ef0", "profile_id": profile_id}
    if config is not None:
        body["config"] = config
    return json.dumps(body)


def test_the_profile_config_picks_provider_model_voice_and_language():
    raw = metadata({"provider": "phonic", "model": "phonic-1", "voice": "eduardo", "language": "es"})
    assert choice_from_metadata(raw, "gemini") == ModelChoice(
        provider="phonic", model="phonic-1", voice="eduardo", language="es",
        profile_id="018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8",
    )


def test_an_empty_profile_config_keeps_the_environment_default():
    choice = choice_from_metadata(metadata({}), "openai")
    assert choice.provider == "openai" and choice.model == "" and choice.profile_id


@pytest.mark.parametrize("raw", [None, "", "not json", "[]", json.dumps({"device_id": "x"})])
def test_no_dispatch_metadata_means_the_environment_and_no_profile(raw):
    assert choice_from_metadata(raw, "gemini") == ModelChoice(provider="gemini")


def test_an_unknown_provider_is_an_error_not_a_silent_default():
    with pytest.raises(UnknownProvider):
        choice_from_metadata(metadata({"provider": "ultravox"}), "gemini")
    with pytest.raises(UnknownProvider):
        choice_from_metadata(None, "geminis")


def test_the_panel_key_wins_over_the_environment():
    env = {"PHONIC_API_KEY": "ph_env", "GOOGLE_API_KEY": "g_env"}
    assert api_key_for("phonic", "ph_panel", env) == "ph_panel"
    assert api_key_for("phonic", None, env) == "ph_env"
    assert api_key_for("gemini", None, env) == "g_env"
    assert api_key_for("openai", None, env) is None
    assert api_key_for("phonic", None, {"PHONIC_API_KEY": " "}) is None


def fetch_against(handler, secret: str = "s3cret", profile_id: str = "abc"):
    async def run():
        app = web.Application()
        app.router.add_get("/v1/agent-profiles/{id}/credentials", handler)
        runner = web.AppRunner(app)
        await runner.setup()
        site = web.TCPSite(runner, "127.0.0.1", 0)
        await site.start()
        port = site._server.sockets[0].getsockname()[1]  # type: ignore[union-attr]
        try:
            async with aiohttp.ClientSession() as http:
                return await fetch_profile_credentials(http, f"http://127.0.0.1:{port}", secret, profile_id)
        finally:
            await runner.cleanup()

    return asyncio.run(run())


def test_fetch_returns_the_stored_key():
    seen = {}

    async def handler(request):
        seen["secret"] = request.headers.get("X-Agent-Secret")
        seen["id"] = request.match_info["id"]
        return web.json_response({"provider": "phonic", "apiKey": "ph_panel"})

    assert fetch_against(handler) == ProfileCredentials(provider="phonic", api_key="ph_panel")
    assert seen == {"secret": "s3cret", "id": "abc"}


def test_fetch_without_a_stored_key_has_no_key():
    async def handler(_):
        return web.json_response({"provider": "gemini"})

    assert fetch_against(handler) == ProfileCredentials(provider="gemini", api_key=None)


def test_a_legacy_profile_reports_no_provider():
    async def handler(_):
        return web.json_response({})

    assert fetch_against(handler) == ProfileCredentials(provider="", api_key=None)


def test_the_stored_key_is_only_used_for_the_provider_it_was_stored_for():
    choice = ModelChoice(provider="phonic", profile_id="abc")
    assert panel_key_for(choice, ProfileCredentials(provider="phonic", api_key="ph_panel")) == "ph_panel"
    assert panel_key_for(choice, ProfileCredentials(provider="phonic", api_key=None)) is None
    with pytest.raises(CredentialsUnavailable):
        panel_key_for(choice, ProfileCredentials(provider="gemini", api_key="AIza-panel"))
    with pytest.raises(CredentialsUnavailable):
        panel_key_for(choice, ProfileCredentials(provider="", api_key="AIza-panel"))


@pytest.mark.parametrize("status", [401, 404, 503])
def test_fetch_failures_are_raised(status):
    async def handler(_):
        return web.json_response({"title": "x"}, status=status)

    with pytest.raises(CredentialsUnavailable):
        fetch_against(handler)
