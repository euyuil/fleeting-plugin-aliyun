"""Package version ordering and input boundaries, without provider access."""
import unittest
from debian_package import debian_version


class DebianVersionTests(unittest.TestCase):
    def test_release_candidates_sort_before_final(self):
        self.assertEqual(debian_version("1.2.3-rc.1"), "1.2.3~rc.1")
        self.assertEqual(debian_version("1.2.3"), "1.2.3")

    def test_reject_unsafe_names(self):
        for version in ("../1.2.3", "1.2.3\nArchitecture: all", "v1.2.3", "1.2.3+dirty"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                debian_version(version)
