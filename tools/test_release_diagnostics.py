import copy
import json
import unittest

from job_acceptance import result_diagnostic
from release_diagnostics import normal_release_diagnostic


class ReleaseDiagnosticTests(unittest.TestCase):
    def setUp(self):
        self.record = {"call_index": 1, "result": "FAIL", "deadline_scope": "normal_release", "phase": "inventory",
                       "elapsed_ms": 180001, "associated_resources": "not_started", "sample_age_ms": 1024,
                       "last_complete_sample": {"desired_capacity": 1, "total_capacity": 1, "pending_capacity": 0,
                                                "removing_capacity": 0, "member_count": 1, "instance_count": 1,
                                                "history_quiet": True, "stable_zero_samples": 0, "span_ms": 25}}

    def project(self, record):
        return normal_release_diagnostic({"last_normal_release_observation": record})

    def test_failure_projection_keeps_scope_and_pre_cleanup_counts(self):
        result = {"last_normal_release_observation": self.record, "instances": [], "normal_release": False}
        public = result_diagnostic(result, 1)["last_normal_release"]
        self.assertEqual(public["evidence"], "RECORDED")
        self.assertEqual(public["deadline_scope"], "normal_release")
        self.assertEqual(public["last_complete_sample"]["instance_count"], 1)
        self.assertEqual(public["sample_age_ms"], 1024)
        self.assertEqual(self.record["last_complete_sample"]["instance_count"], 1)

    def test_success_and_no_complete_sample_are_not_inferred_as_current_failure(self):
        record = copy.deepcopy(self.record)
        record.update(result="PASS", deadline_scope="none", phase="associated", associated_resources="verified")
        record["last_complete_sample"].update(desired_capacity=0, total_capacity=0, member_count=0, instance_count=0, stable_zero_samples=2)
        self.assertEqual(self.project(record)["result"], "PASS")
        for key, value in (("deadline_scope", "normal_release"), ("last_complete_sample", None)):
            invalid = copy.deepcopy(record)
            invalid[key] = value
            self.assertEqual(self.project(invalid), {"evidence": "INVALID"})
        record.update(result="FAIL", last_complete_sample=None, sample_age_ms=None)
        self.assertEqual(self.project(record)["last_complete_sample"], {"evidence": "NOT_RECORDED"})
        self.assertEqual(normal_release_diagnostic({}), {"evidence": "NOT_RECORDED"})

    def test_invalid_or_unbounded_fields_never_become_observed_zero(self):
        for field, value in (("phase", "PRIVATE-CANARY"), ("elapsed_ms", -1), ("elapsed_ms", 10**30),
                             ("call_index", True), ("deadline_scope", []), ("sample_age_ms", None)):
            with self.subTest(field=field, value=value):
                record = copy.deepcopy(self.record)
                record[field] = value
                self.assertEqual(self.project(record), {"evidence": "INVALID"})
        for field, value in (("instance_count", "PRIVATE-CANARY"), ("member_count", True), ("span_ms", 10**30),
                             ("history_quiet", "yes"), ("stable_zero_samples", 3)):
            record = copy.deepcopy(self.record)
            record["last_complete_sample"][field] = value
            self.assertEqual(self.project(record), {"evidence": "INVALID"})

    def test_unknown_fields_and_identities_are_never_copied(self):
        record = copy.deepcopy(self.record)
        record["raw_error"] = "PRIVATE-CANARY"
        record["last_complete_sample"]["resource_ids"] = ["PRIVATE-CANARY"]
        public = self.project(record)
        self.assertEqual(public["evidence"], "RECORDED")
        self.assertNotIn("PRIVATE-CANARY", json.dumps(public))
