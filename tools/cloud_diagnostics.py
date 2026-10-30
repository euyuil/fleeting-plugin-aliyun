"""Allowlisted public diagnostics; raw driver output remains private."""
import json
from pathlib import Path
import re
import subprocess
import time


PHASES = {
    "scenario": frozenset(("preflight", "claim", "ingress", "plugin_init", "increase", "wait_ready",
                           "disk_verification", "observe_before_ssh", "connect_info", "ssh", "restart",
                           "creation_restart", "response_loss_restart", "restart_verification",
                           "specified_scale_in", "normal_release", "removal_restart")),
    "cleanup": frozenset(("plugin_shutdown", "driver_cleanup", "evidence_write")),
}
API_ACTIONS = {
    "sts": frozenset(("GetCallerIdentity",)),
    "vpc": frozenset(("DescribeVpcs", "DescribeVSwitches", "CreateVpc", "CreateVSwitch", "DeleteVpc", "DeleteVSwitch")),
    "ecs": frozenset(("DescribeDisks", "DescribeInstances", "DescribeInstanceTypes", "DescribeKeyPairs",
                      "DescribeLaunchTemplates", "DescribeLaunchTemplateVersions", "DescribeNetworkInterfaces",
                      "DescribeSecurityGroupAttribute", "DescribeSecurityGroups", "CreateSecurityGroup",
                      "ImportKeyPair", "CreateLaunchTemplate", "DeleteInstance", "DeleteDisk", "DeleteNetworkInterface",
                      "DeleteKeyPairs", "DeleteLaunchTemplate", "DeleteSecurityGroup", "AuthorizeSecurityGroup",
                      "RevokeSecurityGroup", "ModifyInstanceAutoReleaseTime")),
    "ess": frozenset(("DescribeScalingGroups", "DescribeScalingInstances", "DescribeScalingActivities",
                      "DescribeScalingRules", "DescribeScheduledTasks", "DescribeLifecycleHooks", "DescribeInstanceRefreshes",
                      "CreateScalingGroup", "DeleteScalingGroup", "EnableScalingGroup", "ScaleWithAdjustment",
                      "RemoveInstances", "TagResources", "UntagResources")),
}
API_CODES = frozenset(("IdempotentParameterMismatch", "Forbidden", "Forbidden.RAM", "AccessDenied", "Unauthorized",
                       "NoPermission", "Throttling", "Throttling.User", "Throttling.Api", "InsufficientInstanceCapacity",
                       "ResourceNotAvailable", "SoldOut", "QuotaExceeded", "LimitExceeded", "TransportError",
                       "DeadlineExceeded", "Canceled", "InvalidAutoReleaseTime.Malformed"))
TRANSPORT_MARKERS = (
    ("authentication", ("ssh: unable to authenticate",)),
    ("host_key", ("ssh: host key mismatch", "knownhosts: key mismatch")),
    ("timeout", ("i/o timeout", "context deadline exceeded")),
    ("connection_refused", ("connection refused",)),
    ("connection_reset", ("connection reset by peer",)),
    ("handshake", ("ssh: handshake failed",)))


def transport_categories(log):
    return [name for name, markers in TRANSPORT_MARKERS if any(marker in log for marker in markers)]


def api_error(service, action, code):
    category = "other_cloud_api"
    if isinstance(code, str):
        for prefixes, label in ((("Forbidden", "AccessDenied", "Unauthorized", "NoPermission"), "authorization"),
                                (("IdempotentParameterMismatch",), "idempotency_conflict"),
                                (("Quota", "LimitExceeded"), "quota"),
                                (("InsufficientInstanceCapacity", "ResourceNotAvailable", "SoldOut"), "capacity"),
                                (("Throttling",), "throttling"), (("TransportError", "DeadlineExceeded", "Canceled"), "transport")):
            if code.startswith(prefixes):
                category = label
                break
    return {"service": service if isinstance(service, str) and service in API_ACTIONS else "unknown",
            "action": action if isinstance(service, str) and isinstance(action, str) and action in API_ACTIONS.get(service, ()) else "unknown",
            "code": code if isinstance(code, str) and code in API_CODES else "unknown", "category": category}


class CloudOperationError(RuntimeError):
    def __init__(self, service, action, *, output="", unavailable=False):
        # CLI output is untrusted and may include request IDs or credentials.
        match = re.search(r'(?:"(?:Code|error_code)"\s*:\s*"|ErrorCode:\s*)([A-Za-z0-9._-]+)', output)
        self.diagnostic = api_error(service, action, match[1] if match else "")
        self.diagnostic["kind"] = "unavailable" if unavailable else "cloud_api"
        super().__init__("cloud operation " + ("unavailable: " if unavailable else "failed: ") +
                         self.diagnostic["service"] + "/" + self.diagnostic["action"])


def diagnostic_error(error):
    if isinstance(error, CloudOperationError):
        return error.diagnostic
    # Do not publish str(error), exception class names from dependencies, or argv.
    for kind, classes in (("interrupted", (KeyboardInterrupt, SystemExit)),
                          ("timeout", (TimeoutError, subprocess.TimeoutExpired)),
                          ("validation", (ValueError,)), ("io", (OSError,)),
                          ("runtime", (RuntimeError,))):
        if isinstance(error, classes):
            return {"kind": kind}
    return {"kind": "unknown"}


def duration_ms(start):
    return max(0, min(3_600_000, int((time.monotonic() - start) * 1000)))


