"""Exercise publication refusal without credentials or network access."""
import os
import pathlib
import runpy
import subprocess
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).with_name("release-preflight.py")


class ReleasePreflightTests(unittest.TestCase):
    def run_preflight(self, release_status=404, tag_status=404):
        requests = []

        def invoke(command, **_kwargs):
            if command[0] == "go":
                return subprocess.CompletedProcess(command, 0)
            requests.append(command[2])
            status = tag_status if "/git/ref/" in command[2] else release_status
            return subprocess.CompletedProcess(command, 0 if status == 200 else 1,
                                               "HTTP/2.0 " + str(status) + " Test\n", "")

        with mock.patch.dict(os.environ, {"VERSION": "1.2.3", "GITHUB_REPOSITORY": "example/plugin"}, clear=True):
            with mock.patch("subprocess.run", side_effect=invoke):
                runpy.run_path(str(SCRIPT), run_name="__main__")
        return requests

    def test_new_release_and_tag_are_required(self):
        requests = self.run_preflight()
        self.assertEqual(len(requests), 2)
        self.assertTrue(requests[1].endswith("/git/ref/tags/v1.2.3"))

    def test_existing_source_tag_is_refused_without_release(self):
        with self.assertRaisesRegex(SystemExit, "Git tag already exists"):
            self.run_preflight(tag_status=200)

    def test_existing_release_is_refused(self):
        with self.assertRaisesRegex(SystemExit, "release already exists"):
            self.run_preflight(release_status=200)

    def test_authentication_and_service_failures_are_not_absence(self):
        for status in (401, 403, 500):
            with self.subTest(status=status):
                with self.assertRaisesRegex(SystemExit, "cannot verify"):
                    self.run_preflight(release_status=status)


if __name__ == "__main__":
    unittest.main()
