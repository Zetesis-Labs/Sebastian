"""Which realtime model a session talks through.

The control room dispatches the agent with the speaker's agent profile: its
`config` names the provider, model, voice and language, and empty fields keep
the agent's environment defaults. The provider API key is the profile's own
when the panel stored one (fetched from the server, never carried by LiveKit),
else the agent's environment key.
"""
from __future__ import annotations

import json
from collections.abc import Mapping
from dataclasses import dataclass

import aiohttp

PROVIDERS = ("gemini", "openai", "phonic")
ENV_KEYS = {"gemini": "GOOGLE_API_KEY", "openai": "OPENAI_API_KEY", "phonic": "PHONIC_API_KEY"}


class UnknownProvider(ValueError):
    pass


class CredentialsUnavailable(RuntimeError):
    pass


@dataclass(frozen=True)
class ModelChoice:
    provider: str
    model: str = ""
    voice: str = ""
    language: str = ""
    profile_id: str = ""


def _text(body: Mapping[str, object], key: str) -> str:
    value = body.get(key)
    return value.strip() if isinstance(value, str) else ""


def choice_from_metadata(metadata: str | None, default_provider: str) -> ModelChoice:
    """Pure. No or malformed metadata (a room joined by hand) = the environment."""
    try:
        body = json.loads(metadata) if metadata else None
    except ValueError:
        body = None
    if not isinstance(body, dict):
        body = {}
    config = body.get("config")
    if not isinstance(config, dict):
        config = {}
    provider = _text(config, "provider").lower() or default_provider
    if provider not in PROVIDERS:
        raise UnknownProvider(f"provider {provider!r} is not one of {', '.join(PROVIDERS)}")
    return ModelChoice(
        provider=provider,
        model=_text(config, "model"),
        voice=_text(config, "voice"),
        language=_text(config, "language"),
        profile_id=_text(body, "profile_id"),
    )


@dataclass(frozen=True)
class ProfileCredentials:
    provider: str  # "" = a legacy profile that talks through the environment provider
    api_key: str | None


def panel_key_for(choice: ModelChoice, creds: ProfileCredentials) -> str | None:
    """Pure. A stored key only ever goes to the provider it was stored for."""
    if creds.api_key is None:
        return None
    if creds.provider != choice.provider:
        raise CredentialsUnavailable(
            f"the profile's key is for {creds.provider or 'no provider'!r}, the session would use {choice.provider!r}"
        )
    return creds.api_key


def api_key_for(provider: str, panel_key: str | None, environ: Mapping[str, str]) -> str | None:
    """Pure. The profile's stored key wins; otherwise the provider's env key."""
    if panel_key:
        return panel_key
    return environ.get(ENV_KEYS[provider], "").strip() or None


async def fetch_profile_credentials(
    http: aiohttp.ClientSession, api_url: str, agent_secret: str, profile_id: str
) -> ProfileCredentials:
    """The profile's provider and the key the panel stored for it (None when it
    stored none). Any failure raises: the session must not start on a guessed key."""
    url = f"{api_url.rstrip('/')}/v1/agent-profiles/{profile_id}/credentials"
    try:
        async with http.get(
            url, headers={"X-Agent-Secret": agent_secret}, timeout=aiohttp.ClientTimeout(total=5)
        ) as res:
            if res.status != 200:
                raise CredentialsUnavailable(f"GET {url} → {res.status}")
            body = await res.json()
    except (aiohttp.ClientError, TimeoutError, ValueError) as e:
        raise CredentialsUnavailable(f"GET {url}: {e!r}") from e
    if not isinstance(body, dict):
        raise CredentialsUnavailable(f"GET {url}: unexpected body")
    key = body.get("apiKey")
    provider = body.get("provider")
    return ProfileCredentials(
        provider=provider if isinstance(provider, str) else "",
        api_key=key if isinstance(key, str) and key else None,
    )
