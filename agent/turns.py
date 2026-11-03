"""User turns rebuilt from the chat history.

Phonic delivers the user's transcript word by word and the plugin stores each
word as its own message, while Gemini and OpenAI store one message per turn.
A turn is therefore every user message up to the next item of any other kind.
"""
from __future__ import annotations

from collections.abc import Iterable


def user_turns(history: Iterable[tuple[str | None, str]]) -> list[str]:
    turns: list[str] = []
    current: list[str] = []
    for role, text in history:
        if role != "user":
            if current:
                turns.append(" ".join(current))
                current = []
            continue
        fragment = text.strip()
        if fragment:
            current.append(fragment)
    if current:
        turns.append(" ".join(current))
    return turns
