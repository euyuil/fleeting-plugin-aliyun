"""Pin the reviewed job fixture through the dedicated project's existing PAT."""
import base64
import json
from pathlib import Path
import re
import urllib.error
import urllib.parse
import urllib.request

from release_candidate import revision

FILES = (".gitlab-ci.yml", "smoke.sh")


class GitLabError(ValueError):
    def __init__(self, status=0):
        self.status = status
        super().__init__(f"GitLab fixture request failed (HTTP {status})")


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
        try:
            with self.opener.open(request, timeout=30) as response:
                data = response.read(128 * 1024 + 1)
        except urllib.error.HTTPError as error:
            raise GitLabError(error.code) from None
        except (OSError, urllib.error.URLError):
            raise GitLabError() from None
        if len(data) > 128 * 1024:
            raise ValueError("GitLab fixture response exceeds the fixed limit")
        if raw:
            return data
        try:
            return json.loads(data)
        except (ValueError, UnicodeError):
            raise ValueError("invalid GitLab fixture response") from None

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

    def files(self, sha):
        return {name: self.request("/repository/files/" + urllib.parse.quote(name, safe="") + "/raw?ref=" + sha, raw=True)
                for name in FILES}

    def tree(self, sha):
        entries = self.request("/repository/tree?per_page=100&ref=" + sha)
        if not isinstance(entries, list) or len(entries) >= 100:
            raise ValueError("fixture root tree exceeds its complete readback limit")
        result = {}
        for entry in entries:
            path = entry["path"]
            if not isinstance(path, str) or not path or "/" in path or path in result:
                raise ValueError("invalid fixture root tree")
            revision(entry["id"])
            if path in FILES and (entry["type"] != "blob" or entry["mode"] not in ("100644", "100755")):
                raise ValueError("fixture scripts must be regular files")
            # Unchanged subtree IDs also prove nested files did not change.
            result[path] = (entry["type"], entry["mode"], None if path in FILES else entry["id"])
        if not set(FILES).issubset(result):
            raise ValueError("fixture tree lacks its fixed files")
        return result


def prepare(config, candidate_sha, token):
    """Return a private runtime pin; never change the configured base or secrets."""
    revision(candidate_sha)
    revision(config["fixture_sha"])
    client = GitLab(config, token)
    project = client.request("")
    if str(project.get("id")) != config["project_id"] or project.get("visibility") != "private":
        raise ValueError("fixture provisioning requires the exact private project")
    if client.branch(config["fixture_ref"]) != config["fixture_sha"]:
        raise ValueError("configured GitLab fixture base moved")
    expected = {name: (Path("tests/fixtures/gitlab") / name).read_bytes() for name in FILES}
    base_tree = client.tree(config["fixture_sha"])
    base = client.files(config["fixture_sha"])
    if base == expected:
        return dict(config)
    branch = "acceptance/" + candidate_sha
    message = "test: pin acceptance fixture for " + candidate_sha
    sha = client.branch(branch)
    if sha is None:
        # One atomic commit creates a new branch from the pinned base. No force,
        # delete, arbitrary paths, conflicting ref overwrite, or uncertain retry.
        body = {"branch": branch, "start_sha": config["fixture_sha"], "commit_message": message,
                "actions": [{"action": "update", "file_path": name, "encoding": "base64",
                             "content": base64.b64encode(expected[name]).decode()}
                            for name in FILES if base[name] != expected[name]]}
        try:
            client.request("/repository/commits", body)
        except GitLabError:
            # Recover a lost response or a concurrent identical create only by
            # reading and validating the final branch; never repeat the POST.
            if client.branch(branch) is None:
                raise
        sha = client.branch(branch)
        if sha is None:
            raise ValueError("created GitLab fixture branch is missing")
    commit = client.request("/repository/commits/" + sha)
    if (commit.get("id") != sha or commit.get("parent_ids") != [config["fixture_sha"]] or
            commit.get("message", "").rstrip("\n") != message or client.files(sha) != expected or client.tree(sha) != base_tree):
        raise ValueError("GitLab fixture commit differs from the reviewed files and base")
    if client.branch(branch) != sha:
        raise ValueError("GitLab fixture branch changed during verification")
    return {**config, "fixture_ref": branch, "fixture_sha": sha}
