from text_match import containment, looks_spanish, norm, shingles


def test_norm_lowercases_strips_accents_and_punctuation():
    assert norm("¡Hóla, Múndo!").split() == ["hola", "mundo"]
    assert norm("Árbol ñandú").split() == ["arbol", "nandu"]


def test_shingles_uses_bare_words_below_three():
    assert shingles("hola mundo") == {"hola", "mundo"}
    assert shingles("uno") == {"uno"}
    assert shingles("") == set()


def test_shingles_three_word_windows():
    assert shingles("a b c d") == {"a b c", "b c d"}


def test_containment_full_partial_and_empty():
    assert containment("enciende la luz del salon", "enciende la luz del salon") == 1.0
    assert containment("", "cualquier cosa") == 0.0
    partial = containment("enciende la luz ya", "enciende la luz del salon")
    assert 0.0 < partial < 1.0


def test_looks_spanish():
    assert looks_spanish("enciende la luz por favor")
    assert not looks_spanish("turn on the living room lights please")
    assert looks_spanish("hola")  # too short to judge → benefit of the doubt
    assert not looks_spanish("привет как дела")  # cyrillic → not Latin
