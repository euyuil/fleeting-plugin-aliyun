"""Check executable pins, recorded evidence identities and local documentation links."""
import argparse
import json
from pathlib import Path
import re
import shlex
from debian_package import debian_version

ACTIONS = {
    "actions/checkout": "checkout", "actions/setup-go": "setup_go",
    "actions/upload-artifact": "upload_artifact", "actions/download-artifact": "download_artifact",
    "aliyun/configure-aliyun-credentials-action": "aliyun_credentials",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def go_version(root):
    match = re.search(r"^go ([0-9]+\.[0-9]+\.[0-9]+)$", (root / "go.mod").read_text(), re.M)
    require(match is not None, "go.mod must pin an exact Go toolchain")
    return match[1]


def check_pins(root, lock):
    go_version(root)
    require("go" not in lock, "Go has one authoritative version in go.mod")
    modules = dict(re.findall(r"^\s*(\S+) (v\S+)(?:\s+// indirect)?$", (root / "go.mod").read_text(), re.M))
    selected = {
        "gitlab.com/gitlab-org/fleeting/fleeting": lock["fleeting"],
        "gitlab.com/gitlab-org/fleeting/fleeting-artifact": lock["fleeting_artifact"],
    }
    for key in ("openapi_sdk", "credentials"):
        name, version = lock[key].rsplit("@", 1)
        selected[name] = version
    for name, version in selected.items():
        require(modules.get(name) == version, "module and upstream lock differ: " + name)
    workflows = sorted((root / ".github/workflows").glob("*.y*ml"))
    require(bool(workflows), "workflow inventory is empty")
    seen = set()
    for path in workflows:
        text = path.read_text()
        setup_count = 0
        for match in re.finditer(r"^\s*(?:-\s+)?uses:\s*(.+)$", text, re.M):
            values = shlex.split(match[1], comments=True)
            require(len(values) == 1, "uses must be a literal reference: " + path.name)
            value = values[0]
            if value.startswith("./"):
                require((root / value).is_file(), "missing reusable workflow: " + value)
                continue
            name, separator, revision = value.partition("@")
            require(separator and re.fullmatch(r"[0-9a-f]{40}", revision), "Action must use a full SHA: " + name)
            require(name in ACTIONS, "Action needs an upstream lock mapping: " + name)
            require(lock["actions"][ACTIONS[name]] == revision, "Action differs from upstream lock: " + name)
            seen.add(ACTIONS[name])
            setup_count += name == "actions/setup-go"
        require(not re.search(r"^\s*go-version:", text, re.M), "workflow duplicates Go version: " + path.name)
        files = re.findall(r"^\s*go-version-file:\s*['\"]?go\.mod['\"]?\s*$", text, re.M)
        require(len(files) == setup_count, "every setup-go must read go.mod: " + path.name)
    require(seen == set(lock["actions"]), "unused or missing locked Action")
    requirements = (root / "tools/requirements-dev.txt").read_text()
    require(re.search(r"^ruff==" + re.escape(lock["tools"]["ruff"]) + r"\s", requirements, re.M), "Ruff and wheel lock differ")
    for name, repository in (("gitleaks", "gitleaks/gitleaks"), ("shellcheck", "koalaman/shellcheck")):
        spec = lock["tools"][name]
        require(set(spec["archives"]) == {s + "-" + a for s in ("linux", "darwin") for a in ("amd64", "arm64")}, "tool platform inventory differs: " + name)
        for artifact in spec["archives"].values():
            require(artifact["url"].startswith("https://github.com/" + repository + "/releases/download/v" + spec["version"] + "/"), "tool URL/version differs: " + name)
            require(re.fullmatch(r"[0-9a-f]{64}", artifact["sha256"]), "tool checksum missing: " + name)


def check_status(status):
    require(type(status.get("schema_version")) is int and status["schema_version"] == 1, "unsupported acceptance status schema")
    for key in ("implementation_status", "offline_validation_status", "live_validation_status", "publication_status", "runner_version", "fleeting_version"):
        require(isinstance(status.get(key), str) and status[key], "missing acceptance status: " + key)
    capabilities = status.get("capabilities")
    require(isinstance(capabilities, list) and capabilities, "capability inventory is empty")
    names = set()
    for row in capabilities:
        name = row.get("capability")
        require(isinstance(name, str) and name and name not in names, "missing or duplicate capability")
        names.add(name)
        require(row.get("implementation") in {"implemented", "deferred", "experimental"}, "unknown implementation state: " + name)
        for key in ("offline_validation", "live_validation"):
            require(row.get(key) in {"pass", "partial_pass", "not_run", "fail"}, "unknown validation state: " + name)
        if row.get("live_validation") == "partial_pass":
            require(row.get("verified_scope") and row.get("gap"), "partial evidence needs scope and gap: " + name)
    release = status.get("published_release")
    if release is None:
        return
    require(re.fullmatch(r"[0-9a-f]{40}", release["source_revision"]), "release source must be a full SHA")
    receipts = release.get("installation_receipts", [])
    architectures = []
    for receipt in receipts:
        architectures.append(receipt["architecture"])
        for field, target in (("version", "version"), ("revision", "source_revision"), ("reference", "reference"), ("manifest_digest", "manifest_digest")):
            require(receipt[field] == release[target], "release receipt identity differs: " + field)
        require(re.fullmatch(r"[0-9a-f]{64}", receipt["binary_sha256"]), "release receipt binary checksum missing")
        if release.get("linux_" + receipt["architecture"] + "_runner_installation") == "pass":
            require(all(receipt.get(k) is True for k in ("anonymous_manifest", "empty_configuration_rejected", "plugin_protocol", "release_bytes_match")), "release PASS needs successful installation evidence")
    require(len(architectures) == len(set(architectures)) and set(architectures) <= {"amd64", "arm64"}, "duplicate or unknown installation architecture")
    claimed = {arch for arch in ("amd64", "arm64") if release.get("linux_" + arch + "_runner_installation") == "pass"}
    require(claimed <= set(architectures), "release PASS needs its architecture receipt")

    architectures = []
    for receipt in release.get("debian_installation_receipts", []):
        arch = receipt["architecture"]
        architectures.append(arch)
        package = f"fleeting-plugin-aliyun_{debian_version(release['version'])}_{arch}.deb"
        require(receipt["version"] == release["version"], "Debian receipt version differs")
        require(receipt["package"] == package, "Debian receipt package identity differs")
        require(receipt["release_asset"] == package.replace("~", "."), "Debian receipt asset identity differs")
        require(re.fullmatch(r"[0-9a-f]{64}", receipt["package_sha256"]), "Debian receipt package checksum missing")
        checksums = [asset["sha256"] for asset in release.get("release_assets", [])
                     if asset["name"] == receipt["release_asset"]]
        require(checksums == [receipt["package_sha256"]], "Debian receipt and release asset checksum differ")
        if release.get("linux_" + arch + "_debian_lifecycle") == "pass":
            require(all(receipt.get(k) is True for k in ("anonymous_download", "native_binary_match", "lifecycle")),
                    "Debian PASS needs successful installation evidence")
    require(len(architectures) == len(set(architectures)) and set(architectures) <= {"amd64", "arm64"},
            "duplicate or unknown Debian installation architecture")
    claimed = {arch for arch in ("amd64", "arm64") if release.get("linux_" + arch + "_debian_lifecycle") == "pass"}
    require(claimed <= set(architectures), "Debian PASS needs its architecture receipt")


def check_links(root):
    for path in list(root.glob("*.md")) + list((root / "docs").rglob("*.md")):
        for target in re.findall(r"\]\(([^)]+)\)", path.read_text()):
            if re.match(r"(?:[a-z]+:|#)", target):
                continue
            target = target.split("#", 1)[0]
            require(not target or (path.parent / target).exists(), "broken local documentation link: " + str(path.relative_to(root)) + " -> " + target)


def check(root):
    lock = json.loads((root / "upstream.lock.json").read_text())
    require(lock.get("schema_version") == 1, "unsupported upstream lock schema")
    check_pins(root, lock)
    check_status(json.loads((root / "acceptance/status.json").read_text()))
    check_links(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    check(parser.parse_args().root)
    print("PASS: tool/module pins, acceptance identities and local documentation links")
