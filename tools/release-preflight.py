#!/usr/bin/env python3
"""Refuse existing version releases and source tags before publication."""
import os
import re
import subprocess

version = os.environ["VERSION"]
subprocess.run(["go", "run", "./tools/distribution", "-validate-version", version], check=True)
repository = "repos/" + os.environ["GITHUB_REPOSITORY"]
for label, endpoint in (
    ("release", "/releases/tags/v" + version),
    ("Git tag", "/git/ref/tags/v" + version),
):
    response = subprocess.run(["gh", "api", repository + endpoint, "--include"], capture_output=True, text=True)
    if response.returncode == 0:
        raise SystemExit(label + " already exists")
    status = response.stdout.splitlines()[0] if response.stdout else ""
    if not re.fullmatch(r"HTTP/\S+ 404(?: .*)?", status):
        raise SystemExit("cannot verify " + label + " absence")
