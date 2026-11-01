import unittest


class Boom(unittest.TestCase):
    """A class-level error.

    It stops the suite before the first test runs, which is the shape that
    exits 5: unittest says "no tests ran", and the report carries the error
    in its failures section.
    """

    @classmethod
    def setUpClass(cls):
        raise RuntimeError("boom in setUpClass")

    def test_never_runs(self):
        self.fail("this test must never run")
