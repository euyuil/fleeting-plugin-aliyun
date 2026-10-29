"""Exercise crash checkpoints and independent recovery without cloud access."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import cloud_cleanup_drill as drill
import cloud_fixture as fixture
from cloud_diagnostics import CloudOperationError
from cloud_drill_diagnostics import WriterDiagnostic, read_writer_diagnostic
from test_cloud_config import CONFIG, CONFIG_ENV
from test_cloud_fixture import FakeCloud


class CleanupDrillTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.tags = fixture.labels("example/plugin", "a" * 40, "123", "1", fixture.DRILL_MODE, "2030-01-01T00:00:00Z")
        self.context = (self.tags, CONFIG, "192.0.2.1/32")
        self.expected = drill.checkpoint(self.tags, CONFIG)

    def test_manual_confirmation_and_source_are_checked_before_starting_a_writer(self):
        env = {"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_WORKFLOW": "Cloud cleanup drill", "GITHUB_SHA": "a" * 40}
        for changes, confirmed in (({}, False), ({"GITHUB_EVENT_NAME": "push"}, True), ({"GITHUB_WORKFLOW": "Release"}, True)):
            with mock.patch.dict(os.environ, {**env, **changes}), mock.patch("cloud_cleanup_drill.require_linux"), mock.patch("cloud_cleanup_drill.preflight"):
                with mock.patch("subprocess.check_output") as shell, self.assertRaises(ValueError):
                    drill.drill_context(confirmed)
                shell.assert_not_called()
        with mock.patch.dict(os.environ, env), mock.patch("cloud_cleanup_drill.require_linux"), mock.patch("cloud_cleanup_drill.preflight"):
            with mock.patch("subprocess.check_output", return_value="b" * 40), self.assertRaisesRegex(ValueError, "source differs"):
                drill.drill_context(True)

    def test_successful_injection_is_nonzero_and_never_cleans_in_the_source_process(self):
        receipt = self.directory / "public.json"
        with mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context), mock.patch("subprocess.Popen") as spawn:
            with mock.patch("cloud_cleanup_drill.interrupt_at_checkpoint"), mock.patch("cloud_cleanup_drill.Cloud") as cloud:
                self.assertEqual(drill.run(self.directory / "private", receipt, True), 86)
        cloud.assert_not_called()
        self.assertTrue(spawn.call_args.kwargs["start_new_session"])
        result = json.loads(receipt.read_text())
        self.assertEqual(result["result"], "INTERRUPTED")
        self.assertEqual(result["workers_observed"], 1)
        self.assertTrue(result["writer_terminated"])
        self.assertEqual(result["cleanup_result"], "PENDING_INDEPENDENT_RECOVERY")
        self.assertNotIn("private", json.dumps(result))
        self.assertNotIn(fixture.DRILL_MODE, fixture.MODES)

    def test_failed_checkpoint_stops_the_writer_before_guarded_fallback_cleanup(self):
        order = []
        with mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context), mock.patch("subprocess.Popen"):
            with mock.patch("cloud_cleanup_drill.interrupt_at_checkpoint", side_effect=ValueError("private-checkpoint-error")):
                with mock.patch("cloud_cleanup_drill.stop_process_group", side_effect=lambda _: order.append("stop")):
                    with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
                        owner.return_value.cleanup.side_effect = lambda: order.append("cleanup")
                        with self.assertRaises(ValueError):
                            drill.run(self.directory / "private", self.directory / "public.json", True)
        self.assertEqual(order, ["stop", "cleanup"])
        result = json.loads((self.directory / "public.json").read_text())
        self.assertEqual((result["result"], result["cleanup_result"]), ("FAIL", "PASS"))
        self.assertNotIn("private", json.dumps(result))

    def test_unknown_spawn_or_unproven_termination_never_authorizes_deletion(self):
        for spawn_error in (True, False):
            with self.subTest(spawn_error=spawn_error), mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context):
                with mock.patch("subprocess.Popen", side_effect=KeyboardInterrupt if spawn_error else None):
                    with mock.patch("cloud_cleanup_drill.interrupt_at_checkpoint", side_effect=TimeoutError), mock.patch("cloud_cleanup_drill.stop_process_group", side_effect=RuntimeError("private-process-error")):
                        with mock.patch("cloud_cleanup_drill.Cloud") as cloud, self.assertRaises(KeyboardInterrupt if spawn_error else TimeoutError):
                            drill.run(self.directory / str(spawn_error), self.directory / "public.json", True)
                        cloud.assert_not_called()
            result = json.loads((self.directory / "public.json").read_text())
            self.assertFalse(result["writer_terminated"])
            self.assertNotIn("private", json.dumps(result))

    def test_existing_directory_and_foreign_checkpoint_are_rejected(self):
        with mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context), mock.patch("subprocess.Popen") as spawn:
            with self.assertRaises(FileExistsError):
                drill.run(self.directory, self.directory / "public.json", True)
            spawn.assert_not_called()
        path = self.directory / "checkpoint.json"
        process = mock.Mock()
        process.poll.return_value = None
        for mutation in ({"run_id": "124"}, {"workers_observed": True}, {"source_sha": "b" * 40}):
            fixture.write_json(path, {**self.expected, **mutation})
            with mock.patch("cloud_cleanup_drill.stop_process_group") as stop, self.assertRaises(ValueError):
                drill.interrupt_at_checkpoint(process, path, self.expected)
            stop.assert_not_called()

    def test_worker_budget_and_config_are_enforced_without_retrying_expansion(self):
        worker = {"InstanceId": "i-synthetic", "InstanceType": CONFIG["instance_type"], "ImageId": CONFIG["image"], "ZoneId": CONFIG["zone"],
                  "SpotStrategy": "SpotAsPriceGo", "InstanceChargeType": "PostPaid", "Status": "Running"}
        owner = mock.Mock()
        owner.state = {"ids": {"instance": ["i-synthetic"]}}
        for workers in ([worker, worker], [{**worker, "SpotStrategy": "NoSpot"}], [{**worker, "ImageId": "other-image"}]):
            owner.owned.return_value = workers
            with self.assertRaises(ValueError):
                drill.wait_for_worker(owner, CONFIG)
        owner.owned.return_value = [worker]
        with mock.patch("cloud_cleanup_drill.verify_drill_creation", return_value=True):
            drill.wait_for_worker(owner, CONFIG)
        owner.state["ids"]["instance"].append("i-previous")
        with self.assertRaisesRegex(ValueError, "one-worker budget"):
            drill.wait_for_worker(owner, CONFIG)
        owner.cloud.call.assert_not_called()

    def test_activity_history_rejects_unobserved_replacements_or_incomplete_evidence(self):
        owner = mock.Mock()
        owner.state = {"ids": {"group": "synthetic-group", "instance": ["i-current"]}}
        owner.owned.return_value = [{"InstanceId": "i-current"}]
        activity = {"ScalingActivityId": "synthetic-activity", "ScalingGroupId": "synthetic-group", "CreatedCapacity": 1,
                    "CreatedInstances": {"CreatedInstance": ["i-current"]}, "StatusCode": "Successful"}
        history = {"TotalCount": 1, "ScalingActivities": {"ScalingActivity": [activity]}}
        members = {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [{"ScalingGroupId": "synthetic-group", "InstanceId": "i-current"}]}}
        owner.cloud.call.side_effect = [history, members]
        self.assertTrue(fixture.verify_drill_creation(owner, "i-current"))
        for response in ({**history, "TotalCount": 2},
                         {"TotalCount": 2, "ScalingActivities": {"ScalingActivity": [activity, {**activity, "ScalingActivityId": "old", "CreatedInstances": {"CreatedInstance": ["i-old"]}}]}},
                         {"TotalCount": 1, "ScalingActivities": {"ScalingActivity": [{**activity, "CreatedCapacity": 0}]}},
                         {"TotalCount": 1, "ScalingActivities": {"ScalingActivity": [{**activity, "StatusCode": "Warning"}]}}):
            with self.subTest(response=response):
                owner.cloud.call.side_effect = None
                owner.cloud.call.return_value = response
                with self.assertRaises(ValueError):
                    fixture.verify_drill_creation(owner, "i-current")

    def test_cleanup_started_before_kill_rejects_the_injection_proof(self):
        directory = self.directory / "private"
        def interrupt(*_args):
            (directory / "cleanup-started").touch()
        with mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context), mock.patch("subprocess.Popen"):
            with mock.patch("cloud_cleanup_drill.interrupt_at_checkpoint", side_effect=interrupt), mock.patch("cloud_cleanup_drill.stop_process_group") as stop:
                with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
                    with self.assertRaisesRegex(RuntimeError, "already entered cleanup"):
                        drill.run(directory, self.directory / "public.json", True)
                    owner.return_value.cleanup.assert_called_once()
                stop.assert_not_called()
        result = json.loads((self.directory / "public.json").read_text())
        self.assertEqual(result["result"], "FAIL")
        self.assertNotEqual(result["cleanup_result"], "PENDING_INDEPENDENT_RECOVERY")

    def test_writer_requests_one_worker_once_and_cleans_if_the_reply_is_lost(self):
        fixture.write_json(self.directory / "context.json", {"tags": self.tags, "config": CONFIG, "cidr": self.context[2]})
        with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
            owner.return_value.state = {"ids": {"group": "synthetic-group"}}
            owner.return_value.cloud.call.side_effect = RuntimeError("lost accepted response")
            with self.assertRaisesRegex(RuntimeError, "lost accepted response"):
                drill.writer(self.directory)
        owner.return_value.cloud.call.assert_called_once()
        params = owner.return_value.cloud.call.call_args.kwargs
        self.assertEqual(params["AdjustmentValue"], 1)
        self.assertEqual(params["AdjustmentType"], "TotalCapacity")
        owner.return_value.cleanup.assert_called_once()
        self.assertFalse((self.directory / "checkpoint.json").exists())

    def test_actual_child_failure_reaches_parent_receipt_with_independent_cleanup_errors(self):
        # Exercise the process/file boundary that lost the original live failure.
        # Only the child's cloud boundary is fake; no credentials are consulted.
        script = """
