"""Read one acceptance case and publish only fixed, identity-free diagnostics."""
import argparse
from datetime import datetime, timedelta
import json
import os
import re
import sys
from urllib.parse import urlencode

from cloud_config import decode
from cloud_fixture import JOB_SCENARIOS
from gitlab_fixture import GitLab, GitLabError
from release_candidate import revision

STATUSES = {"created", "pending", "preparing", "running", "success", "failed", "canceled", "skipped", "manual", "scheduled"}
REASONS = {"", "script_failure", "runner_system_failure", "job_execution_timeout", "stuck_or_timeout_failure",
           "api_failure", "unknown_failure", "runner_unsupported", "scheduler_failure", "trace_size_exceeded",
           "runner_service_shutdown", "no_updates_running", "server_timeout_running", "missing_dependency_failure"}
FLAGS = ("checkpoint", "cancel_requested", "loss_injected", "ssh_transport_loss", "control_released", "crash_injected", "normal_release")
HEADER = re.compile(r"^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z )?[0-9a-f]{2}[OE] ")
SGR = re.compile(r"\x1b\[[0-9;]*m")
CSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
SSH_ERROR = re.compile(r"^ERROR: Job failed: run exit \(exit code: (-?[0-9]{1,3})\): wait: remote command exited without exit status or exit signal$")
# Unknown words, numbers, identifiers and addresses are never copied. These
# public error terms retain a useful outline when a pinned literal changes.
ERROR_WORDS = frozenset("ERROR Job failed system failure run exit code wait remote command exited without status or signal "
                        "Process with from Reason was ssh connection closed by keepalive timeout context canceled deadline exceeded "
                        "read write tcp udp pipe broken reset peer EOF unexpected stage step_script execute executing execution "
                        "error process killed terminated aborted stopped stopping transport session not started disconnected "
                        "permission denied socket network use of user initiated shutdown KILL TERM HUP INT QUIT".split())


def error_outline(line):
    words = []
    for token in re.findall(r"[A-Za-z0-9_]+", line):
        value = token if token in ERROR_WORDS else "?"
        if value != "?" or not words or words[-1] != "?":
            words.append(value)
        if len(words) == 64:
            break
    match = re.match(r"ERROR: Job failed: run exit \(exit code: (-?[0-9]{1,3})\): ", line)
    code = int(match[1]) if match and -1 <= int(match[1]) <= 255 else None
    return {"words": words, "connector_exit_code": code}


class ReadOnlyGitLab(GitLab):
    def request(self, path, *, raw=False):
        operations = (("project", r""), ("candidate_branch", r"/repository/branches/acceptance%2F[0-9a-f]{40}"),
                      ("pipelines", r"/pipelines"), ("variables", r"/pipelines/[1-9][0-9]*/variables"),
                      ("pipeline", r"/pipelines/[1-9][0-9]*"), ("jobs", r"/pipelines/[1-9][0-9]*/jobs"),
                      ("trace", r"/jobs/[1-9][0-9]*/trace"))
        operation = next((name for name, pattern in operations if re.fullmatch(pattern, path.split("?", 1)[0])), None)
        if operation is None:
            raise ValueError("unsupported diagnostic read")
        # Report a fixed operation label even when a scoped token denies a read.
        print(json.dumps({"gitlab_read": operation}), flush=True)
        return super().request(path, raw=raw)


def allowed(value, choices):
    return value if isinstance(value, str) and value in choices else "unknown"


def outcome(job):
    """Project fields; never serialize the original job or arbitrary error text."""
    if not isinstance(job, dict):
        return {"status": "unknown", "failure_reason": "unknown"}
    return {"status": allowed(job.get("status"), STATUSES), "failure_reason": allowed(job.get("failure_reason", ""), REASONS),
            **{key: job[key] if type(job.get(key)) is bool else None for key in FLAGS}}


def trace_diagnostic(raw):
    """Classify fixed Runner messages without copying trace text or identifiers."""
    result = {"ssh_exit_codes": [], "ssh_message_with_sgr_only": False, "ssh_message_with_csi_removed": False,
              "script_exit_codes": [], "terminal_error_seen": False, "terminal_errors": []}
    for line in raw.decode("utf-8", errors="replace").split("\n"):
        line = HEADER.sub("", line.removesuffix("\r"))
        sgr, csi = SGR.sub("", line), CSI.sub("", line)
        result["terminal_error_seen"] |= csi.startswith("ERROR: Job failed")
        if csi.startswith("ERROR: Job failed") and len(result["terminal_errors"]) < 4:
            result["terminal_errors"].append(error_outline(csi))
        for value, key in ((sgr, "ssh_message_with_sgr_only"), (csi, "ssh_message_with_csi_removed")):
            match = SSH_ERROR.fullmatch(value)
            if match and -1 <= int(match[1]) <= 255:
                result[key] = True
                result["ssh_exit_codes"].append(int(match[1]))
        match = re.fullmatch(r"ERROR: Job failed: exit status ([0-9]{1,3})", csi)
        if match and int(match[1]) <= 255:
            result["script_exit_codes"].append(int(match[1]))
    for key in ("ssh_exit_codes", "script_exit_codes"):
        result[key] = sorted(set(result[key]))
    return result


