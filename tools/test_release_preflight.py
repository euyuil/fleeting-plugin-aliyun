"""Exercise publication refusal without credentials or network access."""
import contextlib
import json
from pathlib import Path
import textwrap
import os
import pathlib
import runpy
import subprocess
import tempfile
import unittest
from unittest import mock
from unittest.mock import patch

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


class PreparedTagTests(unittest.TestCase):
    def preflight(self, expected=None, tag_sha=None, tag_type="commit", released=False, tag_status=200):
        environment = {"VERSION": "0.1.0-rc.4", "GITHUB_REPOSITORY": "example/plugin"}
        if expected is not None:
            environment["CANDIDATE_SHA"] = expected
        body = json.dumps({"object": {"sha": tag_sha, "type": tag_type}})
        responses = [subprocess.CompletedProcess([], 0),
                     subprocess.CompletedProcess([], 0 if released else 1, "HTTP/2 200 OK\n\n{}" if released else "HTTP/2 404 Not Found\n\n{}"),
                     subprocess.CompletedProcess([], 0 if tag_status == 200 else 1, f"HTTP/2 {tag_status} Status\n\n{body}")]
        with patch.dict(os.environ, environment, clear=True), patch.object(Path, "read_text", return_value="# 0.1.0-rc.4\n"), patch("subprocess.run", side_effect=responses):
            runpy.run_path(str(Path(__file__).with_name("release-preflight.py")), run_name="__main__")

    def test_only_exact_bound_commit_tag_is_accepted(self):
        self.preflight("a" * 40, "a" * 40)
        for expected, target, kind in [(None, "a" * 40, "commit"), ("a" * 40, "b" * 40, "commit"), ("a" * 40, "a" * 40, "tag")]:
            with self.subTest(expected=expected, target=target, kind=kind), self.assertRaises(SystemExit):
                self.preflight(expected, target, kind)

    def test_existing_release_and_unknown_read_still_block(self):
        with self.assertRaises(SystemExit):
            self.preflight("a" * 40, "a" * 40, released=True)
        with self.assertRaises(SystemExit):
            self.preflight("a" * 40, tag_status=503)
        self.preflight(tag_status=404)

    def test_matching_remote_tag_needs_no_write_and_other_target_fails(self):
        workflow = Path(".github/workflows/release.yml").read_text()
        step = workflow.split("      - name: Create the exact source tag\n", 1)[1].split("\n      - name:", 1)[0]
        script = textwrap.dedent(step.partition("run: |\n")[2])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, stderr=subprocess.PIPE, text=True).strip()
            git("init", "--quiet")
            git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Fixture")
            sha = git("rev-parse", "HEAD")
            git("tag", "v0.1.0-rc.4")
            git("remote", "add", "origin", ".")
            git("remote", "set-url", "--push", "origin", str(root / "missing-push-repository"))
            env = {**os.environ, "VERSION": "0.1.0-rc.4", "CANDIDATE_SHA": sha}
            subprocess.run(["bash", "-e", "-o", "pipefail", "-c", script], cwd=root, env=env, check=True, capture_output=True)
            self.assertEqual(git("rev-parse", "refs/tags/v0.1.0-rc.4"), sha)
            env["CANDIDATE_SHA"] = "b" * 40
            self.assertNotEqual(subprocess.run(["bash", "-e", "-o", "pipefail", "-c", script], cwd=root, env=env, capture_output=True).returncode, 0)


if __name__ == "__main__":
    unittest.main()
