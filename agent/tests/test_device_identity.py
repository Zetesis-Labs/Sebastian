from device_identity import device_identity_from_metadata


def test_metadata_names_the_unit():
    assert device_identity_from_metadata('{"device_id":"68ee8f4d8dd4","profile_id":"x"}', "legacy") == "68ee8f4d8dd4"


def test_missing_or_broken_metadata_falls_back():
    assert device_identity_from_metadata(None, "legacy") == "legacy"
    assert device_identity_from_metadata("", "legacy") == "legacy"
    assert device_identity_from_metadata("not json", "legacy") == "legacy"
    assert device_identity_from_metadata("[1,2]", "legacy") == "legacy"
    assert device_identity_from_metadata('{"device_id":"  "}', "legacy") == "legacy"
    assert device_identity_from_metadata('{"device_id":7}', "legacy") == "legacy"
