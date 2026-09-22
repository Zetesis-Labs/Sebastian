from wake_verify import is_clear_speech, matches_wake_word


def test_matches_the_name_however_whisper_spells_it() -> None:
    assert matches_wake_word("Okay Nabu")
    assert matches_wake_word("ok nabú, enciende la luz")
    assert matches_wake_word("oki naboo")


def test_a_sentence_without_the_name_is_not_a_match() -> None:
    assert not matches_wake_word("pues no sé qué decirte de todo esto")


# The board fired on a genuine "Okay Nabu" and Whisper answered in Devanagari
# ('प्रस्तुत करते हैं नाभू', 2026-09-22): the name was heard (नाभू = nabhu) but
# matches_wake_word only reads Latin, so calling this clear speech guarantees a
# REJECT. The documented policy is that a transcription error passes.
def test_non_latin_transcript_fails_open() -> None:
    hindi = "प्रस्तुत करते हैं नाभू"
    assert not matches_wake_word(hindi)
    assert not is_clear_speech(hindi), "must fall through to PASS, not REJECT"


def test_a_clear_spanish_sentence_is_still_clear_speech() -> None:
    assert is_clear_speech("de todo esto")
    assert is_clear_speech("enciende la luz del salón")


def test_short_or_empty_transcripts_are_not_clear_speech() -> None:
    assert not is_clear_speech("")
    assert not is_clear_speech("eh")
