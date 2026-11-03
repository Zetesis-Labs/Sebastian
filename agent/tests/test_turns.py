"""User turns as the user spoke them, whatever the realtime model's transcript granularity."""
from meeting_mode import voice_start_asked
from turns import user_turns


def test_one_message_per_turn_is_kept_as_is():
    history = [("user", "¿Hablas?"), ("assistant", "Claro."), ("user", "Graba la reunión")]
    assert user_turns(history) == ["¿Hablas?", "Graba la reunión"]


def test_consecutive_user_messages_are_one_turn():
    history = [
        ("assistant", "¿En qué te ayudo?"),
        ("user", "Graba"),
        ("user", "la"),
        ("user", "reunión,"),
        ("user", "por"),
        ("user", "favor."),
    ]
    assert user_turns(history) == ["Graba la reunión, por favor."]


def test_an_assistant_reply_or_tool_call_ends_the_turn():
    history = [
        ("user", "Hola"),
        ("user", "Sebastián"),
        ("assistant", "Hola."),
        ("user", "Cuéntame"),
        (None, ""),
        ("user", "un chiste"),
    ]
    assert user_turns(history) == ["Hola Sebastián", "Cuéntame", "un chiste"]


def test_empty_fragments_do_not_add_spaces():
    assert user_turns([("user", " "), ("user", "Graba"), ("user", ""), ("user", "esto")]) == ["Graba esto"]


def test_no_user_messages():
    assert user_turns([("assistant", "Hola.")]) == []


def test_word_by_word_recording_request_is_honoured():
    words = ["Graba", "la", "reunión,", "por", "favor."]
    history = [("assistant", "Dime.")] + [("user", w) for w in words]
    assert voice_start_asked(user_turns(history))
