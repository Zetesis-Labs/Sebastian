from playback import stop_reply


def test_the_agent_confirms_the_music_stopped():
    assert stop_reply(204) == "Música parada."


def test_nothing_playing_is_said_as_such():
    assert stop_reply(404) == "No está sonando nada."


def test_a_failed_stop_is_not_reported_as_done():
    for status in (0, 401, 503):
        assert stop_reply(status) == "No he podido parar la música."
