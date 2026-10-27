#!/usr/bin/env python3
"""Readiness queries with an end-to-end deadline and exact resource identity."""
import json
import subprocess
import sys
import time


def wait(kind, region, resource_id):
    action, flag, container, member, identity = {
        "vpc": ("DescribeVpcs", "--VpcId", "Vpcs", "Vpc", "VpcId"),
        "vswitch": ("DescribeVSwitches", "--VSwitchId", "VSwitches", "VSwitch", "VSwitchId"),
    }[kind]
    deadline = time.monotonic() + 60
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise SystemExit("Network readiness deadline exceeded; preserve fixture receipts")
        try:
            response = subprocess.run(["aliyun", "vpc", action, "--RegionId", region, flag, resource_id],
                                      capture_output=True, text=True, check=True, timeout=min(5, remaining))
        except subprocess.TimeoutExpired:
            continue
        except subprocess.CalledProcessError:
            raise SystemExit("Network readiness query failed; preserve fixture receipts") from None
        rows = json.loads(response.stdout).get(container, {}).get(member, [])
        if rows:
            if len(rows) != 1 or rows[0].get(identity) != resource_id:
                raise SystemExit("Network readiness identity mismatch")
            if rows[0].get("Status") == "Available":
                return
        time.sleep(min(2, max(0, deadline - time.monotonic())))


if __name__ == "__main__":
    wait(*sys.argv[1:])
