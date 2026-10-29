"""Public diagnostics must retain independent failures without private data."""
import json
import unittest

from cloud_diagnostics import CloudOperationError, api_error, diagnostic_error, driver_diagnostic, phase_diagnostics


def marker(event, scope="scenario", phase="ssh", elapsed=0, **fields):
    return "FLEETING_DIAGNOSTIC " + json.dumps(dict(event=event, scope=scope, phase=phase, elapsed_ms=elapsed, **fields)) + "\n"


class DiagnosticTests(unittest.TestCase):
    def test_scenario_and_cleanup_failures_keep_their_own_phase_and_error(self):
        log = (marker("phase", phase="observe_before_ssh") +
               marker("api_error", phase="observe_before_ssh", elapsed=10, service="ecs", action="DescribeInstances", code="Forbidden.RAM") +
               marker("result", phase="observe_before_ssh", elapsed=20, result="FAIL") +
               marker("phase", "cleanup", "driver_cleanup", 21) +
               marker("api_error", "cleanup", "driver_cleanup", 30, service="ess", action="RemoveInstances", code="IdempotentParameterMismatch") +
               marker("failure", "cleanup", "driver_cleanup", 31) +
               marker("phase", "cleanup", "evidence_write", 40) +
               marker("result", "cleanup", "evidence_write", 45, result="FAIL"))
        result = phase_diagnostics(log)
        scenario, cleanup = result["scenario"], result["cleanup"]
        self.assertEqual(scenario["failure_phase"], "observe_before_ssh")
        self.assertEqual(scenario["result"], "FAIL")
        self.assertEqual(scenario["phases"]["observe_before_ssh"]["duration_ms"], 20)
        self.assertEqual(cleanup["failure_phase"], "driver_cleanup")
        self.assertEqual(cleanup["last_phase"], "evidence_write")
        self.assertEqual(cleanup["result"], "FAIL")
        self.assertEqual(cleanup["phases"]["driver_cleanup"]["api_errors"][0]["code"], "IdempotentParameterMismatch")
        self.assertNotIn("IdempotentParameterMismatch", json.dumps(scenario))

    def test_partial_trace_stays_unknown_and_retries_aggregate(self):
        log = (marker("phase", phase="connect_info") + marker("phase", elapsed=5) +
               "ssh: handshake failed: ssh: unable to authenticate private-key\n" +
               marker("phase", phase="connect_info", elapsed=15) + marker("phase", elapsed=20))
        result = phase_diagnostics(log)
        self.assertEqual(result["scenario"]["result"], "UNKNOWN")
        self.assertEqual(result["cleanup"]["result"], "UNKNOWN")
        self.assertEqual(result["scenario"]["phases"]["ssh"]["visits"], 2)
        self.assertEqual(result["scenario"]["phases"]["ssh"]["duration_ms"], 10)
        self.assertEqual(result["scenario"]["phases"]["ssh"]["transport_error_categories"], ["authentication", "handshake"])
        self.assertNotIn("private", json.dumps(result))

    def test_phase_templates_never_contain_interpolated_log_values(self):
        log = (marker("phase") + "live SSH nonce did not match: private-value\n" +
               marker("result", elapsed=10, result="FAIL"))
        result = driver_diagnostic({}, 1, log)
        self.assertEqual(result["stages"]["scenario"]["phases"]["ssh"]["matched_error_templates"], ["live SSH nonce did not match"])
        self.assertNotIn("private", json.dumps(result))

    def test_unknown_fields_and_malformed_markers_cannot_escape(self):
        for value in ([], {}, "private", True, -1, 3_600_001):
            log = "FLEETING_DIAGNOSTIC not-json\nFLEETING_DIAGNOSTIC []\n" + marker("phase", elapsed=value)
            result = phase_diagnostics(log)
            self.assertIsNone(result["scenario"]["last_phase"])
        log = (marker("phase") + marker("api_error", elapsed=1, service="private-service", action="private-action", code="Forbidden.PrivateValue") +
               marker("phase", phase="private-phase", elapsed=2) + marker("result", elapsed=3, result="private-result"))
        result = phase_diagnostics(log)
        self.assertNotIn("private", json.dumps(result).lower())
        self.assertEqual(result["scenario"]["result"], "UNKNOWN")
        self.assertEqual(result["scenario"]["phases"]["ssh"]["api_errors"][0],
                         {"service": "unknown", "action": "unknown", "code": "unknown", "category": "authorization"})
        self.assertEqual(api_error([], {}, []), {"service": "unknown", "action": "unknown", "code": "unknown", "category": "other_cloud_api"})

    def test_cli_errors_publish_only_known_code_action_and_category(self):
        for raw in ('ERROR: SDK.ServerError\nErrorCode: IdempotentParameterMismatch\nRequestId: private-request',
                    '{"error_code":"IdempotentParameterMismatch","request_id":"private-request"}',
                    '{"Code":"IdempotentParameterMismatch","Message":"private-message"}'):
            error = CloudOperationError("ess", "RemoveInstances", output=raw)
            result = diagnostic_error(error)
            self.assertEqual(result["code"], "IdempotentParameterMismatch")
            self.assertEqual(result["action"], "RemoveInstances")
            self.assertNotIn("private", str(error) + json.dumps(result))
        self.assertEqual(diagnostic_error(ValueError("private-message")), {"kind": "validation"})


if __name__ == "__main__":
    unittest.main()
