"""Pin the reviewed job fixture through the dedicated project's existing PAT."""
import base64
import http.client
import json
from pathlib import Path
import re
import time
import urllib.error
import urllib.parse
import urllib.request

from release_candidate import revision

FILES = (".gitlab-ci.yml", "smoke.sh")
SCOPE_MARKER = ".acceptance-scope.json"
OPERATIONS = {"project", "branch", "tree", "file", "commit", "unknown"}
RETRY_STATUSES = {429, 502, 503, 504}


class GitLabError(ValueError):
    def __init__(self, status=0, *, operation="unknown", method="unknown", attempts=1,
                 transport="other", kind=None):
        valid = type(status) is int and (status == 0 or 100 <= status <= 599)
        self.status = status if valid else 0
        self.operation = operation if isinstance(operation, str) and operation in OPERATIONS else "unknown"
        self.method = method if isinstance(method, str) and method in {"GET", "POST"} else "unknown"
        self.attempts = attempts if type(attempts) is int and 1 <= attempts <= 3 else 1
        self.kind = kind if isinstance(kind, str) and kind in {"validation", "unknown"} else (
            "http" if valid and status else "transport" if valid else "unknown")
        self.transport = transport if isinstance(transport, str) and transport in {"timeout", "connection", "incomplete_response", "other"} else "other"
        self.prior_write = None
        super().__init__("GitLab fixture request failed")

    def request_diagnostic(self):
        return {"operation": self.operation, "method": self.method, "kind": self.kind,
                "http_status": self.status if self.kind == "http" else None,
                "transport": self.transport if self.kind == "transport" else None,
                "attempts": self.attempts}

    def diagnostic(self):
        records = [self.prior_write.request_diagnostic()] if isinstance(self.prior_write, GitLabError) else []
        records.append(self.request_diagnostic())
        return {"requests": records, "write_outcome": "unconfirmed" if self.prior_write is not None or self.method == "POST" else "not_reported"}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # A project credential must never follow a redirect to another host.
        return None


class GitLab:
    def __init__(self, config, token):
        if config["url"] != "https://gitlab.com" or not re.fullmatch(r"[1-9][0-9]*", config["project_id"]) or not token:
            raise ValueError("invalid dedicated GitLab endpoint or credential")
        self.base = config["url"] + "/api/v4/projects/" + config["project_id"]
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, path, body=None, *, raw=False):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data,
                                         headers={"PRIVATE-TOKEN": self.token, "Content-Type": "application/json"})
        method = request.get_method()
        operation = next((label for prefix, label in (("/repository/branches/", "branch"),
            ("/repository/tree", "tree"), ("/repository/files/", "file"), ("/repository/commits", "commit"))
            if path.startswith(prefix)), "project" if path == "" else "unknown")
        maximum = 3 if method == "GET" else 1
        for attempt in range(1, maximum + 1):
            try:
                with self.opener.open(request, timeout=30) as response:
                    data = response.read(128 * 1024 + 1)
                break
            except urllib.error.HTTPError as error:
                status = error.code
                error.close()
                failure = GitLabError(status, operation=operation, method=method, attempts=attempt)
                retry = type(status) is int and status in RETRY_STATUSES
            except (OSError, urllib.error.URLError, http.client.IncompleteRead) as error:
                reason = error.reason if isinstance(error, urllib.error.URLError) else error
                transport = "incomplete_response" if isinstance(error, http.client.IncompleteRead) else (
                    "timeout" if isinstance(reason, TimeoutError) else "connection")
                failure = GitLabError(operation=operation, method=method, attempts=attempt, transport=transport)
                retry = True
            if not retry or attempt == maximum:
                raise failure from None
            time.sleep(attempt)
        if len(data) > 128 * 1024:
            raise GitLabError(operation=operation, method=method, attempts=attempt, kind="validation")
        if raw:
            return data
        try:
            return json.loads(data)
        except (ValueError, UnicodeError):
            raise GitLabError(operation=operation, method=method, attempts=attempt, kind="validation") from None

    def branch(self, name):
        try:
            branch = self.request("/repository/branches/" + urllib.parse.quote(name, safe=""))
        except GitLabError as error:
            if error.status == 404:
                return None
            raise
        if branch.get("name") != name:
            raise ValueError("GitLab fixture branch identity differs")
        sha = branch.get("commit", {}).get("id")
        revision(sha)
        return sha

    def files(self, sha, names=FILES):
        return {name: self.request("/repository/files/" + urllib.parse.quote(name, safe="") + "/raw?ref=" + sha, raw=True)
                for name in names}

    def tree(self, sha, extra_files=()):
        owned = set(FILES) | set(extra_files)
        entries = self.request("/repository/tree?per_page=100&ref=" + sha)
        if not isinstance(entries, list) or len(entries) >= 100:
            raise ValueError("fixture root tree exceeds its complete readback limit")
        result = {}
        for entry in entries:
            path = entry["path"]
            if not isinstance(path, str) or not path or "/" in path or path in result:
                raise ValueError("invalid fixture root tree")
            revision(entry["id"])
            if path in owned and (entry["type"] != "blob" or entry["mode"] not in ("100644", "100755")):
                raise ValueError("fixture scripts must be regular files")
            # Unchanged subtree IDs also prove nested files did not change.
            result[path] = (entry["type"], entry["mode"], None if path in owned else entry["id"])
        if not owned.issubset(result):
            raise ValueError("fixture tree lacks its fixed files")
        return result


