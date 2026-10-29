"""Install checksum-pinned development executables without extracting archive paths."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import tarfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def executable(data, expected, name):
    if hashlib.sha256(data).hexdigest() != expected:
        raise ValueError("development tool archive checksum mismatch")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = [m for m in archive.getmembers() if Path(m.name).name == name]
        if len(members) != 1 or not members[0].isfile():
            raise ValueError("archive must contain exactly one regular tool executable")
        return archive.extractfile(members[0]).read()


def install(name):
    system = {"Linux": "linux", "Darwin": "darwin"}[platform.system()]
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
    spec = json.loads((ROOT / "upstream.lock.json").read_text())["tools"][name]
    artifact = spec["archives"][system + "-" + arch]
    cache = ROOT / ".cache/tools"
    cache.mkdir(parents=True, exist_ok=True)
    archive = cache / (name + "-" + spec["version"] + "-" + system + "-" + arch + ".tar.gz")
    if not archive.exists():
        with urllib.request.urlopen(artifact["url"], timeout=120) as response:
            data = response.read()
        binary = executable(data, artifact["sha256"], name)
        archive.write_bytes(data)
    else:
        binary = executable(archive.read_bytes(), artifact["sha256"], name)
    temporary = cache / (name + ".tmp")
    temporary.write_bytes(binary)
    temporary.chmod(0o755)
    os.replace(temporary, cache / name)
    print(name + " " + spec["version"] + ": verified")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tools", nargs="+", choices=("gitleaks", "shellcheck"))
    for tool in parser.parse_args().tools:
        install(tool)
