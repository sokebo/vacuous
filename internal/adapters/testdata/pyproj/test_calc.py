import pytest

from calc import add


def test_add():
    assert add(1, 2) == 3


def test_skipped():
    # Skipped on purpose: a test that does not run proves nothing, so the
    # adapter has to report it as skipped rather than as a pass.
    pytest.skip("not ready yet")


def test_fails():
    assert add(1, 2) == 4
