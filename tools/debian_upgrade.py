"""Validate a pinned released package and a stopped Manager upgrade/rollback."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import re
import stat
import subprocess
import urllib.request

from debian_package import debian_version

ROOT = Path(__file__).resolve().parents[1]
CLI = Path("/usr/bin/fleeting-plugin-aliyun")
SERVICE = "gitlab-runner-aliyun.service"
STATE = tuple(Path(p) for p in ("/etc/gitlab-runner-aliyun", "/var/lib/gitlab-runner-aliyun", "/var/lib/fleeting-plugin-aliyun"))


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def baseline(arch):
    data = json.loads((ROOT / "tests/fixtures/debian-upgrade.json").read_text())
    debian_version(data["version"])
    if arch not in ("amd64", "arm64") or not re.fullmatch(r"[0-9a-f]{40}", data["commit"]):
        raise ValueError("invalid previous-release identity")
    selected = data["architectures"][arch]
    if any(not re.fullmatch(r"[0-9a-f]{64}", value) for value in (data["checksums_sha256"], selected["package_sha256"], selected["binary_sha256"])):
        raise ValueError("invalid previous-release checksums")
    return {"version": data["version"], "commit": data["commit"], "checksums_sha256": data["checksums_sha256"], **selected}


def fetch_baseline(directory, arch):
    pin = baseline(arch)
    directory.mkdir(parents=True, exist_ok=True)
    name = f"fleeting-plugin-aliyun_{debian_version(pin['version'])}_{arch}.deb"
    base = f"https://github.com/euyuil/fleeting-plugin-aliyun/releases/download/v{pin['version']}/"
    for filename, checksum in (("SHA256SUMS", pin["checksums_sha256"]), (name, pin["package_sha256"])):
        path = directory / filename
        if not path.exists():
            # Anonymous public release assets only; a candidate is never a fallback.
            with urllib.request.urlopen(base + filename.replace("~", "."), timeout=60) as response:
                content = response.read(32 * 1024 * 1024 + 1)
            if len(content) > 32 * 1024 * 1024 or hashlib.sha256(content).hexdigest() != checksum:
                raise ValueError("previous-release download differs from its reviewed pin")
            path.write_bytes(content)
        if not path.is_file() or path.is_symlink() or digest(path) != checksum:
            raise ValueError("previous-release cache differs from its reviewed pin")
    inventory = dict(line.split(maxsplit=1)[::-1] for line in (directory / "SHA256SUMS").read_text().splitlines() if line.strip())
    if inventory.get(name) != pin["package_sha256"]:
        raise ValueError("previous-release checksum inventory lacks the package")
    return directory / name


def command(*args):
    result = subprocess.run([str(arg) for arg in args], capture_output=True, text=True, timeout=60)
    if result.returncode:
        raise ValueError("package transition command failed; private host state is preserved")
    return result.stdout.strip()


def require_newer(version, arch="amd64"):
    command("dpkg", "--compare-versions", debian_version(version), "gt", debian_version(baseline(arch)["version"]))


def stopped():
    for field, expected in (("ActiveState", "inactive"), ("MainPID", "0"), ("ControlGroup", "")):
        if command("systemctl", "show", SERVICE, "--property=" + field, "--value") != expected:
            raise ValueError("package transition requires an inactive Manager with no unit processes")


def fingerprint():
    account = pwd.getpwnam("gitlab-runner-aliyun")
    rows = [("account", account.pw_uid, account.pw_gid, account.pw_dir, account.pw_shell)]
    for directory in STATE:
        for path in (directory, *sorted(directory.rglob("*"))):
            info = path.lstat()
            if not (stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode)):
                raise ValueError("managed state contains an unsupported file type")
            rows.append((str(path), info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode),
                         digest(path) if stat.S_ISREG(info.st_mode) else "directory"))
    return hashlib.sha256(json.dumps(rows, separators=(",", ":")).encode()).hexdigest()


def verify_package(path, checksum, version, arch):
    if not path.is_file() or path.is_symlink() or digest(path) != checksum:
        raise ValueError("package bytes differ from the accepted artifact")
    for field, expected in (("Package", "fleeting-plugin-aliyun"), ("Version", debian_version(version)), ("Architecture", arch)):
        if command("dpkg-deb", "-f", path, field) != expected:
            raise ValueError("package metadata differs from the accepted artifact")


def transition(candidate, candidate_sha, candidate_binary_sha, version, previous, arch):
    pin = baseline(arch)
    require_newer(version, arch)
    verify_package(previous, pin["package_sha256"], pin["version"], arch)
    verify_package(candidate, candidate_sha, version, arch)
    if digest(CLI) != pin["binary_sha256"] or command("dpkg-query", "-W", "-f=${Version}", "fleeting-plugin-aliyun") != debian_version(pin["version"]):
        raise ValueError("Manager was not installed from the pinned previous release")
    stopped()
    command(CLI, "manager", "doctor")
    before = fingerprint()
    for package, expected_binary, expected_version in (
            (candidate, candidate_binary_sha, version),
            (previous, pin["binary_sha256"], pin["version"]),
            (candidate, candidate_binary_sha, version)):
        command("dpkg", "-i", package)
        stopped()
        command(CLI, "manager", "doctor")
        if (digest(CLI) != expected_binary or fingerprint() != before or
                command("dpkg-query", "-W", "-f=${Version}", "fleeting-plugin-aliyun") != debian_version(expected_version)):
            raise ValueError("package transition changed Manager state or installed unexpected bytes")
    return {"version": pin["version"], "package_sha256": pin["package_sha256"],
            "candidate_package_sha256": candidate_sha, "state_preserved": True, "rollback_verified": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--disposable-host", action="store_true", required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--candidate-sha", required=True)
    parser.add_argument("--candidate-binary-sha", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--previous", type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or platform.system() != "Linux" or not Path("/run/systemd/system").is_dir():
        raise ValueError("package transition requires a disposable root Linux systemd host")
    arch = {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]
    if any(not re.fullmatch(r"[0-9a-f]{64}", value) for value in (args.candidate_sha, args.candidate_binary_sha)):
        raise ValueError("invalid candidate checksums")
    print(json.dumps(transition(args.candidate.resolve(), args.candidate_sha, args.candidate_binary_sha, args.version, args.previous.resolve(), arch)))


if __name__ == "__main__":
    main()
