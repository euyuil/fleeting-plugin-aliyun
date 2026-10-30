"""Public diagnostics must retain independent failures without private data."""
import json
import unittest

from cloud_diagnostics import CloudOperationError, api_error, diagnostic_error, driver_diagnostic, matching_error_templates, phase_diagnostics


def marker(event, scope="scenario", phase="ssh", elapsed=0, **fields):
    return "FLEETING_DIAGNOSTIC " + json.dumps(dict(event=event, scope=scope, phase=phase, elapsed_ms=elapsed, **fields)) + "\n"


class DiagnosticTests(unittest.TestCase):
    def test_unclassified_failure_is_preserved_without_exposing_private_errors(self):
        result = driver_diagnostic({"result": "FAIL_UNCLASSIFIED", "errors": ["synthetic-private"]}, 1, "")
        self.assertEqual(result["result"], "FAIL_UNCLASSIFIED")
        self.assertNotIn("synthetic-private", json.dumps(result))

    def test_product_violation_literals_remain_public_without_private_values(self):
        source = ('return productViolation("actual ECS instance is not Spot")\n'
                  'return productViolation(fmt.Sprintf("instance creation budget %d exceeded, including replacements", budget))')
        log = "actual ECS instance is not Spot\ninstance creation budget 9 exceeded, including replacements\nsynthetic-private-id"
        actual = matching_error_templates(log, source)
        self.assertEqual(actual, ["actual ECS instance is not Spot", "instance creation budget %d exceeded, including replacements"])
        self.assertNotIn("synthetic-private", json.dumps(actual))

    def test_auto_release_error_uses_an_exact_public_code(self):
        code = "InvalidAutoReleaseTime.Malformed"
        result = api_error("ecs", "ModifyInstanceAutoReleaseTime", code)
        self.assertEqual(result["code"], code)
        self.assertEqual(result["action"], "ModifyInstanceAutoReleaseTime")
        self.assertEqual(api_error("ecs", "ModifyInstanceAutoReleaseTime", code + ".private")["code"], "unknown")

    def test_formatted_errors_keep_static_suffixes_and_never_values(self):
        source = ('fmt.Sprintf("GitLab API HTTP %d", status)\n'
                  'fmt.Errorf("member %s is not verified as releasable by ESS", id)\n'
                  'fmt.Errorf("%v: %w", first, second)')
        templates = matching_error_templates("GitLab API HTTP 503\nmember synthetic-private-id is not verified as releasable by ESS", source)
        self.assertEqual(templates, ["GitLab API HTTP %d", "member %s is not verified as releasable by ESS"])
        self.assertNotIn("synthetic-private", json.dumps(templates))
        self.assertEqual(matching_error_templates("member private-id has an unrelated error", source), [])

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

    def test_empty_prefix_template_cannot_move_an_error_into_a_clean_phase(self):
        template = "%w: request_id=%s page=%d row=%d missing_instance_id=%t missing_group_id=%t lifecycle=%q"
        log = (marker("phase", phase="observe_before_ssh") +
               'incomplete identity: request_id=private page=1 row=0 missing_instance_id=true missing_group_id=false lifecycle="Pending"\n' +
               marker("result", phase="observe_before_ssh", elapsed=10, result="FAIL") +
               marker("phase", "cleanup", "driver_cleanup", 11) + "ordinary cleanup progress\n" +
               marker("result", "cleanup", "driver_cleanup", 20, result="PASS"))
        actual = phase_diagnostics(log, [template])
        self.assertEqual(actual["scenario"]["phases"]["observe_before_ssh"]["matched_error_templates"], [template])
        self.assertEqual(actual["cleanup"]["phases"]["driver_cleanup"]["matched_error_templates"], [])
        self.assertEqual(actual["cleanup"]["result"], "PASS")
        self.assertNotIn("private", json.dumps(actual))

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
