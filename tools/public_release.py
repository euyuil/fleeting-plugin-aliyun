"""Bind anonymous release downloads and native installation receipts to a sealed candidate."""
import argparse
import json
import os
from pathlib import Path
import re
from types import SimpleNamespace
import urllib.request

from release_candidate import digest, verify, write_json

PLATFORMS = (("ubuntu-26.04", "amd64"), ("ubuntu-26.04-arm", "arm64"),
             ("ubuntu-24.04", "amd64"), ("ubuntu-24.04-arm", "arm64"))
PAYLOAD = ("plugin", "LICENSE", "NOTICE", "licenses.tar.gz")


def assets(manifest):
    files = manifest["files"]
    names = {name.removeprefix("dist/"): value for name, value in files.items()
             if name.startswith("dist/") and "/" not in name.removeprefix("dist/")}
    if len(names) != 6 or not {"SHA256SUMS", "dependencies.json"} <= names.keys():
        raise ValueError("expected the complete six-asset candidate distribution")
    return names


def checksum_inventory(raw, expected):
    seen = {}
    for line in raw.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+\.(?:tar\.gz|deb))", line)
        if match is None or match[2] in seen:
            raise ValueError("invalid or duplicate public checksum entry")
        seen[match[2]] = match[1]
    if seen != {name: value for name, value in expected.items() if name.endswith((".deb", ".tar.gz"))}:
        raise ValueError("public checksum inventory differs from the sealed candidate")


def download(manifest, arch, directory, repository, manifest_digest, read=urllib.request.urlopen):
    if arch not in ("amd64", "arm64") or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid public release platform or repository")
    expected = assets(manifest)
    base = f"https://github.com/{repository}/releases/download/v{manifest['version']}"
    with read(f"https://api.github.com/repos/{repository}/releases/tags/v{manifest['version']}", timeout=60) as response:
        release = json.load(response)
    published = [value.get("name") for value in release.get("assets", [])]
    if len(published) != len(expected) or set(published) != {name.replace("~", ".") for name in expected}:
        raise ValueError("public release assets are missing, duplicated or undeclared")
    directory.mkdir(parents=True, exist_ok=False)
    selected = {name: value for name, value in expected.items()
                if name in ("SHA256SUMS", "dependencies.json") or name.endswith((f"_linux_{arch}.tar.gz", f"_{arch}.deb"))}
    if len(selected) != 4:
        raise ValueError("public native asset selection is incomplete")
    for name, value in selected.items():
        # No request reads credentials from the environment or falls back to the
        # candidate. Verify bytes before any archive extraction or execution.
        with read(base + "/" + name.replace("~", "."), timeout=60) as response:
            (directory / name).write_bytes(response.read())
        if digest(directory / name) != value:
            raise ValueError("public asset differs from the sealed candidate")
    checksum_inventory((directory / "SHA256SUMS").read_text(), expected)
    return {"schema_version": 1, "result": "PASS", "scope": "public-distribution", "candidate_binding": "sealed",
            "candidate_sha": manifest["candidate_sha"], "version": manifest["version"], "manifest_sha256": manifest_digest,
            "architecture": arch, "anonymous_download": True, "files": selected,
            "run_id": os.environ["GITHUB_RUN_ID"], "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"]}


def require_fields(value, expected):
    if not isinstance(value, dict) or any(type(value.get(k)) is not type(v) or value[k] != v for k, v in expected.items()):
        raise ValueError("public verification receipt is missing, stale or incomplete")


