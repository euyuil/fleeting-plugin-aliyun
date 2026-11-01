#!/usr/bin/env python3
"""Validate release notes and refuse existing versions before publication."""
import json
import os
from pathlib import Path
import re
import subprocess

version = os.environ["VERSION"]
title = Path("docs/release-notes.md").read_text().partition("\n")[0].strip()
if title != "# " + version:
    raise SystemExit("release notes must start with '# " + version + "'")
subprocess.run(["go", "run", "./tools/distribution", "-validate-version", version], check=True)
repository = "repos/" + os.environ["GITHUB_REPOSITORY"]
for label, endpoint in (
    ("release", "/releases/tags/v" + version),
    ("Git tag", "/git/ref/tags/v" + version),
):
    response = subprocess.run(["gh", "api", repository + endpoint, "--include"], capture_output=True, text=True)
    if response.returncode == 0:
        expected = os.environ.get("CANDIDATE_SHA", "")
        if label == "Git tag" and re.fullmatch(r"[0-9a-f]{40}", expected):
            tag = json.loads(response.stdout.partition("\n\n")[2])
            if tag.get("object", {}).get("type") == "commit" and tag["object"].get("sha") == expected:
                continue
        raise SystemExit(label + " already exists")
    status = response.stdout.splitlines()[0] if response.stdout else ""
    if not re.fullmatch(r"HTTP/\S+ 404(?: .*)?", status):
        raise SystemExit("cannot verify " + label + " absence")