def timestamp(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?Z", value):
        raise ValueError("invalid UTC time window")
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def identity(value):
    if type(value) is not int or value <= 0:
        raise ValueError("invalid response identity")
    return value


def inspect(client, config, sha, executor, scenario, started_at, completed_at):
    revision(sha)
    start, end = timestamp(started_at), timestamp(completed_at)
    if not start < end <= start + timedelta(hours=2) or executor not in ("instance", "docker-autoscaler") or scenario not in JOB_SCENARIOS:
        raise ValueError("invalid acceptance case selector")
    job_scenario = "smoke" if scenario == "upgrade" else scenario
    if not isinstance(config.get("runner_id"), str) or not re.fullmatch(r"[1-9][0-9]*", config["runner_id"]):
        raise ValueError("invalid dedicated runner")
    project = client.request("")
    if str(project.get("id")) != config["project_id"] or project.get("visibility") != "private":
        raise ValueError("expected dedicated private project")
    ref = "acceptance/" + sha
    fixture_sha = client.branch(ref)
    if fixture_sha is None:
        raise ValueError("candidate fixture branch missing")
    pipelines = client.request("/pipelines?" + urlencode({"ref": ref, "per_page": 100, "order_by": "id", "sort": "desc"}))
    if not isinstance(pipelines, list) or len(pipelines) >= 100:
        raise ValueError("pipeline readback exceeds its complete bound")
    matches = []
    for pipeline in pipelines:
        if pipeline.get("ref") != ref or pipeline.get("sha") != fixture_sha:
            raise ValueError("pipeline fixture identity differs")
        if not start <= timestamp(pipeline.get("created_at")) <= end:
            continue
        pipeline_id = identity(pipeline.get("id"))
        variables = client.request(f"/pipelines/{pipeline_id}/variables?per_page=100")
        if not isinstance(variables, list) or len(variables) >= 100:
            raise ValueError("invalid pipeline variables")
        values = {item["key"]: item["value"] for item in variables}
        if len(values) != len(variables):
            raise ValueError("duplicate pipeline variables")
        if values.get("FLEETING_EXECUTION_MODE") != executor or values.get("FLEETING_JOB_SCENARIO") != job_scenario:
            continue
        if values.get("FLEETING_EXPECTED_RUNNER_ID") != config["runner_id"] or not re.fullmatch(r"[0-9a-f]{48}", values.get("FLEETING_NONCE", "")):
            raise ValueError("pipeline acceptance identity differs")
        matches.append(pipeline_id)
    if len(matches) != 1:
        raise ValueError("selector must identify exactly one pipeline")
    pipeline_id = matches[0]
    pipeline = client.request(f"/pipelines/{pipeline_id}")
    if pipeline.get("id") != pipeline_id or pipeline.get("ref") != ref or pipeline.get("sha") != fixture_sha:
        raise ValueError("pipeline changed during inspection")
    jobs = client.request(f"/pipelines/{pipeline_id}/jobs?per_page=100&include_retried=true")
    if not isinstance(jobs, list) or len(jobs) != 1:
        raise ValueError("expected one unretried acceptance job")
    job = jobs[0]
    job_id = identity(job.get("id"))
    if (job.get("pipeline", {}).get("id") != pipeline_id or job.get("commit", {}).get("id") != fixture_sha or
            job.get("ref") != ref or (job.get("runner") or {}).get("id") != int(config["runner_id"])):
        raise ValueError("job identity differs from acceptance fixture")
    trace = client.request(f"/jobs/{job_id}/trace", raw=True)
    return {"pipeline_status": allowed(pipeline.get("status"), STATUSES), "job": outcome(job), "trace": trace_diagnostic(trace)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("sha", "executor", "scenario", "started-at", "completed-at"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    try:
        config = decode("RELEASE_GITLAB_CONFIG", os.environ)
        client = ReadOnlyGitLab(config, os.environ.get("RELEASE_GITLAB_API_TOKEN", ""))
        result = inspect(client, config, args.sha, args.executor, args.scenario, args.started_at, args.completed_at)
    except GitLabError as error:
        print(json.dumps({"diagnostic": "unavailable", "http_status": error.status if 100 <= error.status <= 599 else 0}))
        return 1
    except Exception:
        # API fields and validation exceptions can contain private configuration.
        print(json.dumps({"diagnostic": "unavailable", "reason": "validation_or_read_failure"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
