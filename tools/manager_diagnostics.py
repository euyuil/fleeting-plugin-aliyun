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
    if state != "RECORDED":
        return {"evidence": state}
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
        if len(records) == 8:
            omitted += 1
            continue
        match = re.search(r"Process exited with status ([0-9]{1,3})(?=$|[ \"\\])", message)
        code = int(match[1]) if match and int(match[1]) <= 255 else None
        records.append({"event": name, "process_exit_code": code, "outline": error_outline(message)})
    return {"evidence": "RECORDED", "records": records, "omitted_count": omitted, "window_limited": len(lines) == 200}
