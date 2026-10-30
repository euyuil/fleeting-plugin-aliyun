"""Project private Manager journal messages into bounded public diagnostics."""
import json
import re

from job_diagnostics import CSI, error_outline

EVENTS = {
    "connection preparation failed": "connection_preparation_failed",
    "ready up preparation failed": "ready_up_preparation_failed",
    "ready up failed because instance is marked for removal": "already_marked_for_removal",
    "instance marked for removal": "instance_marked_for_removal",
    "instance unexpectedly removed": "instance_unexpectedly_removed",
    "health failure": "health_failure",
    "acquire heartbeat failure": "acquire_heartbeat_failure",
    "backing off scale up due to ready up failures": "readiness_backoff",
}
BOOTSTRAP_PHASES = {1: "ubuntu_packages", 2: "docker_install", 3: "docker_start", 4: "base_packages",
                    5: "ssh_setup", 6: "runner_download", 7: "runner_verification", 8: "bootstrap_probes"}


def bootstrap_diagnostic(name, code, message):
    # These codes belong only to the shipped ready-command protocol. Never
    # interpret a connector error or publish the untrusted worker status file.
    if name != "ready_up_preparation_failed" or "ready command:" not in message or code is None:
        return None
    if 201 <= code <= 208:
        return {"phase": BOOTSTRAP_PHASES[code - 200], "outcome": "failed"}
    if 221 <= code <= 228:
        return {"phase_observed": BOOTSTRAP_PHASES[code - 220], "outcome": "deadline"}
    if code in (209, 229):
        return {"phase" if code == 209 else "phase_observed": "unknown", "outcome": "failed" if code == 209 else "deadline"}
    if code in (231, 232, 233):
        return {"phase": {231: "git_probe", 232: "runner_probe", 233: "docker_probe"}[code], "outcome": "failed"}
    if code == 234:
        return {"phase_observed": "ready_marker_present", "outcome": "deadline"}
    return None


def bounded_json(text):
    # Interpreter JSON recursion limits vary. Bound structural nesting ourselves;
    # brackets in quoted log messages and escaped quotes do not affect depth.
    if not text.lstrip().startswith(("{", "[")):
        return json.loads(text)
    depth, quoted, escaped = 0, False, False
    for char in text:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "[{":
            depth += 1
            if depth > 32:
                raise RecursionError("Manager JSON nesting exceeds its fixed limit")
        elif char in "]}":
            depth -= 1
    return json.loads(text)


def event(message):
    """Recognize the pinned Runner's default text or JSON formatter."""
    try:
        fields = bounded_json(message)
    except ValueError:
        fields = None
    if isinstance(fields, dict):
        name = fields.get("msg")
        if fields.get("subsystem") == "taskscaler" and isinstance(name, str):
            return EVENTS.get(name)
        return None
    text = re.sub(r"^(?:ERROR|WARNING|FATAL|PANIC): ", "", message)
    if not re.search(r"(?:^|\s)subsystem=taskscaler(?:\s|$)", text):
        return None
    for name, value in EVENTS.items():
        if text.startswith(name) and (len(text) == len(name) or text[len(name)].isspace()):
            return value
    return None


def manager_diagnostic(result):
    state = result.get("manager_journal_state", "NOT_RECORDED")
    if state not in ("RECORDED", "NOT_STARTED", "UNAVAILABLE", "NOT_RECORDED"):
        return {"evidence": "INVALID"}
    capture = result.get("manager_journal_capture")
    quality = {}
    if "manager_journal_capture" in result:
        reasons = ("recorded", "not_started", "invalid_start", "ownership_unavailable", "read_failed", "no_valid_records")
        if (not isinstance(capture, dict) or capture.get("reason") not in reasons or
                type(capture.get("raw_truncated")) is not bool or
                any(type(capture.get(key)) is not int or not 0 <= capture[key] <= 4 * 1024 * 1024
                    for key in ("omitted_records", "invalid_records"))):
            return {"evidence": "INVALID"}
        quality["capture"] = {key: capture[key] for key in ("reason", "raw_truncated", "omitted_records", "invalid_records")}
    if state != "RECORDED":
        return {"evidence": state, **quality}
    raw = result.get("manager_journal")
    if not isinstance(raw, str):
        return {"evidence": "INVALID"}
    try:
        if len(raw.encode()) > 128 * 1024:
            return {"evidence": "INVALID"}
    except UnicodeEncodeError:
        return {"evidence": "INVALID"}
    lines = raw.splitlines()
    if len(lines) > 200:
        return {"evidence": "INVALID"}
    records, omitted = [], 0
    for line in lines:
        try:
            entry = bounded_json(line)
        except (ValueError, RecursionError):
            return {"evidence": "INVALID"}
        if not isinstance(entry, dict):
            return {"evidence": "INVALID"}
        message = entry.get("MESSAGE")
        if isinstance(message, list):
            # journald represents non-printable MESSAGE bytes, including ANSI
            # colors, as an integer array. Arrays of repeated fields are not text.
            if len(message) > 128 * 1024 or any(type(value) is not int or not 0 <= value <= 255 for value in message):
                return {"evidence": "INVALID"}
            try:
                message = bytes(message).decode("utf-8")
            except UnicodeDecodeError:
                return {"evidence": "INVALID"}
        if not isinstance(message, str):
            return {"evidence": "INVALID"}
        message = CSI.sub("", message)
        try:
            name = event(message)
        except RecursionError:
            return {"evidence": "INVALID"}
        if name is None:
            continue
        match = re.search(r"Process exited with status ([0-9]{1,3})(?=$|[ \"\\])", message)
        code = int(match[1]) if match and int(match[1]) <= 255 else None
        record = {"event": name, "process_exit_code": code, "outline": error_outline(message)}
        bootstrap = bootstrap_diagnostic(name, code, message)
        if bootstrap is not None:
            record["bootstrap"] = bootstrap
        records.append(record)
        if len(records) > 8:
            records.pop(0)
            omitted += 1
    limited = len(lines) == 200 or bool(quality and (capture["raw_truncated"] or capture["omitted_records"] or capture["invalid_records"]))
    return {"evidence": "RECORDED", "records": records, "omitted_count": omitted, "window_limited": limited, **quality}
