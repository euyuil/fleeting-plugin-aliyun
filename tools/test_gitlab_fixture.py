import base64
import io
import json
from pathlib import Path
import unittest
from unittest.mock import Mock, patch
import urllib.error

import gitlab_fixture as fixture


CONFIG = {"url": "https://gitlab.com", "project_id": "123", "fixture_ref": "main", "fixture_sha": "a" * 40,
          "runner_id": "456", "runner_tag": "acceptance"}
CANDIDATE, CREATED = "b" * 40, "c" * 40


class FixtureServer:
    """Exercise the actual HTTP request and parsing boundary without credentials."""
    def __init__(self, *, existing=False, fault=""):
        self.created, self.fault, self.posts, self.requests = existing, fault, [], []

    def open(self, request, timeout):
        self.requests.append(request)
        self.assert_request(request, timeout)
        path = request.full_url.removeprefix("https://gitlab.com/api/v4/projects/123")
        sha = CREATED if self.created else None
        if path == "":
            data = {"id": 123, "visibility": "public" if self.fault == "public" else "private"}
        elif path == "/repository/branches/main":
            data = {"name": "main", "commit": {"id": CREATED if self.fault == "moved-base" else CONFIG["fixture_sha"]}}
        elif path == "/repository/branches/acceptance%2F" + CANDIDATE:
            if sha is None:
                self.error(request, 403 if self.fault == "denied" else 404)
            data = {"name": "acceptance/" + CANDIDATE, "commit": {"id": sha}}
        elif path == "/repository/commits" and request.get_method() == "POST":
            body = json.loads(request.data)
            self.posts.append(body)
            if self.fault == "write-denied":
                self.error(request, 403)
            self.created = True
            if self.fault == "lost-response":
                raise urllib.error.URLError("private endpoint and credential")
            data = {"id": CREATED}
        elif path == "/repository/commits/" + CREATED:
            data = {"id": CREATED, "parent_ids": [CONFIG["fixture_sha"]],
                    "message": "test: pin acceptance fixture for " + CANDIDATE + "\n"}
            if self.fault == "wrong-parent":
                data["parent_ids"] = ["d" * 40]
            if self.fault == "wrong-message":
                data["message"] = "foreign write"
        elif path.startswith("/repository/tree?"):
            data = [{"path": name, "type": "blob", "mode": "100644", "id": "e" * 40} for name in fixture.FILES]
            data.append({"path": "preserved", "type": "tree", "mode": "040000", "id": "f" * 40})
            if path.endswith(CREATED):
                if self.fault == "wrong-extra-file":
                    data.append({"path": "extra", "type": "blob", "mode": "100644", "id": "d" * 40})
                if self.fault == "wrong-subtree":
                    data[-1]["id"] = "d" * 40
                if self.fault == "wrong-mode":
                    data[0]["mode"] = "100755"
            if self.fault == "large-tree":
                data *= 34
        elif path.startswith("/repository/files/"):
            name = path.split("/")[3].split("?")[0]
            content = (Path("tests/fixtures/gitlab") / name).read_bytes()
            if path.endswith(CONFIG["fixture_sha"]) and self.fault != "already-current":
                content = b"old fixture\n"
            if path.endswith(CREATED) and self.fault == "wrong-content":
                content = b"foreign content\n"
            return io.BytesIO(content)
        else:
            raise AssertionError("unexpected synthetic API path")
        return io.BytesIO(json.dumps(data).encode())

    @staticmethod
    def assert_request(request, timeout):
        assert request.get_header("Private-token") == "synthetic-token"
        assert timeout == 30

    @staticmethod
    def error(request, status):
        raise urllib.error.HTTPError(request.full_url, status, "private details", {}, io.BytesIO(b"private body"))


class FixtureTests(unittest.TestCase):
    def prepare(self, server):
        with patch.object(fixture.urllib.request, "build_opener", return_value=server):
            return fixture.prepare(CONFIG, CANDIDATE, "synthetic-token")

    def test_atomic_creation_and_exact_readback(self):
        server = FixtureServer()
        result = self.prepare(server)
        self.assertEqual(result, {**CONFIG, "fixture_ref": "acceptance/" + CANDIDATE, "fixture_sha": CREATED})
        self.assertEqual(len(server.posts), 1)
        body = server.posts[0]
        self.assertEqual(set(body), {"branch", "start_sha", "commit_message", "actions"})
        self.assertEqual(body["start_sha"], CONFIG["fixture_sha"])
        self.assertEqual({a["file_path"] for a in body["actions"]}, set(fixture.FILES))
        for action in body["actions"]:
            self.assertEqual(action["action"], "update")
            self.assertEqual(base64.b64decode(action["content"]), (Path("tests/fixtures/gitlab") / action["file_path"]).read_bytes())
        self.assertEqual(CONFIG["fixture_ref"], "main")

    def test_existing_identical_branch_is_read_only(self):
        server = FixtureServer(existing=True)
        self.assertEqual(self.prepare(server)["fixture_sha"], CREATED)
        self.assertEqual(server.posts, [])

    def test_same_files_reuse_explicit_base_pin(self):
        server = FixtureServer(fault="already-current")
        self.assertEqual(self.prepare(server), CONFIG)
        self.assertEqual(server.posts, [])

    def test_unknown_write_outcome_is_read_back_without_repeating_post(self):
        server = FixtureServer(fault="lost-response")
        self.assertEqual(self.prepare(server)["fixture_sha"], CREATED)
        self.assertEqual(len(server.posts), 1)

    def test_foreign_or_unavailable_fixture_fails_closed(self):
        for fault in ("public", "moved-base", "denied", "write-denied", "wrong-parent", "wrong-message", "wrong-content",
                      "wrong-extra-file", "wrong-subtree", "wrong-mode", "large-tree"):
            with self.subTest(fault=fault), self.assertRaises(ValueError):
                server = FixtureServer(existing=fault.startswith("wrong-"), fault=fault)
                try:
                    self.prepare(server)
                finally:
                    self.assertEqual(len(server.posts), int(fault == "write-denied"))

    def test_credential_endpoint_response_and_redirect_limits(self):
        with self.assertRaises(ValueError):
            fixture.GitLab({**CONFIG, "url": "https://foreign.example"}, "synthetic-token")
        self.assertIsNone(fixture.NoRedirect().redirect_request(None, None, 302, "", {}, "https://foreign.example"))
        for failure in (urllib.error.URLError("private endpoint"),
                        urllib.error.HTTPError("https://private.example", 403, "private details", {}, None)):
            client = fixture.GitLab(CONFIG, "synthetic-token")
            client.opener = Mock()
            client.opener.open.side_effect = failure
            with self.assertRaises(fixture.GitLabError) as caught:
                client.request("")
            self.assertNotIn("private", str(caught.exception))
            self.assertNotIn("synthetic-token", str(caught.exception))
        for data in (b"private invalid JSON", b"x" * (128 * 1024 + 1)):
            client = fixture.GitLab(CONFIG, "synthetic-token")
            client.opener = Mock()
            client.opener.open.return_value = io.BytesIO(data)
            with self.assertRaises(ValueError):
                client.request("")