def prepare(config, candidate_sha, token, *, run_id=None, attempt=None):
    """Return a private runtime pin; never change the configured base or secrets."""
    revision(candidate_sha)
    revision(config["fixture_sha"])
    scoped = run_id is not None or attempt is not None
    if scoped and any(not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]{0,18}", value) or int(value) > 2**63 - 1
                      for value in (run_id, attempt)):
        raise ValueError("fixture scope requires an exact owning run and attempt")
    client = GitLab(config, token)
    project = client.request("")
    if str(project.get("id")) != config["project_id"] or project.get("visibility") != "private":
        raise ValueError("fixture provisioning requires the exact private project")
    if client.branch(config["fixture_ref"]) != config["fixture_sha"]:
        raise ValueError("configured GitLab fixture base moved")
    expected = {name: (Path("tests/fixtures/gitlab") / name).read_bytes() for name in FILES}
    base_tree = client.tree(config["fixture_sha"])
    if scoped and SCOPE_MARKER in base_tree:
        raise ValueError("configured fixture base contains the reserved run marker")
    base = client.files(config["fixture_sha"])
    base_matches = base == expected
    if base_matches and not scoped:
        return dict(config)
    branch = "acceptance/" + candidate_sha
    if scoped:
        branch += "/" + run_id + "/" + attempt
    message = "test: pin acceptance fixture for " + candidate_sha
    expected_tree = dict(base_tree)
    extra_files = ()
    if scoped:
        message += "/" + run_id + "/" + attempt
        expected[SCOPE_MARKER] = (json.dumps({"schema_version": 1, "candidate_sha": candidate_sha, "run_id": run_id,
                                           "run_attempt": attempt}, sort_keys=True, separators=(",", ":")) + "\n").encode()
        expected_tree[SCOPE_MARKER] = ("blob", "100644", None)
        extra_files = (SCOPE_MARKER,)
    sha = client.branch(branch)
    write_failure = None
    try:
        if sha is None:
            # A run marker makes even an identical fixture a nonempty atomic
            # commit, using the existing commit permission instead of adding
            # branch-create permission. A concurrent loser only reads back.
            body = {"branch": branch, "start_sha": config["fixture_sha"], "commit_message": message,
                    "actions": [{"action": "update", "file_path": name, "encoding": "base64",
                                 "content": base64.b64encode(expected[name]).decode()}
                                for name in FILES if base[name] != expected[name]]}
            if scoped:
                body["actions"].append({"action": "create", "file_path": SCOPE_MARKER, "encoding": "base64",
                                        "content": base64.b64encode(expected[SCOPE_MARKER]).decode()})
            try:
                client.request("/repository/commits", body)
            except (GitLabError, ValueError) as error:
                # Reconcile an uncertain write exclusively through exact reads.
                write_failure = error if isinstance(error, GitLabError) else GitLabError(
                    operation="commit", method="POST", kind="validation")
            sha = client.branch(branch)
            if sha is None:
                if write_failure is not None:
                    raise write_failure
                raise ValueError("created GitLab fixture branch is missing")
        commit = client.request("/repository/commits/" + sha)
        ancestry_valid = commit.get("parent_ids") == [config["fixture_sha"]] and commit.get("message", "").rstrip("\n") == message
        if (commit.get("id") != sha or not ancestry_valid or client.files(sha, expected) != expected or client.tree(sha, extra_files) != expected_tree):
            raise ValueError("GitLab fixture commit differs from the reviewed files and base")
        if client.branch(branch) != sha:
            raise ValueError("GitLab fixture branch changed during verification")
    except Exception as error:
        if write_failure is not None and error is not write_failure:
            failure = error if isinstance(error, GitLabError) else GitLabError(kind="validation")
            failure.prior_write = write_failure
            raise failure from None
        raise
    return {**config, "fixture_ref": branch, "fixture_sha": sha}