def phase_diagnostics(log, templates=()):
    """Aggregate a bounded set of phases, never arbitrary log strings or identities."""
    scopes = {scope: {"result": "UNKNOWN", "last_phase": None, "failure_phase": None, "phases": {}}
              for scope in PHASES}
    active, previous = None, 0
    for line in log.splitlines():
        if not line.startswith("FLEETING_DIAGNOSTIC "):
            if active:
                row = scopes[active[0]]["phases"][active[1]]
                for template in templates:
                    if matches_error_template(line, template) and template not in row["matched_error_templates"]:
                        row["matched_error_templates"].append(template)
                for category in transport_categories(line):
                    if category not in row["transport_error_categories"]:
                        row["transport_error_categories"].append(category)
                for code in re.findall(r"Alibaba Cloud API error: code=([^\s]+)", line):
                    error = api_error(None, None, code)
                    if error not in row["api_errors"] and len(row["api_errors"]) < 20:
                        row["api_errors"].append(error)
            continue
        try:
            event = json.loads(line.removeprefix("FLEETING_DIAGNOSTIC "))
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        scope, phase, elapsed = event.get("scope"), event.get("phase"), event.get("elapsed_ms")
        if (not isinstance(scope, str) or not isinstance(phase, str) or phase not in PHASES.get(scope, ()) or
                type(elapsed) is not int or not previous <= elapsed <= 3_600_000):
            continue
        kind = event.get("event")
        if kind not in ("phase", "result", "failure", "api_error"):
            continue
        if kind != "phase" and active != (scope, phase):
            continue
        if active:
            scopes[active[0]]["phases"][active[1]]["duration_ms"] += elapsed - previous
        previous = elapsed
        summary = scopes[scope]
        row = summary["phases"].setdefault(phase, {"visits": 0, "duration_ms": 0, "api_errors": [],
                                                 "matched_error_templates": [], "transport_error_categories": []})
        if kind == "phase":
            active = (scope, phase)
            summary["last_phase"] = phase
            row["visits"] += 1
        elif kind == "api_error":
            error = api_error(event.get("service"), event.get("action"), event.get("code"))
            if error not in row["api_errors"] and len(row["api_errors"]) < 20:
                row["api_errors"].append(error)
        elif kind == "failure":
            summary["failure_phase"] = summary["failure_phase"] or phase
        elif event.get("result") in ("PASS", "FAIL"):
            summary["result"] = event["result"]
            if event["result"] == "FAIL":
                summary["failure_phase"] = summary["failure_phase"] or phase
            active = None
    return scopes


def read_driver_diagnostic(directory, run, attempt, code):
    try:
        receipt = json.loads((directory / "evidence" / ("run-" + run + "-" + attempt) / "extended-result.json").read_text())
    except (OSError, ValueError):
        receipt = {}
    try:
        log = (directory / "driver.log").read_text(errors="replace")
    except OSError:
        log = ""
    return driver_diagnostic(receipt, code, log)


def matches_error_template(log, template):
    parts = re.split(r"%[-+#0-9. ]*[vTtbcdoOqxXUeEfFgGsqpw]", template)
    return sum(map(len, parts)) >= 12 and re.search(r"[\s\S]*?".join(map(re.escape, parts)), log) is not None


def matching_error_templates(log, source):
    templates = set()
    pattern = r'\b(?:errors\.New|fmt\.(?:Errorf|Sprintf)|t\.(?:Fatalf?|Errorf?))\(\s*("(?:[^"\\]|\\.)*")'
    for literal in re.findall(pattern, source):
        try:
            template = json.loads(literal)
        except ValueError:
            continue
        if matches_error_template(log, template):
            # Return only the published source literal, never interpolated data.
            templates.add(template)
    return sorted(templates)


def source_error_templates(log):
    root = Path(__file__).resolve().parent.parent
    sources = [root / "tests/live/extended_test.go"]
    for package in ("acceptance", "aliyun", "provider"):
        sources.extend(p for p in (root / "internal" / package).glob("*.go") if not p.name.endswith("_test.go"))
    templates = set()
    for path in sources:
        try:
            templates.update(matching_error_templates(log, path.read_text()))
        except OSError:
            continue
    return sorted(templates)


def driver_diagnostic(receipt, code, log):
    """Select public test facts without copying private log or receipt values."""
    if not isinstance(receipt, dict):
        receipt = {}
    resources = receipt.get("resources", {})
    resources = resources if isinstance(resources, dict) else {}
    instances = resources.get("instances")
    results = ("PASS", "FAIL_PRODUCT", "FAIL_INFRASTRUCTURE", "CLEANUP_FAILED")
    templates = source_error_templates(log)
    categories = {api_error(None, None, code)["category"]
                  for code in re.findall(r"Alibaba Cloud API error: code=([^\s]+)", log)}
    return {"exit_code": code if type(code) is int and -255 <= code <= 255 else None,
            "result": receipt.get("result") if receipt.get("result") in results else "UNKNOWN",
            "stages": phase_diagnostics(log, sorted(templates)),
            "test_lines": sorted({int(n) for n in re.findall(r"(?m)^\s+extended_test\.go:([1-9][0-9]{0,4}):", log)}),
            "api_error_categories": sorted(categories),
            "transport_error_categories": transport_categories(log),
            "matched_error_templates": sorted(templates),
            "workers_observed": len(instances) if isinstance(instances, list) else None,
            "worker_checks": {k: sum(isinstance(row, dict) and row.get(k) is True for row in instances)
                              if isinstance(instances, list) else None for k in
                              ("inventory_complete", "spot_verified", "deletion_verified")},
            "evidence_incomplete": resources.get("evidence_incomplete") if type(resources.get("evidence_incomplete")) is bool else None,
            "checks": {k: receipt.get(k) if type(receipt.get(k)) is bool else None for k in
                       ("ssh_completed", "normal_release", "specified_release", "restart_recovered")}}
