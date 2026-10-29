"""Best-effort writer diagnostics; never evidence authorizing cloud operations."""
import json

from cloud_diagnostics import api_error, diagnostic_error
from release_candidate import write_json


PHASES = frozenset(("context", "authentication", "fixture_setup", "scale_request", "worker_inventory",
                    "worker_configuration", "creation_verification", "checkpoint", "await_interruption"))
REASONS = {
    "cleanup drill exceeded its one-worker budget": "worker_budget",
    "cleanup drill worker differs from the configured Spot fixture": "worker_configuration",
    "cleanup drill did not observe one running owned worker": "worker_readiness_deadline",
    "cleanup drill activity history is incomplete": "activity_history_incomplete",
    "cleanup drill activity is failed, unknown or malformed": "activity_invalid",
    "cleanup drill activity creation evidence is incomplete": "creation_evidence_incomplete",
    "cleanup drill activity history exceeds its one-worker budget": "creation_budget",
    "cleanup drill observed a replacement worker": "replacement_worker",
    "incomplete group member inventory; refusing forced deletion": "member_inventory_incomplete",
    "invalid group member row; refusing forced deletion": "member_row_invalid",
    "invalid group member identity; refusing forced deletion": "member_identity_invalid",
    "group contains unverified members; refusing forced deletion": "foreign_member",
    "cleanup drill supervisor did not interrupt at the checkpoint": "supervisor_deadline",
}
KINDS = frozenset(("cloud_api", "unavailable", "interrupted", "timeout", "validation", "io", "runtime", "response_shape", "unknown"))
CATEGORIES = frozenset(("authorization", "idempotency_conflict", "quota", "capacity", "throttling", "transport", "other_cloud_api"))


def error_details(error):
    result = dict(diagnostic_error(error))
    if isinstance(error, (KeyError, TypeError, AttributeError)):
        result["kind"] = "response_shape"
    # Match fixed source literals only; exception text is never copied publicly.
    if type(error) in (ValueError, RuntimeError) and str(error) in REASONS:
        result["reason"] = REASONS[str(error)]
    return result


class WriterDiagnostic:
    def __init__(self, directory):
        self.directory = directory
        self.data = {"schema_version": 1, "last_phase": None, "failure_phase": None, "error": None,
                     "cleanup": {"result": "NOT_STARTED", "error": None}}

    def save(self):
        try:
            temporary = self.directory / "writer-diagnostic.tmp"
            write_json(temporary, self.data)
            temporary.replace(self.directory / "writer-diagnostic.json")
        except OSError:
            # Optional diagnostic I/O must not suppress cleanup or the real error.
            pass

    def phase(self, phase):
        if phase not in PHASES:
            raise ValueError("unknown drill diagnostic phase")
        self.data["last_phase"] = phase
        self.save()

    def failure(self, error):
        self.data.update(failure_phase=self.data["last_phase"], error=error_details(error))
        self.save()

    def cleanup(self, result, error=None):
        self.data["cleanup"] = {"result": result, "error": error_details(error) if error is not None else None}
        self.save()


def public_error(value):
    if value is None:
        return None
    if not isinstance(value, dict):
        return {"kind": "unknown"}
    kind = value.get("kind")
    result = {"kind": kind if isinstance(kind, str) and kind in KINDS else "unknown"}
    if result["kind"] in ("cloud_api", "unavailable"):
        result.update(api_error(value.get("service"), value.get("action"), value.get("code")))
        category = value.get("category")
        if isinstance(category, str) and category in CATEGORIES:
            result["category"] = category
    reason = value.get("reason")
    if isinstance(reason, str) and reason in REASONS.values():
        result["reason"] = reason
    return result


def read_writer_diagnostic(directory, exit_code):
    result = {"evidence": "MISSING", "exit_code": exit_code if type(exit_code) is int and -255 <= exit_code <= 255 else None}
    try:
        with (directory / "writer-diagnostic.json").open() as source:
            raw = source.read(16_385)
        if len(raw) > 16_384:
            raise ValueError("oversized diagnostic")
        data = json.loads(raw)
        if not isinstance(data, dict) or type(data.get("schema_version")) is not int or data["schema_version"] != 1:
            raise ValueError("invalid diagnostic")
        phases = {key: data.get(key) for key in ("last_phase", "failure_phase")}
        if any(value is not None and (not isinstance(value, str) or value not in PHASES) for value in phases.values()):
            raise ValueError("invalid phase")
        cleanup = data.get("cleanup")
        if not isinstance(cleanup, dict) or cleanup.get("result") not in ("NOT_STARTED", "RUNNING", "PASS", "FAIL"):
            raise ValueError("invalid cleanup diagnostic")
        result.update(evidence="RECORDED", **phases, error=public_error(data.get("error")),
                      cleanup={"result": cleanup["result"], "error": public_error(cleanup.get("error"))})
    except FileNotFoundError:
        pass
    except (OSError, ValueError, TypeError, RecursionError):
        result["evidence"] = "UNREADABLE"
    return result