import pathlib, sys
sys.path.insert(0, sys.argv[1])
import cloud_cleanup_drill as drill
from cloud_diagnostics import CloudOperationError
class Owner:
    def __init__(self, *_args):
        self.state = {'ids': {'group': 'synthetic-group'}}
        self.cloud = self
    def prepare(self, *_args): pass
    def call(self, *_args, **_kwargs):
        raise CloudOperationError('ess', 'ScaleWithAdjustment', output='{"Code":"Forbidden.RAM","Message":"private-secret","RequestId":"private-request"}')
    def cleanup(self): raise RuntimeError('private-cleanup-error')
drill.Cloud = lambda *_args: None
drill.Fixture = Owner
drill.writer(pathlib.Path(sys.argv[2]))
"""
        popen = subprocess.Popen
        def spawn(args, **kwargs):
            return popen([sys.executable, "-c", script, str(Path(drill.__file__).parent), args[-1]], **kwargs)
        def stopped(process):
            self.assertEqual(process.wait(timeout=5), 1)
        receipt = self.directory / "public.json"
        with mock.patch("cloud_cleanup_drill.drill_context", return_value=self.context), mock.patch("subprocess.Popen", side_effect=spawn):
            with mock.patch("cloud_cleanup_drill.stop_process_group", side_effect=stopped), mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
                with self.assertRaisesRegex(RuntimeError, "exited before"):
                    drill.run(self.directory / "private", receipt, True)
                owner.return_value.cleanup.assert_called_once()
        result = json.loads(receipt.read_text())
        self.assertEqual((result["result"], result["cleanup_result"]), ("FAIL", "PASS"))
        writer = result["writer"]
        self.assertEqual((writer["evidence"], writer["exit_code"], writer["failure_phase"]), ("RECORDED", 1, "scale_request"))
        self.assertEqual(writer["error"], {"kind": "cloud_api", "service": "ess", "action": "ScaleWithAdjustment", "code": "Forbidden.RAM", "category": "authorization"})
        self.assertEqual(writer["cleanup"], {"result": "FAIL", "error": {"kind": "runtime"}})
        self.assertNotIn("private", receipt.read_text())

    def test_writer_preserves_failure_before_and_during_setup(self):
        context = self.directory / "context.json"
        context.write_text("invalid private context")
        with self.assertRaises(ValueError):
            drill.writer(self.directory)
        result = read_writer_diagnostic(self.directory, 1)
        self.assertEqual((result["failure_phase"], result["error"], result["cleanup"]["result"]), ("context", {"kind": "validation"}, "NOT_STARTED"))
        fixture.write_json(context, {"tags": self.tags, "config": CONFIG, "cidr": self.context[2]})
        error = CloudOperationError("sts", "GetCallerIdentity", output='{"Code":"AccessDenied","Message":"private"}')
        with mock.patch("cloud_cleanup_drill.Cloud", side_effect=error), self.assertRaises(CloudOperationError):
            drill.writer(self.directory)
        self.assertEqual(read_writer_diagnostic(self.directory, 1)["failure_phase"], "authentication")
        with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
            owner.return_value.prepare.side_effect = ValueError("private-setup-error")
            owner.return_value.cleanup.side_effect = RuntimeError("private-cleanup-error")
            with self.assertRaisesRegex(ValueError, "private-setup-error"):
                drill.writer(self.directory)
        result = read_writer_diagnostic(self.directory, 1)
        self.assertEqual((result["failure_phase"], result["error"]["kind"], result["cleanup"]["result"]), ("fixture_setup", "validation", "FAIL"))
        self.assertNotIn("private", json.dumps(result))

    def test_actual_history_failure_records_phase_and_fixed_reason(self):
        fixture.write_json(self.directory / "context.json", {"tags": self.tags, "config": CONFIG, "cidr": self.context[2]})
        worker = {"InstanceId": "i-synthetic", "InstanceType": CONFIG["instance_type"], "ImageId": CONFIG["image"], "ZoneId": CONFIG["zone"],
                  "SpotStrategy": "SpotAsPriceGo", "InstanceChargeType": "PostPaid", "Status": "Running"}
        with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
            owner.return_value.state = {"ids": {"group": "synthetic-group", "instance": ["i-synthetic"]}}
            owner.return_value.owned.return_value = [worker]
            owner.return_value.cloud.call.side_effect = [{}, {"TotalCount": 1, "ScalingActivities": {"ScalingActivity": []}}]
            with self.assertRaisesRegex(ValueError, "history is incomplete"):
                drill.writer(self.directory)
            owner.return_value.cleanup.assert_called_once()
        result = read_writer_diagnostic(self.directory, 1)
        self.assertEqual(result["failure_phase"], "creation_verification")
        self.assertEqual(result["error"], {"kind": "validation", "reason": "activity_history_incomplete"})
        self.assertEqual(result["cleanup"]["result"], "PASS")

    def test_diagnostic_write_failure_does_not_suppress_cleanup_or_primary_error(self):
        fixture.write_json(self.directory / "context.json", {"tags": self.tags, "config": CONFIG, "cidr": self.context[2]})
        with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
            owner.return_value.prepare.side_effect = KeyError("private-response")
            with mock.patch("cloud_drill_diagnostics.write_json", side_effect=OSError("disk full")):
                with self.assertRaises(KeyError):
                    drill.writer(self.directory)
            owner.return_value.cleanup.assert_called_once()
        self.assertEqual(read_writer_diagnostic(self.directory, 1)["evidence"], "MISSING")

    def test_writer_retains_cancellation_priority_and_both_failures(self):
        for primary, cleanup, expected in ((KeyboardInterrupt, RuntimeError, KeyboardInterrupt),
                                           (ValueError, KeyboardInterrupt, KeyboardInterrupt)):
            with self.subTest(primary=primary, cleanup=cleanup):
                directory = self.directory / primary.__name__
                directory.mkdir()
                fixture.write_json(directory / "context.json", {"tags": self.tags, "config": CONFIG, "cidr": self.context[2]})
                with mock.patch("cloud_cleanup_drill.Cloud"), mock.patch("cloud_cleanup_drill.Fixture") as owner:
                    owner.return_value.prepare.side_effect = primary("private-primary")
                    owner.return_value.cleanup.side_effect = cleanup("private-cleanup")
                    with self.assertRaises(expected):
                        drill.writer(directory)
                    owner.return_value.cleanup.assert_called_once()
                result = read_writer_diagnostic(directory, 1)
                self.assertEqual(result["failure_phase"], "fixture_setup")
                self.assertEqual(result["cleanup"]["result"], "FAIL")
                self.assertEqual(result["error"]["kind"], "interrupted" if primary is KeyboardInterrupt else "validation")
                self.assertEqual(result["cleanup"]["error"]["kind"], "interrupted" if cleanup is KeyboardInterrupt else "runtime")
                self.assertNotIn("private", json.dumps(result))

    def test_diagnostic_reader_projects_only_bounded_public_fields(self):
        diagnostic = WriterDiagnostic(self.directory)
        diagnostic.phase("creation_verification")
        diagnostic.failure(AttributeError("private-field"))
        path = self.directory / "writer-diagnostic.json"
        data = json.loads(path.read_text())
        data.update(raw_log="private-log", resource_id="private-resource")
        data["error"].update(message="private-error", reason="private-reason")
        data["cleanup"]["private"] = "private-token"
        fixture.write_json(path, data)
        result = read_writer_diagnostic(self.directory, True)
        self.assertEqual(result["error"], {"kind": "response_shape"})
        self.assertIsNone(result["exit_code"])
        self.assertNotIn("private", json.dumps(result))
        for raw in ("{", "[" * 2000, "x" * 16_385, json.dumps({**data, "schema_version": True}),
                    json.dumps({**data, "last_phase": "private-phase"})):
            path.write_text(raw)
            self.assertEqual(read_writer_diagnostic(self.directory, 1), {"evidence": "UNREADABLE", "exit_code": 1})

    def test_opaque_api_values_are_reclassified_without_copying_private_text(self):
        diagnostic = WriterDiagnostic(self.directory)
        diagnostic.phase("scale_request")
        diagnostic.failure(CloudOperationError("ess", "ScaleWithAdjustment", output='{"Code":"Forbidden.private-id","Message":"private-secret"}'))
        result = read_writer_diagnostic(self.directory, 1)
        self.assertEqual((result["error"]["category"], result["error"]["code"]), ("authorization", "unknown"))
        self.assertNotIn("private", json.dumps(result))

    def test_any_region_cleanup_failure_blocks_recovery_receipt(self):
        env = {"RELEASE_ALIYUN_CLEANUP_REGIONS": '["example-1","previous-2"]'}
        receipt = self.directory / "cleanup.json"
        with mock.patch.dict(os.environ, env), mock.patch("cloud_fixture.Cloud"):
            with mock.patch("cloud_fixture.janitor_region", side_effect=[RuntimeError("private-failure"), {"cleaned_fixtures": 1, "drill_budget_verified": True}]) as region:
                with self.assertRaisesRegex(RuntimeError, "remaining regions were attempted"):
                    fixture.janitor(self.directory / "recovery", "123", "1", receipt=receipt, require_recovery=True)
        self.assertEqual(region.call_count, 2)
        result = json.loads(receipt.read_text())
        self.assertEqual((result["result"], result["cleaned_fixtures"], result["leftover_resources"]), ("FAIL", 1, -1))
        self.assertNotIn("private", json.dumps(result))

    @unittest.skipUnless(sys.platform == "linux", "writer-termination proof requires Linux procfs")
    def test_real_sigkill_bypasses_finally_cleanup(self):
        path, cleanup = self.directory / "checkpoint.json", self.directory / "cleanup.txt"
        script = "import pathlib, sys, time\nsys.path.insert(0, sys.argv[4])\nfrom cloud_drill_diagnostics import WriterDiagnostic\ntry:\n p = pathlib.Path(sys.argv[1])\n WriterDiagnostic(p.parent).phase('await_interruption')\n p.with_suffix('.tmp').write_text(sys.argv[3])\n p.with_suffix('.tmp').replace(p)\n time.sleep(30)\nfinally:\n pathlib.Path(sys.argv[2]).write_text('cleanup')\n"
        process = subprocess.Popen([sys.executable, "-c", script, str(path), str(cleanup), json.dumps(self.expected), str(Path(drill.__file__).parent)], start_new_session=True)
        try:
            drill.interrupt_at_checkpoint(process, path, self.expected, timeout=10)
            self.assertEqual(process.returncode, -signal.SIGKILL)
            self.assertFalse(cleanup.exists())
            result = read_writer_diagnostic(self.directory, process.returncode)
            self.assertEqual((result["evidence"], result["last_phase"], result["failure_phase"]), ("RECORDED", "await_interruption", None))
            self.assertEqual(result["cleanup"]["result"], "NOT_STARTED")
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)

    def test_new_janitor_discovers_drill_without_prior_files_and_rejects_empty_proof(self):
        cloud = FakeCloud()
        cloud.rows["vpc"] = [{"VpcId": "synthetic-vpc", "VpcName": fixture.name_for(self.tags),
                              "Tags": {"Tag": [{"Key": k, "Value": v} for k, v in self.tags.items()]}}]
        receipt = self.directory / "cleanup.json"
        with mock.patch.dict(os.environ, {**CONFIG_ENV, "GITHUB_REPOSITORY": "example/plugin"}):
            with mock.patch("cloud_fixture.Cloud", return_value=cloud), mock.patch("cloud_fixture.stopped_run", return_value=True), mock.patch("time.sleep"), mock.patch("cloud_fixture.verify_drill_creation", return_value=True):
                result = fixture.janitor(self.directory / "fresh-runner", "123", "1", receipt=receipt, require_recovery=True)
                self.assertEqual((result["result"], result["cleaned_fixtures"], result["leftover_resources"]), ("PASS", 1, 0))
                self.assertEqual(cloud.rows["vpc"], [])
                with self.assertRaisesRegex(RuntimeError, "empty scan is not proof"):
                    fixture.janitor(self.directory / "another-runner", "123", "1", receipt=receipt, require_recovery=True)
        self.assertEqual(json.loads(receipt.read_text())["result"], "FAIL")

    def test_post_interruption_budget_failure_still_cleans_but_cannot_pass_the_drill(self):
        cloud = FakeCloud()
        cloud.rows["vpc"] = [{"VpcId": "synthetic-vpc", "VpcName": fixture.name_for(self.tags),
                              "Tags": {"Tag": [{"Key": k, "Value": v} for k, v in self.tags.items()]}}]
        receipt = self.directory / "cleanup.json"
        with mock.patch.dict(os.environ, {**CONFIG_ENV, "GITHUB_REPOSITORY": "example/plugin"}):
            with mock.patch("cloud_fixture.Cloud", return_value=cloud), mock.patch("cloud_fixture.stopped_run", return_value=True), mock.patch("time.sleep"):
                with mock.patch("cloud_fixture.verify_drill_creation", side_effect=ValueError("replacement worker")):
                    with self.assertRaisesRegex(RuntimeError, "budget is not proven"):
                        fixture.janitor(self.directory / "recovery", "123", "1", receipt=receipt, require_recovery=True)
        self.assertEqual(cloud.rows["vpc"], [])
        result = json.loads(receipt.read_text())
        self.assertEqual((result["result"], result["cleaned_fixtures"], result["leftover_resources"], result["drill_budget_verified"]), ("FAIL", 1, 0, False))


if __name__ == "__main__":
    unittest.main()
