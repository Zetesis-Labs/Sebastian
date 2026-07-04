from unittest.mock import patch
from phantom import PhantomDetector

def test_phantom_detector_normal_conversation() -> None:
    # A normal conversation where user doesn't repeat the agent
    pd = PhantomDetector()
    
    # Agent speaks
    pd.on_state("speaking")
    pd.check("assistant", "Hola, ¿cómo estás?")
    pd.on_state("listening")
    
    # User speaks normally (not an echo)
    with patch("phantom.m_phantom") as mock_phantom:
        pd.check("user", "Muy bien, gracias")
        mock_phantom.add.assert_not_called()

def test_phantom_detector_literal_echo() -> None:
    # Echo: user "says" exactly what the agent just said
    pd = PhantomDetector()
    
    # Agent speaks
    pd.on_state("speaking")
    pd.check("assistant", "Apagando las luces")
    pd.on_state("listening")
    
    # Echo comes right back
    with patch("phantom.m_phantom") as mock_phantom:
        pd.check("user", "Apagando las luces")
        mock_phantom.add.assert_called_once_with(1, {"reason": "overlap"})

def test_phantom_detector_during_speech() -> None:
    # Echo: user "says" something non-sensical while the agent is still speaking
    pd = PhantomDetector()
    
    pd.on_state("speaking")
    
    with patch("phantom.m_phantom") as mock_phantom, \
         patch("phantom.m_user_during_speech") as mock_during_speech:
        pd.check("user", "turn on the living room lights please") # English (fails looks_spanish)
        mock_during_speech.add.assert_called_once_with(1)
        # Fails both language and during_speech
        mock_phantom.add.assert_called_once_with(1, {"reason": "during_speech"})