def gate(args):
    from job_acceptance import EXECUTION_TOPOLOGY, public_smoke_gate, recovery_policy, selected_cases
    manifest = verify(args.root, args.digest, args.sha, args.version)
    run, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    identity = {"candidate_sha": args.sha, "version": args.version, "manifest_sha256": args.digest,
                "run_id": run, "run_attempt": attempt}
    candidate = json.loads(args.candidate_summary.read_text())
    require_fields(candidate, {**identity, "schema_version": 2, "result": "PASS", "phase": "candidate", "validation_profile": "full",
                               "execution_topology": EXECUTION_TOPOLOGY,
                               "scope": "full-suite", "receipt_schema_version": 3, "recovery_policies": {scenario: recovery_policy(scenario) for _, scenario in selected_cases()},
                               "full_suite": True, "release_ready": True, "selected_cases": ["/".join(c) for c in selected_cases()], "omitted_cases": []})
    public_smoke_gate(SimpleNamespace(**vars(args), phase="public", receipts=args.job_receipts))
    for path in args.job_receipts.glob("*.json"):
        receipt = json.loads(path.read_text())
        require_fields(receipt, {key: candidate[key] for key in ("fixture_config_sha256", "gitlab_config_sha256")})
    expected_paths = set()
    for runner, arch in PLATFORMS:
        names = [f"download-receipt-{runner}-{arch}.json", f"install-receipt-{runner}-{arch}.json",
                 f"install-receipt-debian-{runner}-{arch}.json"]
        expected_paths.update(names)
        downloaded, oci, debian = [json.loads((args.platform_receipts / name).read_text()) for name in names]
        require_fields(downloaded, {**identity, "schema_version": 1, "result": "PASS", "scope": "public-distribution",
                                    "candidate_binding": "sealed", "architecture": arch, "anonymous_download": True})
        expected_files = {name: value for name, value in assets(manifest).items()
                          if name in ("SHA256SUMS", "dependencies.json") or name.endswith((f"_linux_{arch}.tar.gz", f"_{arch}.deb"))}
        require_fields(downloaded, {"files": expected_files})
        payload = {name: manifest["files"][f"dist/packages/linux/{arch}/{name}"] for name in PAYLOAD}
        require_fields(oci, {**identity, "runner_image": runner, "version": args.version, "revision": args.sha, "os": "linux", "architecture": arch,
                             "reference": f"ghcr.io/{os.environ['GITHUB_REPOSITORY']}:{args.version}",
                             "binary_sha256": payload["plugin"], "anonymous_manifest": True, "release_bytes_match": True,
                             "plugin_protocol": True, "empty_configuration_rejected": True, "payload_sha256": payload})
        index = oci.get("manifest_digest")
        platform_digest = oci.get("platform_manifest_digest")
        if (not isinstance(index, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", index) or
                oci.get("pinned_reference") != f"ghcr.io/{os.environ['GITHUB_REPOSITORY']}@{index}" or
                not isinstance(platform_digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", platform_digest)):
            raise ValueError("public OCI installation lacks its immutable reference")
        package = next(name for name in expected_files if name.endswith(".deb"))
        require_fields(debian, {**identity, "version": args.version, "architecture": arch, "runner_image": runner, "package": package,
                                "package_sha256": expected_files[package], "anonymous_download": True, "native_binary_match": True,
                                "install_smoke": True, "lifecycle": False})
    if {path.name for path in args.platform_receipts.glob("*.json")} != expected_paths:
        raise ValueError("all exact native public verification receipts are required")
    return {"schema_version": 1, "result": "PASS", "scope": "release-completion", **identity,
            "candidate_full_suite": True, "public_platforms": 4, "public_real_jobs": 2, "candidate_binding": "sealed"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("download", "gate"))
    for name in ("root", "receipt", "directory", "platform-receipts", "job-receipts", "candidate-summary"):
        parser.add_argument("--" + name, type=Path)
    for name in ("digest", "sha", "version", "arch"):
        parser.add_argument("--" + name)
    args = parser.parse_args()
    if any(getattr(args, name) is None for name in ("root", "receipt", "digest", "sha", "version")):
        parser.error("a sealed candidate identity and receipt path are required")
    if args.operation == "download":
        if args.arch is None or args.directory is None:
            parser.error("download requires a native architecture and new directory")
        manifest = verify(args.root, args.digest, args.sha, args.version)
        result = download(manifest, args.arch, args.directory, os.environ["GITHUB_REPOSITORY"], args.digest)
    else:
        if any(getattr(args, name) is None for name in ("platform_receipts", "job_receipts", "candidate_summary")):
            parser.error("gate requires platform, job and candidate summary receipts")
        result = gate(args)
    write_json(args.receipt, result)


if __name__ == "__main__":
    main()
