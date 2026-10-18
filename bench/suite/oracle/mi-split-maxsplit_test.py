import unittest
from more_itertools import split_at


class Oracle(unittest.TestCase):
    def test_maxsplit(self):
        odd = lambda n: n % 2 == 1
        self.assertEqual(list(split_at(range(10), odd, maxsplit=1)), [[0], [2, 3, 4, 5, 6, 7, 8, 9]])
        self.assertEqual(list(split_at(range(10), odd, maxsplit=2)), [[0], [2], [4, 5, 6, 7, 8, 9]])
        self.assertEqual(list(split_at(range(10), odd)), [[0], [2], [4], [6], [8], []])
        self.assertEqual(list(split_at(range(10), odd, maxsplit=0)), [list(range(10))])
        self.assertEqual(list(split_at('abcdcba', lambda x: x == 'b', maxsplit=1, keep_separator=True)), [['a'], ['b'], ['c', 'd', 'c', 'b', 'a']])


if __name__ == '__main__':
    unittest.main()
