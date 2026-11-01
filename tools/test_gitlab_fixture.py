import base64
import http.client
import io
import json
from pathlib import Path
import unittest
from unittest.mock import Mock, patch
import urllib.error
import urllib.parse

import gitlab_fixture as fixture


CONFIG = {"url": "https://gitlab.com", "project_id": "123", "fixture_ref": "main", "fixture_sha": "a" * 40,
          "runner_id": "456", "runner_tag": "acceptance"}
CANDIDATE, CREATED = "b" * 40, "c" * 40


class FixtureServer:
    """Exercise the actual HTTP request and parsing boundary without credentials."""
    def __init__(self, *, existing=False, fault="", scope=""):
        self.created, self.fault, self.posts, self.requests = existing, fault, [], []
        self.branch = "acceptance/" + CANDIDATE + scope
        self.scope = scope
        self.marker = (json.dumps({"schema_version": 1, "candidate_sha": CANDIDATE, "run_id": "10", "run_attempt": "1"},
                                  sort_keys=True, separators=(",", ":")) + "\n").encode()

    def open(self, request, timeout):
        self.requests.append(request)
        self.assert_request(request, timeout)
        path = request.full_url.removeprefix("https://gitlab.com/api/v4/projects/123")
        sha = CREATED if self.created else None
        if path == "":
            data = {"id": 123, "visibility": "public" if self.fault == "public" else "private"}
        elif path == "/repository/branches/main":
            data = {"name": "main", "commit": {"id": CREATED if self.fault == "moved-base" else CONFIG["fixture_sha"]}}
        elif path == "/repository/branches/" + urllib.parse.quote(self.branch, safe=""):
            if sha is None:
                self.error(request, 403 if self.fault == "denied" else 404)
            if self.created and self.fault == "lost-response-readback":
                self.error(request, 503)
            data = {"name": self.branch, "commit": {"id": sha}}
        elif path == "/repository/commits/" + CONFIG["fixture_sha"]:
            data = {"id": CONFIG["fixture_sha"]}
        elif path == "/repository/commits" and request.get_method() == "POST":
            body = json.loads(request.data)
            self.posts.append(body)
            if self.fault == "write-denied":
                self.error(request, 403)
            self.created = True
            if self.fault == "concurrent-conflict":
                self.error(request, 409)
            if self.fault in {"lost-response", "lost-response-readback"}:
                raise urllib.error.URLError("private endpoint and credential")
            data = {"id": CREATED}
        elif path == "/repository/commits/" + CREATED:
            data = {"id": CREATED, "parent_ids": [CONFIG["fixture_sha"]],
                    "message": "test: pin acceptance fixture for " + CANDIDATE + self.scope + "\n"}
            if self.fault == "wrong-parent":
                data["parent_ids"] = ["d" * 40]
            if self.fault == "wrong-message":
                data["message"] = "foreign write"
        elif path.startswith("/repository/tree?"):
            data = [{"path": name, "type": "blob", "mode": "100644", "id": "e" * 40} for name in fixture.FILES]
            data.append({"path": "preserved", "type": "tree", "mode": "040000", "id": "f" * 40})
            if self.scope and (path.endswith(CREATED) or self.fault == "marker-collision"):
                data.append({"path": fixture.SCOPE_MARKER, "type": "blob", "mode": "100644", "id": "1" * 40})
                if self.fault == "marker-mode":
                    data[-1]["mode"] = "100755"
            if path.endswith(CREATED):
                if self.fault == "wrong-extra-file":
                    data.append({"path": "extra", "type": "blob", "mode": "100644", "id": "d" * 40})
                if self.fault == "wrong-subtree":
                    next(entry for entry in data if entry["path"] == "preserved")["id"] = "d" * 40
                if self.fault == "wrong-mode":
                    data[0]["mode"] = "100755"
            if self.fault == "large-tree":
                data *= 34
        elif path.startswith("/repository/files/"):
            name = path.split("/")[3].split("?")[0]
            content = self.marker if name == fixture.SCOPE_MARKER else (Path("tests/fixtures/gitlab") / name).read_bytes()
            if name == fixture.SCOPE_MARKER and self.fault == "wrong-marker":
                content = b"foreign scope\n"
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
    def setUp(self):
        sleep = patch.object(fixture.time, "sleep")
        self.sleep = sleep.start()
        self.addCleanup(sleep.stop)

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

    def test_explicit_read_only_scope_never_creates_or_repairs_a_branch(self):
        for existing, fault in ((False, ""), (True, ""), (True, "wrong-marker"), (True, "wrong-parent"), (True, "wrong-subtree")):
            server = FixtureServer(existing=existing, fault=fault, scope="/10/1")
            with self.subTest(existing=existing, fault=fault), patch.object(fixture.urllib.request, "build_opener", return_value=server):
                if existing and not fault:
                    result = fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1", read_only=True)
                    self.assertEqual(result["fixture_sha"], CREATED)
                else:
                    with self.assertRaises(ValueError):
                        fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1", read_only=True)
            self.assertEqual(server.posts, [])

    def test_same_files_reuse_explicit_base_pin(self):
        server = FixtureServer(fault="already-current")
        self.assertEqual(self.prepare(server), CONFIG)
        self.assertEqual(server.posts, [])

    def test_run_scoped_refs_share_exact_pins_without_repeating_writes(self):
        for already_current in (False, True):
            with self.subTest(already_current=already_current):
                server = FixtureServer(fault="already-current" if already_current else "", scope="/10/1")
                with patch.object(fixture.urllib.request, "build_opener", return_value=server):
                    first = fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1")
                    second = fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1")
                self.assertEqual(first, second)
                self.assertEqual(first["fixture_ref"], "acceptance/" + CANDIDATE + "/10/1")
                self.assertEqual(first["fixture_sha"], CREATED)
                self.assertEqual(len(server.posts), 1)
                actions = server.posts[0]["actions"]
                marker = [a for a in actions if a["file_path"] == fixture.SCOPE_MARKER]
                self.assertEqual(len(marker), 1)
                self.assertEqual(marker[0]["action"], "create")
                self.assertEqual(base64.b64decode(marker[0]["content"]), server.marker)
                if already_current:
                    self.assertEqual(len(actions), 1)
                self.assertEqual(CONFIG["fixture_ref"], "main")

    def test_scoped_commit_reconciles_conflict_or_lost_response_without_another_post(self):
        for fault in ("concurrent-conflict", "lost-response"):
            with self.subTest(fault=fault):
                server = FixtureServer(fault=fault, scope="/10/1")
                with patch.object(fixture.urllib.request, "build_opener", return_value=server):
                    result = fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1")
                self.assertEqual(result["fixture_sha"], CREATED)
                self.assertEqual(len(server.posts), 1)

    def test_scoped_commit_rejects_marker_collision_or_tampering(self):
        for fault in ("marker-collision", "wrong-marker", "marker-mode", "wrong-parent", "wrong-subtree"):
            with self.subTest(fault=fault):
                server = FixtureServer(fault=fault, scope="/10/1")
                with patch.object(fixture.urllib.request, "build_opener", return_value=server), self.assertRaises(ValueError):
                    fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id="10", attempt="1")
                self.assertEqual(len(server.posts), 0 if fault == "marker-collision" else 1)

    def test_partial_or_malformed_run_scope_never_reaches_the_project(self):
        with patch.object(fixture.urllib.request, "build_opener") as opener:
            for run, attempt in (("10", None), (None, "1"), ("0", "1"), ("10/foreign", "1"), ("10", "01"), (str(2**63), "1")):
                with self.subTest(run=run, attempt=attempt), self.assertRaises(ValueError):
                    fixture.prepare(CONFIG, CANDIDATE, "synthetic-token", run_id=run, attempt=attempt)
            opener.assert_not_called()

    def test_unknown_write_outcome_is_read_back_without_repeating_post(self):
        server = FixtureServer(fault="lost-response")
        self.assertEqual(self.prepare(server)["fixture_sha"], CREATED)
        self.assertEqual(len(server.posts), 1)

    def test_uncertain_post_and_failed_readback_both_remain_observable(self):
        server = FixtureServer(fault="lost-response-readback")
        with self.assertRaises(fixture.GitLabError) as caught:
            self.prepare(server)
        self.assertEqual(len(server.posts), 1)
        diagnostic = caught.exception.diagnostic()
        self.assertEqual(diagnostic["write_outcome"], "unconfirmed")
        self.assertEqual([(r["method"], r["operation"], r["kind"], r["attempts"])
                          for r in diagnostic["requests"]], [("POST", "commit", "transport", 1), ("GET", "branch", "http", 3)])
        self.assertNotIn("private", json.dumps(diagnostic))

    def test_two_lane_fixture_paces_every_attempt_and_fresh_client(self):
        clock, attempts = [0.0], []

        def sleep(seconds):
            clock[0] += seconds

        def opened(request, timeout):
            attempts.append(clock[0])
            if len(attempts) == 1:
                raise urllib.error.URLError("synthetic transport loss")
            return io.BytesIO(b"{}")

        with patch.object(fixture.time, "monotonic", side_effect=lambda: clock[0]), \
                patch.object(fixture.time, "sleep", side_effect=sleep):
            for _ in range(2):
                client = fixture.GitLab({**CONFIG, "runners": {}}, "synthetic-token")
                client.opener.open = opened
                for _ in range(45):
                    client.request("")
        self.assertEqual(len(attempts), 91)
        self.assertEqual(attempts, [1.5 * i for i in range(1, 92)])

    def test_get_retries_only_transient_reads_and_remains_bounded(self):
        faults = [urllib.error.URLError(TimeoutError("private timeout")),
                  http.client.IncompleteRead(b"private response"),
                  *(urllib.error.HTTPError("https://private.example", status, "private", {}, None)
                    for status in (429, 502, 503, 504))]
        for fault in faults:
            with self.subTest(fault=type(fault).__name__):
                client = fixture.GitLab(CONFIG, "synthetic-token")
                client.opener = Mock()
                client.opener.open.side_effect = [fault, io.BytesIO(b"{}")]
                self.assertEqual(client.request(""), {})
                self.assertEqual(client.opener.open.call_count, 2)
        client.opener.open.reset_mock()
        client.opener.open.side_effect = urllib.error.URLError("private endpoint and token")
        self.sleep.reset_mock()
        with self.assertRaises(fixture.GitLabError) as caught:
            client.request("")
        self.assertEqual(client.opener.open.call_count, 3)
        self.assertEqual([call.args for call in self.sleep.call_args_list], [(1,), (2,)])
        self.assertEqual(caught.exception.diagnostic()["requests"][0]["attempts"], 3)
        self.assertNotIn("private", json.dumps(caught.exception.diagnostic()))

    def test_post_auth_redirect_and_validation_failures_are_not_retried(self):
        failures = [("GET", urllib.error.HTTPError("https://private.example", status, "private", {}, None))
                    for status in (301, 302, 400, 401, 403, 404, 500)]
        failures += [("POST", urllib.error.URLError("private endpoint")),
                     ("POST", urllib.error.HTTPError("https://private.example", 503, "private", {}, None))]
        for method, failure in failures:
            with self.subTest(method=method, status=getattr(failure, "code", 0)):
                self.sleep.reset_mock()
                client = fixture.GitLab(CONFIG, "synthetic-token")
                client.opener = Mock()
                client.opener.open.side_effect = failure
                with self.assertRaises(fixture.GitLabError) as caught:
                    client.request("/repository/commits", {} if method == "POST" else None)
                self.assertEqual(client.opener.open.call_count, 1)
                self.sleep.assert_not_called()
                self.assertNotIn("private", json.dumps(caught.exception.diagnostic()))
        for data in (b"private invalid JSON", b"x" * (128 * 1024 + 1)):
            client.opener.open.reset_mock()
            client.opener.open.side_effect = None
            client.opener.open.return_value = io.BytesIO(data)
            with self.assertRaises(fixture.GitLabError) as caught:
                client.request("")
            self.assertEqual(caught.exception.kind, "validation")
            self.assertEqual(client.opener.open.call_count, 1)

    def test_diagnostic_constructor_drops_unknown_labels_and_boolean_numbers(self):
        error = fixture.GitLabError(True, operation=["private"], method="private", attempts=True,
                                    transport=["private"], kind=["private"])
        diagnostic = error.diagnostic()
        self.assertEqual(diagnostic["requests"][0], {"operation": "unknown", "method": "unknown", "kind": "unknown",
                                                   "http_status": None, "transport": None, "attempts": 1})
        self.assertNotIn("private", str(error) + json.dumps(diagnostic))

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
