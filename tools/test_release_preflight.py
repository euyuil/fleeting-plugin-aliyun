"""Exercise publication refusal without credentials or network access."""
import contextlib
import os
import pathlib
import runpy
import subprocess
import tempfile
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).with_name("release-preflight.py")


class ReleasePreflightTests(unittest.TestCase):
    def run_preflight(self, release_status=404, tag_status=404, version="1.2.3", notes="# 1.2.3\n\nChanges.\n"):
        requests = []
        self.commands = []

        def invoke(command, **_kwargs):
            self.commands.append(command)
            if command[0] == "go":
                return subprocess.CompletedProcess(command, 0)
            requests.append(command[2])
            status = tag_status if "/git/ref/" in command[2] else release_status
            return subprocess.CompletedProcess(command, 0 if status == 200 else 1,
                                               "HTTP/2.0 " + str(status) + " Test\n", "")

        with tempfile.TemporaryDirectory() as directory:
            docs = pathlib.Path(directory) / "docs"
            docs.mkdir()
            (docs / "release-notes.md").write_text(notes)
            with contextlib.chdir(directory), mock.patch.dict(os.environ, {
                "VERSION": version, "GITHUB_REPOSITORY": "example/plugin",
            }, clear=True), mock.patch("subprocess.run", side_effect=invoke):
                runpy.run_path(str(SCRIPT), run_name="__main__")
        return requests

    def test_matching_prerelease_notes_are_accepted(self):
        requests = self.run_preflight(version="1.2.3-rc.1", notes="# 1.2.3-rc.1\n\nPreview.\n")
        self.assertTrue(requests[0].endswith("/releases/tags/v1.2.3-rc.1"))

    def test_wrong_or_missing_notes_title_fails_before_commands(self):
        for notes in ("# 1.2.2\n", "", "Changes without a version.\n", "## 1.2.3\n"):
            with self.subTest(notes=notes), self.assertRaisesRegex(SystemExit, "release notes"):
                self.run_preflight(notes=notes)
            self.assertEqual(self.commands, [])

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
