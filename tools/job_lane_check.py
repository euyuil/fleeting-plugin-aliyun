"""Verify private lane configuration and authentication without cloud access."""
import argparse
import hashlib
import http.client
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

from cloud_config import config_digest, decode
from gitlab_fixture import NoRedirect, RETRY_STATUSES
from job_acceptance import EXECUTORS, gitlab_configuration

ACTIVE = ("created", "waiting_for_resource", "preparing", "waiting_for_callback", "pending", "running", "canceling", "manual", "scheduled")


class CheckFailed(ValueError):
    pass


def next_page(headers):
    def values(name):
        return headers.get_all(name, []) if hasattr(headers, "get_all") else [headers.get(name, "")]
    if any(values("X-Next-Page")):
        return True
    for link in values("Link"):
        for match in re.finditer(r'(?:^|;)\s*rel\s*=\s*(?:"([^"]*)"|([^;,\s]+))', link, re.I):
            relation = match[1] if match[1] is not None else match[2]
            if "next" in relation.lower().split():
                return True
    return False


class Client:
    def __init__(self, config, token, *, opener=None, sleep=time.sleep):
        self.config, self.token = config, token
        self.project = "/projects/" + config["project_id"]
        self.opener = opener if opener is not None else urllib.request.build_opener(NoRedirect())
        self.sleep, self.deadline = sleep, time.monotonic() + 180

    def request(self, path, *, body=None):
        method = "GET" if body is None else "POST"
        maximum = 3 if method == "GET" else 1
        allowed_get = (path == self.project or path == self.project + "/runners?per_page=100" or
                       path in {"/runners/" + r["runner_id"] for r in self.config["runners"].values()} or
                       path.startswith(self.project + "/pipelines?") or path.startswith(self.project + "/jobs?"))
        if method == "GET" and not allowed_get or method == "POST" and path != "/runners/verify":
            raise CheckFailed("unsupported configuration check endpoint")
        headers = {"Accept": "application/json"}
        data = None
        if method == "GET":
            headers["PRIVATE-TOKEN"] = self.token
        else:
            # Authentication responses can contain the token. Never forward
            # this body/response to stdout, an artifact or an exception message.
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        request = urllib.request.Request("https://gitlab.com/api/v4" + path, data=data, headers=headers)
        for attempt in range(maximum):
            remaining = self.deadline - time.monotonic()
            if remaining <= 0:
                raise CheckFailed("configuration check deadline elapsed")
            try:
                with self.opener.open(request, timeout=min(30, remaining)) as response:
                    raw = response.read(65537)
                    if time.monotonic() >= self.deadline:
                        raise CheckFailed("configuration request budget elapsed")
                    if len(raw) > 65536 or next_page(response.headers):
                        raise CheckFailed("configuration inventory is incomplete or oversized")
                    return json.loads(raw)
            except urllib.error.HTTPError as error:
                retry = method == "GET" and error.code in RETRY_STATUSES
                error.close()
            except (urllib.error.URLError, TimeoutError, OSError, http.client.HTTPException):
                retry = method == "GET"
            if not retry or attempt + 1 == maximum:
                raise CheckFailed("configuration API request failed") from None
            self.sleep(min(2**attempt, max(0, self.deadline - time.monotonic())))
        raise CheckFailed("configuration API request failed")

    def rows(self, path):
        rows = self.request(path)
        if not isinstance(rows, list) or len(rows) >= 100:
            raise CheckFailed("configuration list exceeds its complete bound")
        return rows

    def topology(self):
        project = self.request(self.project)
        if not isinstance(project, dict) or str(project.get("id")) != self.config["project_id"] or project.get("visibility") != "private":
            raise CheckFailed("expected private project is unavailable")
        available = self.rows(self.project + "/runners?per_page=100")
        expected = {int(r["runner_id"]) for r in self.config["runners"].values()}
        if len(available) != 2 or any(not isinstance(r, dict) or type(r.get("id")) is not int for r in available) or {r["id"] for r in available} != expected:
            raise CheckFailed("project must contain exactly its two pinned Runners")
        for runner in self.config["runners"].values():
            actual = self.request("/runners/" + runner["runner_id"])
            projects = actual.get("projects") if isinstance(actual, dict) else None
            if (not isinstance(actual, dict) or str(actual.get("id")) != runner["runner_id"] or actual.get("runner_type") != "project_type" or
                    actual.get("locked") is not True or actual.get("paused") is not False or actual.get("run_untagged") is not False or
                    actual.get("tag_list") != [runner["runner_tag"]] or not isinstance(projects, list) or len(projects) != 1 or
                    not isinstance(projects[0], dict) or str(projects[0].get("id")) != self.config["project_id"]):
                raise CheckFailed("Runner differs from its locked project and lane tag")

    def empty(self):
        for state in ACTIVE:
            path = self.project + "/pipelines?status=" + state + "&per_page=100"
            if self.rows(path) or self.rows(path + "&source=parent_pipeline"):
                raise CheckFailed("project has nonterminal pipelines")
        query = urllib.parse.urlencode({"per_page": "100", "scope[]": ACTIVE}, doseq=True)
        if self.rows(self.project + "/jobs?" + query):
            raise CheckFailed("project has nonterminal jobs")


