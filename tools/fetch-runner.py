#!/usr/bin/env python3
"""Download the pinned Runner binary and verify a pinned SHA256."""
import hashlib
import json
import os
import pathlib
import platform
import urllib.request

root = pathlib.Path(__file__).resolve().parents[1]
lock = json.loads((root / "upstream.lock.json").read_text())["runner"]
system = {"Linux": "linux", "Darwin": "darwin"}[platform.system()]
arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
name = system + "-" + arch
expected = lock["sha256"][name]
target = root / ".cache" / ("gitlab-runner-" + name)
target.parent.mkdir(exist_ok=True)
if not target.exists():
    url = "https://gitlab-runner-downloads.s3.amazonaws.com/v" + lock["version"] + "/binaries/gitlab-runner-" + name
    temporary = target.with_suffix(".tmp")
    with urllib.request.urlopen(url, timeout=120) as response, temporary.open("wb") as out:
        while True:
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            out.write(chunk)
    if hashlib.sha256(temporary.read_bytes()).hexdigest() != expected:
        temporary.unlink()
        raise SystemExit("Runner checksum mismatch")
    temporary.rename(target)
if hashlib.sha256(target.read_bytes()).hexdigest() != expected:
    raise SystemExit("cached Runner checksum mismatch")
os.chmod(target, 0o755)
print(target)
