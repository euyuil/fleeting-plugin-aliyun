#!/usr/bin/env python3
"""Probe public US East API routes and a pinned, explicitly supplied SSH host."""

import argparse
import base64
import hashlib
import ipaddress
import json
import os
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path


def egress():
    with urllib.request.urlopen("https://api.ipify.org", timeout=10) as response:
        address = ipaddress.IPv4Address(response.read(64).decode().strip())
    if not address.is_global:
        raise ValueError("Runner egress must be a public IPv4 address")
    return {"ipv4": str(address)}


def probe():
    host = ipaddress.IPv4Address(os.environ.get("NETWORK_PROBE_HOST", ""))
    expected = os.environ.get("NETWORK_PROBE_HOST_KEY_SHA256", "")
    if not host.is_global or not expected.startswith("SHA256:"):
        raise ValueError("A public IPv4 target and pinned SSH fingerprint are required")
    results = []
    for endpoint in ("ecs.us-east-1.aliyuncs.com", "ess.aliyuncs.com", "sts.aliyuncs.com"):
        started = time.monotonic()
        try:
            socket.getaddrinfo(endpoint, 443, type=socket.SOCK_STREAM)
            try:
                response = urllib.request.urlopen("https://" + endpoint + "/", timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status = response.status
            # An unsigned request normally returns 4xx. It still proves verified HTTPS.
            results.append({"endpoint": endpoint, "status": "PASS", "http_status": status,
                            "seconds": round(time.monotonic() - started, 3)})
        except (OSError, urllib.error.URLError) as error:
            results.append({"endpoint": endpoint, "status": "FAIL",
                            "error_type": type(error).__name__})
    # The operator adds this run's egress /32 after downloading the early artifact.
    deadline = time.monotonic() + 240
    ssh_status = "FAIL"
    while time.monotonic() < deadline:
        try:
            scanned = subprocess.run(["ssh-keyscan", "-4", "-T", "8", "-t", "ed25519", str(host)],
                                     capture_output=True, text=True, timeout=12, check=False)
        except subprocess.TimeoutExpired:
            time.sleep(5)
            continue
        fingerprints = []
        for line in scanned.stdout.splitlines():
            fields = line.split()
            if len(fields) == 3 and fields[1] == "ssh-ed25519":
                digest = hashlib.sha256(base64.b64decode(fields[2], validate=True)).digest()
                fingerprints.append("SHA256:" + base64.b64encode(digest).decode().rstrip("="))
        if fingerprints:
            ssh_status = "PASS" if set(fingerprints) == {expected} else "HOST_KEY_MISMATCH"
            break
        time.sleep(5)
    results.append({"endpoint": "ephemeral-ecs-ssh", "status": ssh_status,
                    "verification": "public host-key scan match only; no completed authentication"})
    return {"status": "PASS" if all(r["status"] == "PASS" for r in results) else "FAIL",
            "checks": results}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("egress", "probe"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = egress() if args.mode == "egress" else probe()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        result = {"status": "FAIL", "error_type": type(error).__name__}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    if args.mode == "probe" or result.get("status") == "FAIL":
        print(json.dumps(result))
        raise SystemExit(0 if result["status"] == "PASS" else 1)
