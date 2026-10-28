"""Install the pinned official Linux amd64 CLI without using cloud credentials."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import urllib.request

root = Path(__file__).resolve().parents[1]
lock = json.loads((root / "upstream.lock.json").read_text())
version = lock["aliyun_cli"]
expected = lock["aliyun_cli_sha256"]["linux-amd64"]
url = f"https://github.com/aliyun/aliyun-cli/releases/download/v{version}/aliyun-cli-linux-{version}-amd64.tgz"
with urllib.request.urlopen(url, timeout=60) as response:
    archive = response.read()
if hashlib.sha256(archive).hexdigest() != expected:
    raise SystemExit("official Alibaba CLI archive checksum mismatch")
with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as package:
    members = [m for m in package.getmembers() if m.name == "aliyun"]
    if len(members) != 1 or not members[0].isfile():
        raise SystemExit("official Alibaba CLI archive has an unexpected layout")
    binary = package.extractfile(members[0]).read()
path = root / ".cache/tools/aliyun"
path.parent.mkdir(parents=True, exist_ok=True)
path.write_bytes(binary)
path.chmod(0o700)
