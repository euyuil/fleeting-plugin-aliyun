"""Fail early on unsupported local tools; never silently use a different Go version."""
import os
from pathlib import Path
import subprocess
import sys

from check_metadata import go_version

if sys.version_info < (3, 11):
    raise SystemExit("Python 3.11 or newer is required")
expected = "go" + go_version(Path(__file__).resolve().parents[1])
actual = subprocess.check_output([os.environ.get("GO", "go"), "env", "GOVERSION"], text=True).strip()
if actual != expected:
    raise SystemExit("use the exact Go version from go.mod: " + expected)
print("Toolchain: " + actual + ", Python " + sys.version.split()[0])
