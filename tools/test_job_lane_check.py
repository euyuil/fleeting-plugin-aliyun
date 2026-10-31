"""Cloud-free checks must fail before writes and never disclose token bodies."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import unittest
from email.message import Message
from unittest.mock import patch
import urllib.error
import urllib.parse

from cloud_config import config_digest
import job_lane_check as check


def settings():
    config = {"url": "https://gitlab.com", "project_id": "1", "api_user_id": "7", "fixture_ref": "main", "fixture_sha": "a" * 40,
              "runners": {"instance": {"runner_id": "2", "runner_tag": "instance-lane"},
                          "docker-autoscaler": {"runner_id": "3", "runner_tag": "docker-lane"}}}
    env = {"RELEASE_GITLAB_CONFIG": json.dumps(config), "RELEASE_GITLAB_API_TOKEN": "synthetic-private-pat",
           "RELEASE_GITLAB_RUNNER_TOKEN": "synthetic-instance-token", "RELEASE_GITLAB_DOCKER_RUNNER_TOKEN": "synthetic-docker-token",
           "RELEASE_CLOUD_ACCEPTANCE_ENABLED": "false", "RELEASE_JOB_ACCEPTANCE_ENABLED": "false", "RELEASE_CLOUD_CLEANUP_ENABLED": "true",
           "GITHUB_REPOSITORY": "example/project"}
    return config, env


class Server:
    def __init__(self, fault=""):
        self.fault, self.requests, self.system_ids = fault, [], []

    def open(self, request, timeout):
        self.requests.append(request)
        assert 0 < timeout <= 30
        url = urllib.parse.urlparse(request.full_url)
        assert url.scheme == "https" and url.netloc == "gitlab.com"
        path, query = url.path.removeprefix("/api/v4"), urllib.parse.parse_qs(url.query)
        headers = {}
        if request.get_method() == "POST":
            assert path == "/runners/verify"
            assert request.get_header("Private-token") is None
            body = json.loads(request.data)
            self.system_ids.append(body["system_id"])
            if self.fault == "unknown-post":
                raise urllib.error.HTTPError(request.full_url, 502, "private-secret", {}, io.BytesIO(b"private-token-response"))
            identity = 2 if body["token"] == "synthetic-instance-token" else 3
            if self.fault == "swapped":
                identity = 5 - identity
            value = {"id": identity, "token": "private-token-response", "token_expires_at": "private-expiry"}
        else:
            assert request.get_header("Private-token") == "synthetic-private-pat"
            if self.fault == "retry-get" and len(self.requests) < 3:
                raise urllib.error.HTTPError(request.full_url, 503, "private-secret", {}, io.BytesIO(b"private-response"))
            if self.fault == "redirect":
                raise urllib.error.HTTPError(request.full_url, 302, "private-secret", {"Location": "https://foreign.invalid"}, io.BytesIO(b"private-response"))
            if path == "/projects/1":
                value = {"id": 1, "visibility": "private"}
            elif path == "/projects/1/runners":
                value = [{"id": 2}, {"id": 3}]
                if self.fault == "extra-runner":
                    value.append({"id": 4})
            elif path in {"/runners/2", "/runners/3"}:
                identity = int(path.rsplit("/", 1)[1])
                value = {"id": identity, "runner_type": "project_type", "locked": True, "paused": False, "run_untagged": False,
                         "tag_list": ["instance-lane" if identity == 2 else "docker-lane"], "projects": [{"id": 1, "name": "private-project"}]}
                if self.fault == "wrong-tag":
                    value["tag_list"] = ["foreign"]
                if self.fault == "shared-project":
                    value["projects"].append({"id": 9})
            elif path == "/projects/1/pipelines":
                assert query["status"][0] in check.ACTIVE
                value = []
                if self.fault == "child" and query.get("source") == ["parent_pipeline"]:
                    value = [{"id": 9}]
                if self.fault == "active-pipeline" and "source" not in query:
                    value = [{"id": 9}]
            elif path == "/projects/1/jobs":
                assert set(query["scope[]"]) == set(check.ACTIVE)
                value = [{"id": 9}] if self.fault == "active-job" else []
            else:
                raise AssertionError("unexpected configuration endpoint")
        if self.fault == "paged":
            headers["X-Next-Page"] = "2"
        if self.fault == "link-only" and path == "/projects/1/jobs":
            headers["Link"] = '<https://gitlab.com/private?page=2>; rel="next"'
        raw = json.dumps(value).encode()
        if self.fault == "oversized":
            raw = b" " * 65537
        if self.fault == "malformed":
            raw = b"private-token-response"
        response = io.BytesIO(raw)
        response.headers = headers
        return response


class LaneCheckTests(unittest.TestCase):
    def run_check(self, server, env=None, expected=None):
        config, default = settings()
        return check.verify(default if env is None else env, config_digest(config) if expected is None else expected, opener=server, sleep=lambda _: None)

    def test_configuration_and_credentials_with_stable_diagnostic_identities(self):
        first, second = Server(), Server()
        result = self.run_check(first)
        self.assertEqual(self.run_check(second), result)
        self.assertEqual(first.system_ids, second.system_ids)
        self.assertEqual(len(set(first.system_ids)), 2)
        self.assertTrue(all(identity.startswith("r_cfg") and len(identity) == 14 for identity in first.system_ids))
        self.assertEqual(sum(r.get_method() == "POST" for r in first.requests), 2)
        self.assertEqual(result["project_occupancy"], "EMPTY")
        self.assertNotIn("private", json.dumps(result))

    def test_wrong_flags_digest_or_missing_credential_send_nothing(self):
        for fault in ("digest", "cloud", "jobs", "cleanup", "docker-token"):
            with self.subTest(fault=fault):
                _, env = settings()
                expected = None
                if fault == "digest":
                    expected = "0" * 64
                elif fault == "docker-token":
                    env["RELEASE_GITLAB_DOCKER_RUNNER_TOKEN"] = ""
                else:
                    key = {"cloud": "RELEASE_CLOUD_ACCEPTANCE_ENABLED", "jobs": "RELEASE_JOB_ACCEPTANCE_ENABLED", "cleanup": "RELEASE_CLOUD_CLEANUP_ENABLED"}[fault]
                    env[key] = "true" if fault != "cleanup" else "false"
                server = Server()
                with self.assertRaises(ValueError):
                    self.run_check(server, env, expected)
                self.assertEqual(server.requests, [])

    def test_invalid_topology_or_incomplete_occupancy_prevents_authentication_posts(self):
        for fault in ("extra-runner", "wrong-tag", "shared-project", "child", "active-pipeline", "active-job", "paged", "link-only", "redirect", "oversized", "malformed"):
            with self.subTest(fault=fault):
                server = Server(fault)
                with self.assertRaises(ValueError):
                    self.run_check(server)
                self.assertTrue(all(r.get_method() == "GET" for r in server.requests))

    def test_gets_retry_but_unknown_verification_post_is_never_repeated(self):
        server = Server("retry-get")
        self.run_check(server)
        self.assertEqual(sum(r.full_url.endswith("/projects/1") for r in server.requests), 4)
        server = Server("unknown-post")
        with self.assertRaises(ValueError):
            self.run_check(server)
        self.assertEqual(sum(r.get_method() == "POST" for r in server.requests), 1)

    def test_swapped_token_fails_before_verifying_other_lane(self):
        server = Server("swapped")
        with self.assertRaises(ValueError):
            self.run_check(server)
        self.assertEqual(len(server.system_ids), 1)

    def test_late_final_response_cannot_turn_an_expired_budget_into_pass(self):
        clock = [0]
        server = Server()
        original = server.open
        def late(request, timeout):
            response = original(request, timeout)
            if "/projects/1/jobs?" in request.full_url and len(server.system_ids) == 2:
                original_read = response.read
                def read(size):
                    clock[0] = 181
                    return original_read(size)
                response.read = read
            return response
        server.open = late
        with patch.object(check.time, "monotonic", side_effect=lambda: clock[0]), self.assertRaises(ValueError):
            self.run_check(server)
        self.assertEqual(len(server.system_ids), 2)

    def test_pagination_recognizes_repeated_headers_and_relation_tokens(self):
        headers = Message()
        headers.add_header("Link", '<https://example.invalid?page=1>; rel="first"')
        self.assertFalse(check.next_page(headers))
        headers.add_header("Link", '<https://example.invalid?page=2>; rel="prev next"')
        self.assertTrue(check.next_page(headers))
        self.assertTrue(check.next_page({"Link": '<https://example.invalid?page=2>; rel=next'}))
        self.assertFalse(check.next_page({"Link": '<https://example.invalid?page=1>; rel="last"'}))

    def test_exception_and_token_response_bodies_never_reach_public_output(self):
        config, env = settings()
        for fault in ("", "unknown-post", "malformed"):
            with self.subTest(fault=fault), patch.dict(check.os.environ, env, clear=True), \
                    patch.object(check.urllib.request, "build_opener", return_value=Server(fault)), \
                    patch("sys.argv", ["job_lane_check.py", "--expected-config-sha256", config_digest(config)]), redirect_stdout(io.StringIO()) as output:
                status = check.main()
                self.assertEqual(status, 0 if not fault else 1)
                self.assertNotIn("private", output.getvalue())
                self.assertNotIn("synthetic", output.getvalue())

    def test_workflow_has_no_cloud_authorization_and_keeps_the_global_fence(self):
        text = Path(".github/workflows/job-config-check.yml").read_text()
        self.assertIn("permissions:\n  contents: read\n", text)
        self.assertNotIn("id-token:", text)
        self.assertNotIn("configure-aliyun", text)
        self.assertNotIn("ROLE_ARN", text)
        self.assertIn("group: aliyun-pre-release-acceptance", text)
        self.assertIn("cancel-in-progress: false", text)
        self.assertIn("environment: aliyun-pre-release", text)
        self.assertIn("ref: ${{ github.sha }}", text)
        self.assertIn("persist-credentials: false", text)
        self.assertNotIn("upload-artifact", text)


if __name__ == "__main__":
    unittest.main()
