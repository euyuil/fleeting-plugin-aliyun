"""Project only bounded, identity-free evidence from the last release call."""


def normal_release_diagnostic(result):
    raw = result.get("last_normal_release_observation")
    if raw is None:
        return {"evidence": "NOT_RECORDED"}
    if not isinstance(raw, dict):
        return {"evidence": "INVALID"}

    def number(value, maximum=1_000_000, minimum=0):
        return type(value) is int and minimum <= value <= maximum

    choices = {
        "result": {"PASS", "FAIL"},
        "deadline_scope": {"none", "normal_release", "parent_deadline", "parent_canceled", "operation_deadline", "operation_canceled"},
        "phase": {"inventory", "persist", "group", "members", "instances", "history", "evidence", "associated", "wait"},
        "associated_resources": {"not_started", "unconfirmed", "verified"},
    }
    if (any(not isinstance(raw.get(k), str) or raw[k] not in allowed for k, allowed in choices.items()) or
            not number(raw.get("call_index"), minimum=1) or not number(raw.get("elapsed_ms"), 3_600_000)):
        return {"evidence": "INVALID"}
    projected = {"evidence": "RECORDED", **{k: raw[k] for k in choices},
                 "call_index": raw["call_index"], "elapsed_ms": raw["elapsed_ms"]}
    sample, age = raw.get("last_complete_sample"), raw.get("sample_age_ms")
    if sample is None and age is None:
        if raw["result"] == "PASS":
            return {"evidence": "INVALID"}
        projected["last_complete_sample"] = {"evidence": "NOT_RECORDED"}
        projected["sample_age_ms"] = None
        return projected
    if not isinstance(sample, dict) or not number(age, 3_600_000):
        return {"evidence": "INVALID"}
    counts = ("desired_capacity", "total_capacity", "pending_capacity", "removing_capacity", "member_count", "instance_count")
    if (any(not number(sample.get(k)) for k in counts) or type(sample.get("history_quiet")) is not bool or
            not number(sample.get("stable_zero_samples"), 2) or not number(sample.get("span_ms"), 3_600_000)):
        return {"evidence": "INVALID"}
    if raw["result"] == "PASS" and (raw["deadline_scope"] != "none" or raw["phase"] != "associated" or
                                    raw["associated_resources"] != "verified" or sample["history_quiet"] is not True or
                                    sample["stable_zero_samples"] != 2 or any(sample[k] != 0 for k in counts)):
        return {"evidence": "INVALID"}
    projected["last_complete_sample"] = {"evidence": "RECORDED", **{k: sample[k] for k in counts},
                                         "history_quiet": sample["history_quiet"], "stable_zero_samples": sample["stable_zero_samples"],
                                         "span_ms": sample["span_ms"]}
    projected["sample_age_ms"] = age
    return projected
