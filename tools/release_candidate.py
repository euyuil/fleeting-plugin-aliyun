"""Bind an unpublished distribution and cloud acceptance receipts to one source."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile

MODES = ("cloud-smoke", "cloud-scale", "cloud-restart", "cloud-create-restart",
         "cloud-remove-restart", "cloud-increase-response-loss")


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n")


def revision(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise ValueError("a full candidate commit SHA is required")
    return value


def files(root):
    paths = sorted(p for p in root.rglob("*") if p.is_file())
    if any(p.is_symlink() for p in root.rglob("*")):
        raise ValueError("candidate symlinks are forbidden")
    return {str(p.relative_to(root)): digest(p) for p in paths if p != root / "candidate.json"}


def distribution(root, version):
    for arch in ("amd64", "arm64"):
        archive = root / f"dist/fleeting-plugin-aliyun_{version}_linux_{arch}.tar.gz"
        with tarfile.open(archive, "r:gz") as package:
            members = [m for m in package.getmembers() if m.name == "fleeting-plugin-aliyun"]
            if len(members) != 1 or not members[0].isfile():
                raise ValueError("native archive must contain one regular plugin binary")
            binary = package.extractfile(members[0]).read()
        if hashlib.sha256(binary).hexdigest() != digest(root / f"dist/packages/linux/{arch}/plugin"):
            raise ValueError("native and OCI plugin binaries differ")


def build_info(root, sha):
    for relative, arch, instrumented in (("dist/packages/linux/amd64/plugin", "amd64", False),
                                         ("dist/packages/linux/arm64/plugin", "arm64", False),
                                         ("responsegate/plugin", "amd64", True)):
        info = json.loads(subprocess.check_output(["go", "version", "-m", "-json", str(root / relative)], text=True))
        settings = {s["Key"]: s["Value"] for s in info.get("Settings", [])}
        tags = settings.get("-tags", "").replace(",", " ").split()
        if (info.get("Path") != "github.com/euyuil/fleeting-plugin-aliyun/cmd/fleeting-plugin-aliyun"
                or settings.get("vcs.revision") != sha or settings.get("vcs.modified") != "false"
                or settings.get("GOOS") != "linux" or settings.get("GOARCH") != arch
                or ("responsegate" in tags) != instrumented or "componenttest" in tags):
            raise ValueError("candidate binary source, platform or build tags differ")


def create(root, sha, version):
    revision(sha)
    actual = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if actual != sha:
        raise ValueError("candidate checkout differs from the requested SHA")
    contents = files(root)
    required = {"dist/SHA256SUMS", "dist/dependencies.json", "responsegate/plugin"}
    for arch in ("amd64", "arm64"):
        required.update({f"dist/packages/linux/{arch}/plugin",
                         f"dist/fleeting-plugin-aliyun_{version}_linux_{arch}.tar.gz"})
    if not required <= contents.keys():
        raise ValueError("candidate distribution is incomplete")
    distribution(root, version)
    build_info(root, sha)
    manifest = {"schema_version": 1, "candidate_sha": sha, "version": version,
                "files": contents, "normal_binary": "dist/packages/linux/amd64/plugin",
                "fault_binary": "responsegate/plugin"}
    write_json(root / "candidate.json", manifest)
    return digest(root / "candidate.json")


def verify(root, expected_digest, sha, version):
    revision(sha)
    path = root / "candidate.json"
    if digest(path) != expected_digest:
        raise ValueError("candidate manifest checksum mismatch")
    manifest = json.loads(path.read_text())
    if (manifest.get("schema_version"), manifest.get("candidate_sha"), manifest.get("version")) != (1, sha, version):
        raise ValueError("candidate identity mismatch")
    if manifest.get("normal_binary") != "dist/packages/linux/amd64/plugin" or manifest.get("fault_binary") != "responsegate/plugin":
        raise ValueError("candidate binary paths differ")
    if manifest.get("files") != files(root):
        raise ValueError("candidate file inventory or checksum mismatch")
    distribution(root, version)
    build_info(root, sha)
    return manifest


def check_receipt(receipt, manifest, manifest_digest, mode, run, attempt):
    if any(not isinstance(v, str) or not re.fullmatch(r"[1-9][0-9]*", v) for v in (run, attempt)):
        raise ValueError("positive GitHub run and attempt identifiers are required")
    if mode not in MODES:
        raise ValueError("unknown cloud acceptance scenario")
    if not isinstance(receipt.get("fixture_config_sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", receipt["fixture_config_sha256"]):
        raise ValueError("cloud receipt requires a fixture configuration digest")
    expected_binary = manifest["fault_binary" if mode == MODES[-1] else "normal_binary"]
    expected = {"schema_version": 1, "result": "PASS", "cleanup_result": "PASS",
                "candidate_sha": manifest["candidate_sha"], "version": manifest["version"],
                "manifest_sha256": manifest_digest, "mode": mode, "run_id": run,
                "run_attempt": attempt, "plugin_sha256": manifest["files"][expected_binary],
                "response_gate_instrumented": mode == MODES[-1], "leftover_resources": 0}
    if any(type(receipt.get(k)) is not type(v) or receipt.get(k) != v for k, v in expected.items()):
        raise ValueError("cloud acceptance receipt is missing, failed or belongs to another candidate/run")


def gate(root, receipts, expected_digest, sha, version, run, attempt):
    if any(not isinstance(v, str) or not re.fullmatch(r"[1-9][0-9]*", v) for v in (run, attempt)):
        raise ValueError("positive GitHub run and attempt identifiers are required")
    manifest = verify(root, expected_digest, sha, version)
    paths = sorted(receipts.glob("*.json"))
    seen, configs = set(), set()
    for path in paths:
        receipt = json.loads(path.read_text())
        mode = receipt.get("mode")
        if mode not in MODES or mode in seen:
            raise ValueError("unexpected or duplicate cloud acceptance scenario")
        check_receipt(receipt, manifest, expected_digest, mode, run, attempt)
        seen.add(mode)
        configs.add(receipt["fixture_config_sha256"])
    if len(configs) != 1:
        raise ValueError("all scenarios must use the same fixture configuration")
    if seen != set(MODES):
        raise ValueError("all six cloud acceptance scenarios must pass before publication")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("create", "verify", "gate"))
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--digest")
    parser.add_argument("--receipts", type=Path)
    parser.add_argument("--run")
    parser.add_argument("--attempt")
    args = parser.parse_args()
    if args.operation == "create":
        print(create(args.root, args.sha, args.version))
    elif args.operation == "verify":
        verify(args.root, args.digest, args.sha, args.version)
    else:
        gate(args.root, args.receipts, args.digest, args.sha, args.version, args.run, args.attempt)


if __name__ == "__main__":
    main()
