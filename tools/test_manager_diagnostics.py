"""Manager lifecycle diagnostics expose fixed events without journal identities."""
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest

import job_acceptance
from job_diagnostics import ERROR_WORDS
from manager_diagnostics import manager_diagnostic


def result(*messages):
    return {"manager_journal_state": "RECORDED", "manager_journal": "".join(
        json.dumps({"MESSAGE": message, "_HOSTNAME": "private-host", "_PID": "123456"}) + "\n" for message in messages)}


class ManagerDiagnosticTests(unittest.TestCase):
    def test_default_and_json_runner_formats_expose_readiness_failure(self):
        error = "ready command: Process exited with status 124"
        messages = (
            '\x1b[31;1mERROR: ready up preparation failed            \x1b[0m err=' + error +
            ' instance=i-private runner=private-token subsystem=taskscaler',
            json.dumps({"level": "error", "msg": "ready up preparation failed", "err": error,
                        "instance": "i-private", "runner": "private-token", "subsystem": "taskscaler"}),
        )
        for message in (*messages, list(messages[0].encode())):
            with self.subTest(message=message):
                value = job_acceptance.result_diagnostic(result(message), 1)["manager"]
                self.assertEqual(value["evidence"], "RECORDED")
                self.assertEqual(value["records"][0]["event"], "ready_up_preparation_failed")
                self.assertEqual(value["records"][0]["process_exit_code"], 124)
                self.assertTrue(set(value["records"][0]["outline"]["words"]) <= ERROR_WORDS | {"?"})
                for private in ("private", "123456", "MESSAGE", "_HOSTNAME"):
                    self.assertNotIn(private, json.dumps(value))

    def test_event_order_and_bounds_do_not_publish_arbitrary_messages(self):
        removed = "instance marked for removal                   instance=i-private reason=instance ready up preparation failed subsystem=taskscaler"
        unknown = "unrelated private message instance=i-private subsystem=taskscaler"
        failed = "ERROR: connection preparation failed err=private subsystem=taskscaler"
        value = manager_diagnostic(result(removed, failed, unknown, *(removed for _ in range(9))))
        self.assertEqual(len(value["records"]), 8)
        self.assertEqual(value["omitted_count"], 3)
        self.assertFalse(value["window_limited"])
        self.assertEqual([r["event"] for r in value["records"][:2]], ["instance_marked_for_removal", "connection_preparation_failed"])
        self.assertNotIn("private", json.dumps(value))
        self.assertTrue(manager_diagnostic(result(*(unknown for _ in range(200))))["window_limited"])
        for message in (removed.replace("subsystem=taskscaler", "subsystem=private"), "$ echo " + removed,
                        json.dumps({"msg": "private", "subsystem": "taskscaler"})):
            self.assertEqual(manager_diagnostic(result(message))["records"], [])

    def test_unavailable_or_malformed_evidence_never_becomes_empty_success(self):
        self.assertEqual(manager_diagnostic({}), {"evidence": "NOT_RECORDED"})
        for state in ("UNAVAILABLE", "NOT_STARTED"):
            self.assertEqual(manager_diagnostic({"manager_journal_state": state, "manager_journal": "private"}), {"evidence": state})
        malformed = [None, {}, [], 1, "private", '{"MESSAGE": 7}', "[]", "\ud800", "x" * (128 * 1024 + 1)]
        malformed.extend(json.dumps({"MESSAGE": value}) for value in ([True], [256], [-1], [1.0], [255], ["private", "duplicate"]))
        for raw in malformed:
            self.assertEqual(manager_diagnostic({"manager_journal_state": "RECORDED", "manager_journal": raw}), {"evidence": "INVALID"})
        self.assertEqual(manager_diagnostic(result(*("private" for _ in range(201)))), {"evidence": "INVALID"})
        for state in ([], {}, "private", 1):
            self.assertEqual(manager_diagnostic({"manager_journal_state": state}), {"evidence": "INVALID"})

    def test_process_exit_codes_are_bounded_and_not_address_fragments(self):
        for code in ("-1", "256", "999", "123456", "192.0.2.1"):
            message = "ERROR: ready up preparation failed err=ready command: Process exited with status " + code + " subsystem=taskscaler"
            self.assertIsNone(manager_diagnostic(result(message))["records"][0]["process_exit_code"])

    def test_brackets_and_escaped_quotes_in_log_values_are_not_structural_depth(self):
        error = 'private "quoted" \\ ' + "[{" * 100
        for message in (json.dumps({"msg": "ready up preparation failed", "subsystem": "taskscaler", "err": error}),
                        "ERROR: ready up preparation failed err=" + error + " subsystem=taskscaler"):
            value = manager_diagnostic(result(message))
            self.assertEqual(value["evidence"], "RECORDED")
            self.assertEqual(value["records"][0]["event"], "ready_up_preparation_failed")
            self.assertNotIn("private", json.dumps(value))

    def test_nested_malformed_evidence_cannot_mask_the_original_driver_failure(self):
        nested = "[" * 10000 + "0" + "]" * 10000
        for raw in (nested, result(nested)["manager_journal"]):
            value = {"manager_journal_state": "RECORDED", "manager_journal": raw, "result": "FAIL_PRODUCT",
                     "github_run_id": "10", "github_run_attempt": "1", "errors": ["private-original"]}
            self.assertEqual(job_acceptance.result_diagnostic(value, 1)["manager"], {"evidence": "INVALID"})
            with tempfile.TemporaryDirectory() as temp:
                fixture = SimpleNamespace(directory=Path(temp), driver_exit_code=1)
                path = fixture.directory / "evidence" / "run-10-1" / "result.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps(value))
                public = job_acceptance.saved_driver_diagnostic(fixture, "10", "1")
            self.assertEqual(public["result"], "FAIL_PRODUCT")
            self.assertEqual(public["manager"], {"evidence": "INVALID"})
            self.assertNotIn("private", json.dumps(public))


if __name__ == "__main__":
    unittest.main()