def verify(env, expected_digest, *, opener=None, sleep=time.sleep):
    if not isinstance(expected_digest, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_digest):
        raise CheckFailed("expected canonical configuration digest is required")
    if any(env.get(k) != v for k, v in {"RELEASE_CLOUD_ACCEPTANCE_ENABLED": "false", "RELEASE_JOB_ACCEPTANCE_ENABLED": "false",
                                      "RELEASE_CLOUD_CLEANUP_ENABLED": "true"}.items()):
        raise CheckFailed("configuration checks require disabled creation and enabled cleanup")
    config = gitlab_configuration(decode("RELEASE_GITLAB_CONFIG", env))
    if config_digest(config) != expected_digest:
        raise CheckFailed("actual Environment configuration differs from the expected digest")
    repository = env.get("GITHUB_REPOSITORY", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise CheckFailed("repository identity is required")
    names = {"instance": "RELEASE_GITLAB_RUNNER_TOKEN", "docker-autoscaler": "RELEASE_GITLAB_DOCKER_RUNNER_TOKEN"}
    if any(not isinstance(env.get(k), str) or not env[k] for k in ("RELEASE_GITLAB_API_TOKEN", *names.values())):
        raise CheckFailed("existing project and lane credentials are required")
    client = Client(config, env["RELEASE_GITLAB_API_TOKEN"], opener=opener, sleep=sleep)
    # Complete all local and GET-only gates before either authentication POST.
    client.topology()
    client.empty()
    for executor in EXECUTORS:
        # This stable diagnostic identity can create/heartbeat a logical Manager
        # record. It never starts a Runner process, job or compute resource, and
        # it never overwrites an old acceptance Manager's contact metadata.
        system_id = "r_cfg" + hashlib.sha256(("fleeting-config-check/v1/" + repository + "/" + executor).encode()).hexdigest()[:9]
        response = client.request("/runners/verify", body={"token": env[names[executor]], "system_id": system_id})
        valid = isinstance(response, dict) and type(response.get("id")) is int and str(response["id"]) == config["runners"][executor]["runner_id"]
        del response
        if not valid:
            raise CheckFailed("Runner authentication differs from its pinned lane")
    client.topology()
    client.empty()
    if time.monotonic() >= client.deadline:
        raise CheckFailed("configuration request budget elapsed")
    return {"configuration": "PASS", "runner_authentication": "PASS", "project_occupancy": "EMPTY", "cloud_access": "NOT_REQUESTED"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-config-sha256", required=True)
    args = parser.parse_args()
    try:
        result = verify(os.environ, args.expected_config_sha256)
    except Exception:
        # API bodies, malformed responses and exception strings can contain
        # credentials or identifiers, including the token returned by verify.
        print(json.dumps({"configuration_authentication_check": "FAIL"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
