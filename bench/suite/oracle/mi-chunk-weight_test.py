import unittest
from more_itertools import chunked_by_weight


class Oracle(unittest.TestCase):
    def test_default_len(self):
        self.assertEqual(list(chunked_by_weight(['ab', 'c', 'def', 'gh', 'i'], 4)), [['ab', 'c'], ['def'], ['gh', 'i']])

    def test_custom_weight_and_heavy(self):
        self.assertEqual(list(chunked_by_weight([1, 2, 10, 3, 3, 3], 6, weight=lambda x: x)), [[1, 2], [10], [3, 3], [3]])

    def test_exact_fit(self):
        self.assertEqual(list(chunked_by_weight([2, 2, 2], 4, weight=lambda x: x)), [[2, 2], [2]])

    def test_empty_and_iterator(self):
        self.assertEqual(list(chunked_by_weight([], 3)), [])
        self.assertEqual(list(chunked_by_weight(iter(['a', 'b', 'c']), 2)), [['a', 'b'], ['c']])

    def test_invalid(self):
        with self.assertRaises(ValueError):
            list(chunked_by_weight(['a'], 0))


if __name__ == '__main__':
    unittest.main()
