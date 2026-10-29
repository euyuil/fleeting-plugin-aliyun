"""Check tracked Go files without modifying the worktree."""
import os
from pathlib import Path
import subprocess

paths = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go"]).decode().rstrip("\0").split("\0")
goroot = subprocess.check_output([os.environ.get("GO", "go"), "env", "GOROOT"], text=True).strip()
result = subprocess.run([str(Path(goroot) / "bin/gofmt"), "-l", *filter(None, paths)], check=True, capture_output=True, text=True)
if result.stdout:
    print(result.stdout, end="")
    raise SystemExit("Go formatting differs; run gofmt -w on the listed files")
