import unittest
from more_itertools import rstrip, strip


class Oracle(unittest.TestCase):
    def test_rstrip(self):
        pred = lambda x: x in {None, False, ''}
        it = (None, False, None, 1, 2, None, 3, False, None)
        self.assertEqual(list(rstrip(it, pred)), [None, False, None, 1, 2, None, 3])
        self.assertEqual(list(rstrip([0, 1, 0, 2, 0, 0, 3, 0], lambda x: x == 0)), [0, 1, 0, 2, 0, 0, 3])
        self.assertEqual(list(strip([0, 1, 0, 0, 2, 0], lambda x: x == 0)), [1, 0, 0, 2])
        self.assertEqual(list(rstrip([], bool)), [])


if __name__ == '__main__':
    unittest.main()
