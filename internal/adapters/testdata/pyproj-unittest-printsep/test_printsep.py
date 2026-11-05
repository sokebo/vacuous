import sys
import unittest


class PrintSepTest(unittest.TestCase):
    """A green suite that prints the shape of a failures section.

    The class prints the separator and a line that looks like a failure
    header to stderr. `tearDownClass` runs after the result lines are
    written, so the separator lands at the start of a line and the failures
    section is real in every way but the one that matters: no test went red,
    and the runner exits 0.
    """

    def test_ok(self):
        pass

    @classmethod
    def tearDownClass(cls):
        print("=" * 70, file=sys.stderr)
        print("FAIL: this line was printed by a green test", file=sys.stderr)
