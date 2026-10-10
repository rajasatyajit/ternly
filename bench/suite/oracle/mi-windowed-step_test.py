import unittest
from more_itertools import windowed


class Oracle(unittest.TestCase):
    def test_windowed_step(self):
        self.assertEqual(list(windowed(range(6), 3, step=3)), [(0, 1, 2), (3, 4, 5)])
        self.assertEqual(list(windowed(range(7), 3, step=3)), [(0, 1, 2), (3, 4, 5), (6, None, None)])
        self.assertEqual(list(windowed(range(5), 2, step=3)), [(0, 1), (3, 4)])
        self.assertEqual(list(windowed(range(8), 2, step=4, fillvalue='x')), [(0, 1), (4, 5)])
        self.assertEqual(list(windowed(range(6), 3, step=2)), [(0, 1, 2), (2, 3, 4), (4, 5, None)])
        self.assertEqual(list(windowed(range(4), 2)), [(0, 1), (1, 2), (2, 3)])
        self.assertEqual(list(windowed([], 3, step=3)), [])


if __name__ == '__main__':
    unittest.main()
