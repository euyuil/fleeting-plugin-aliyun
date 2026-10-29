import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import job_acceptance as job
from cloud_fixture import JOB_MODES, labels, name_for


def inputs():
    fixture = {"region": "test-region-1", "zone": "test-region-1a", "instance_type": "ecs.example.large", "image": "official-ubuntu-image",
               "disk_category": "cloud_essd", "disk_size_gib": 20, "disk_performance_level": "PL0", "internet_max_bandwidth_out": 1,
               "spot_duration": 1, "vpc_cidr": "10.45.0.0/16", "vswitch_cidr": "10.45.1.0/24", "swap_mib": 1024}
    config = {"fixture": fixture, "job_image": "docker.io/library/example@sha256:" + "a" * 64}
    gitlab = {"url": "https://gitlab.com", "project_id": "1", "runner_id": "2", "fixture_ref": "main",
              "fixture_sha": "b" * 40, "runner_tag": "acceptance"}
    env = {"GITHUB_REF": "refs/heads/main", "RELEASE_ALIYUN_ACCOUNT_ID": "1234567890123456",
           "RELEASE_ALIYUN_ROLE_ARN": "acs:ram::1234567890123456:role/acceptance",
           "RELEASE_ALIYUN_OIDC_PROVIDER_ARN": "acs:ram::1234567890123456:oidc-provider/github",
           "RELEASE_CLOUD_ACCEPTANCE_ENABLED": "true", "RELEASE_CLOUD_CLEANUP_ENABLED": "true", "RELEASE_JOB_ACCEPTANCE_ENABLED": "true",
           "RELEASE_ALIYUN_CLEANUP_REGIONS": '["test-region-1"]', "RELEASE_ALIYUN_JOB_FIXTURE_CONFIG": json.dumps(config),
           "RELEASE_GITLAB_CONFIG": json.dumps(gitlab), "RELEASE_GITLAB_API_TOKEN": "synthetic-api", "RELEASE_GITLAB_RUNNER_TOKEN": "synthetic-runner",
           "GITHUB_REPOSITORY": "example/project", "GITHUB_RUN_ID": "10", "GITHUB_RUN_ATTEMPT": "1"}
    return config, gitlab, env


def manifest():
    return {"candidate_sha": "c" * 40, "version": "0.1.0-dev", "normal_binary": "dist/packages/linux/amd64/plugin",
            "files": {"dist/packages/linux/amd64/plugin": "d" * 64, "dist/fleeting-plugin-aliyun_0.1.0~dev_amd64.deb": "e" * 64,
                      "dist/SHA256SUMS": "f" * 64}}


def receipt(executor="instance", phase="candidate", scenario="smoke"):
    m = manifest()
    return {"schema_version": 2, "job_scenario": scenario, "job_count": 1 if scenario == "smoke" else 2, "result": "PASS", "cleanup_result": "PASS", "leftover_resources": 0,
            "candidate_sha": m["candidate_sha"], "version": m["version"], "manifest_sha256": "a" * 64,
            "phase": phase, "executor": executor, "manager_mode": "systemd", "run_id": "10", "run_attempt": "1",
            "distribution_source": "sealed-candidate" if phase == "candidate" else "anonymous-release",
            "plugin_sha256": "d" * 64, "package_sha256": "e" * 64,
            "fixture_config_sha256": "1" * 64, "gitlab_config_sha256": "2" * 64}


class JobAcceptanceTests(unittest.TestCase):
    def test_worker_rejects_wrong_os_before_installing_anything(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            observed = root / "install-called"
            for name, content in {"uname": "#!/bin/sh\nprintf x86_64", "install": '#!/bin/sh\ntouch "$TEST_CALL_LOG"\nexit 97'}.items():
                path = root / name
                path.write_text(content)
                path.chmod(0o700)
            release = root / "os-release"
            script = Path("infra/bootstrap-job-worker.sh").read_text().replace("/etc/os-release", str(release))
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "TEST_CALL_LOG": str(observed),
                   "PACKAGE_SNAPSHOT": "20261027T000000Z", "INSTALL_DOCKER": "false"}
            for system, version, codename in (("debian", "26.04", "resolute"), ("ubuntu", "24.04", "noble"), ("ubuntu", "26.04", "noble")):
                with self.subTest(system=system, version=version, codename=codename):
                    release.write_text(f"ID={system}\nVERSION_ID={version}\nVERSION_CODENAME={codename}\n")
                    result = subprocess.run(["sh"], input=script, text=True, env=env, capture_output=True)
                    self.assertEqual(result.returncode, 1)
                    self.assertFalse(observed.exists())
            release.write_text("ID=ubuntu\nVERSION_ID=26.04\nVERSION_CODENAME=resolute\n")
            result = subprocess.run(["sh"], input=script, text=True, env=env, capture_output=True)
            self.assertEqual(result.returncode, 97)
            self.assertTrue(observed.exists())

    def test_cold_bootstrap_uses_locked_runner_and_repository_scripts(self):
        lock = json.loads(Path("upstream.lock.json").read_text())
        for executor in job.EXECUTORS:
            script = job.worker_script(executor)
            subprocess.run(["sh", "-n"], input=script, text=True, check=True)
            self.assertIn(lock["runner"]["sha256"]["linux-amd64"], script)
            self.assertIn(lock["job_worker"]["ubuntu_snapshot"], script)
            self.assertTrue(script.endswith(Path("infra/bootstrap-linux.sh").read_text()))
            self.assertIn("export INSTALL_DOCKER=" + ("true" if executor == "docker-autoscaler" else "false"), script)

    def test_separate_configuration_and_opt_in(self):
        config, gitlab, env = inputs()
        self.assertEqual(job.configuration(env), (config, gitlab))
        env["RELEASE_ALIYUN_FIXTURE_CONFIG"] = "unrelated plugin-only configuration"
        self.assertEqual(job.configuration(env), (config, gitlab))
        for flag in ("RELEASE_CLOUD_ACCEPTANCE_ENABLED", "RELEASE_CLOUD_CLEANUP_ENABLED", "RELEASE_JOB_ACCEPTANCE_ENABLED"):
            with self.subTest(flag=flag), self.assertRaises(ValueError):
                job.configuration({**env, flag: "false"})
        self.assertEqual(job.configuration({**env, "RELEASE_JOB_ACCEPTANCE_ENABLED": "false"}, read_only=True), (config, gitlab))
        for name in ("RELEASE_ALIYUN_JOB_FIXTURE_CONFIG", "RELEASE_GITLAB_CONFIG", "RELEASE_GITLAB_API_TOKEN", "RELEASE_GITLAB_RUNNER_TOKEN"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                job.configuration({**env, name: ""})
        config["job_image"] = "docker.io/library/example:latest"
        with self.assertRaises(ValueError):
            job.configuration({**env, "RELEASE_ALIYUN_JOB_FIXTURE_CONFIG": json.dumps(config)})

    def test_job_modes_have_distinct_recoverable_fixture_names(self):
        tags = [labels("example/project", "a" * 40, "123456789012345678", "1", mode, "2030-01-01T00:00:00Z") for mode in JOB_MODES]
        names = {name_for(value) for value in tags}
        self.assertEqual(len(names), 16)
        self.assertTrue(all(len(name) <= 64 for name in names))
        self.assertEqual(names, {name_for(json.loads(json.dumps(value))) for value in tags})

    def test_receipts_reject_stale_foreign_or_failed_evidence(self):
        good = receipt()
        job.check_receipt(good, manifest(), "a" * 64, "candidate", "instance", "10", "1")
        changes = {"result": "FAIL", "cleanup_result": "FAIL", "leftover_resources": False, "candidate_sha": "f" * 40,
                   "version": "0.1.0", "manifest_sha256": "f" * 64, "phase": "public", "executor": "docker-autoscaler",
                   "manager_mode": "process", "run_id": "11", "run_attempt": "2", "distribution_source": "anonymous-release",
                   "plugin_sha256": "f" * 64, "package_sha256": "f" * 64, "fixture_config_sha256": "", "gitlab_config_sha256": None}
        for key, value in changes.items():
            with self.subTest(key=key), self.assertRaises(ValueError):
                job.check_receipt({**good, key: value}, manifest(), "a" * 64, "candidate", "instance", "10", "1")

    def test_private_result_requires_actual_job_and_normal_spot_release(self):
        _, gitlab, _ = inputs()
        lock = {"runner": {"version": "1.2.3", "sha256": {"linux-amd64": "f" * 64}}}
        result = {"result": "PASS", "cleanup_result": "PASS", "normal_release": True, "mode": "smoke", "executor": "instance",
                  "manager_mode": "systemd", "plugin_commit": "c" * 40, "plugin_version": "0.1.0-dev", "plugin_sha256": "d" * 64,
                  "runner_sha256": "f" * 64, "runner_version": "1.2.3", "gitlab_fixture_sha": "b" * 40, "gitlab_project_id": "1", "gitlab_runner_id": 2,
                  "github_run_id": "10", "github_run_attempt": "1", "instance_limit": 1, "creation_budget": 1, "evidence_incomplete": False,
                  "gitlab_pipeline_id": 3, "gitlab_job_id": 4, "leftover_resources": None, "manager_pid": 123, "job_scenario": "smoke",
                  "jobs": [{"scenario": "smoke", "status": "success", "failure_reason": "", "manager_pid": 123, "normal_release": True,
                            "pipeline_id": 3, "job_id": 4, "nonce": "1" * 48, "instance_id": "i-worker1", "cancel_requested": False}],
                  "instances": [{"instance_id": "i-worker1", "spot_verified": True, "deletion_verified": True, "inventory_complete": True}]}
        job.check_result(result, manifest(), gitlab, "instance", lock, "10", "1")
        for key, value in {"normal_release": False, "gitlab_fixture_sha": "a" * 40, "gitlab_runner_id": 3, "gitlab_pipeline_id": 0, "gitlab_job_id": True,
                           "instances": [], "leftover_resources": ["synthetic-instance"], "errors": ["failure"], "runner_sha256": "a" * 64,
                           "github_run_id": "11", "github_run_attempt": "2", "jobs": [], "manager_pid": 0}.items():
            with self.subTest(key=key), self.assertRaises(ValueError):
                job.check_result({**result, key: value}, manifest(), gitlab, "instance", lock, "10", "1")

        for scenario, status, reason in (("failure", "failed", "script_failure"), ("cancel", "canceled", "job_canceled"),
                                         ("timeout", "failed", "job_execution_timeout")):
            recovery = copy.deepcopy(result)
            recovery.update(job_scenario=scenario, creation_budget=2)
            first = {**recovery["jobs"][0], "scenario": scenario, "status": status, "failure_reason": reason,
                     "pipeline_id": 1, "job_id": 2, "instance_id": "i-worker0", "nonce": "0" * 48,
                     "cancel_requested": scenario == "cancel", "checkpoint": True}
            recovery["jobs"].insert(0, first)
            recovery["instances"].insert(0, {**recovery["instances"][0], "instance_id": "i-worker0"})
            job.check_result(recovery, manifest(), gitlab, "instance", lock, "10", "1", scenario)
            for key, value in (("manager_pid", 124), ("normal_release", False), ("checkpoint", False),
                               ("job_id", 4), ("pipeline_id", 3), ("instance_id", "i-worker1"), ("nonce", "1" * 48), ("status", "success")):
                broken = copy.deepcopy(recovery)
                broken["jobs"][0][key] = value
                with self.subTest(scenario=scenario, key=key), self.assertRaises(ValueError):
                    job.check_result(broken, manifest(), gitlab, "instance", lock, "10", "1", scenario)

    def test_gate_requires_every_scenario_and_same_fixture(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"GITHUB_RUN_ID": "10", "GITHUB_RUN_ATTEMPT": "1"}), patch.object(job, "verify", return_value=manifest()):
            root = Path(temp)
            args = SimpleNamespace(root=root, digest="a" * 64, sha="c" * 40, version="0.1.0-dev", phase="candidate", receipts=root)
            for executor in job.EXECUTORS:
                for scenario in job.JOB_SCENARIOS:
                    with self.assertRaises(ValueError):
                        job.gate(args)
                    (root / f"{executor}-{scenario}.json").write_text(json.dumps(receipt(executor, scenario=scenario)))
            job.gate(args)
            different = receipt("docker-autoscaler", scenario="timeout")
            different["gitlab_config_sha256"] = "3" * 64
            (root / "docker-autoscaler-timeout.json").write_text(json.dumps(different))
            with self.assertRaises(ValueError):
                job.gate(args)

    def test_recovery_receipt_cannot_reuse_smoke_or_wrong_case(self):
        for scenario in ("failure", "cancel", "timeout"):
            good = receipt(scenario=scenario)
            job.check_receipt(good, manifest(), "a" * 64, "candidate", "instance", "10", "1", scenario)
            for key, value in (("schema_version", 1), ("job_count", 1), ("job_count", True), ("job_scenario", "smoke")):
                with self.subTest(scenario=scenario, key=key), self.assertRaises(ValueError):
                    job.check_receipt({**good, key: value}, manifest(), "a" * 64, "candidate", "instance", "10", "1", scenario)

    def test_driver_failure_still_tears_down_and_emits_only_sanitized_receipt(self):
        config, gitlab, env = inputs()
        fixture = Mock()
        fixture.state = {"ids": {}}
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, env), \
                patch.object(job, "require_linux"), patch.object(job, "verify", return_value=manifest()), \
                patch.object(job, "install_package"), patch.object(job, "Cloud"), patch.object(job, "Fixture", return_value=fixture), \
                patch.object(job.subprocess, "check_output", return_value="192.0.2.1"), \
                patch.object(job, "driver", side_effect=ValueError("private diagnostic")):
            root = Path(temp)
            args = SimpleNamespace(root=root, digest="a" * 64, sha="c" * 40, version="0.1.0-dev", phase="candidate", executor="instance", scenario="smoke",
                                   directory=root / "private", receipt=root / "receipt.json", driver=root / "driver")
            with self.assertRaises(ValueError):
                job.run(args)
            fixture.cleanup.assert_called_once()
            value = json.loads(args.receipt.read_text())
            self.assertEqual(value["result"], "FAIL")
            self.assertEqual(value["cleanup_result"], "PASS")
            self.assertEqual(value["fixture_config_sha256"], job.config_digest(config))
            self.assertEqual(value["gitlab_config_sha256"], job.config_digest(gitlab))
            self.assertNotIn("private diagnostic", args.receipt.read_text())
            self.assertFalse({"project_id", "runner_id", "fixture_sha", "instances", "errors"} & value.keys())

    def test_unconfirmed_writer_exit_does_not_clear_fixture_barrier(self):
        config, gitlab, env = inputs()
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, env), \
                patch.object(job.subprocess, "Popen") as popen, patch.object(job, "stop_process_group", side_effect=ValueError("still alive")):
            root = Path(temp)
            fixture = Mock(directory=root, tags={"fleeting-project": "example/project"}, state={"ids": {"group": "synthetic-group"}})
            popen.return_value.pid = 123
            popen.return_value.wait.return_value = 1
            with self.assertRaises(ValueError):
                job.driver(fixture, root, manifest(), config, gitlab, "instance", "192.0.2.1/32", root / "driver")
            self.assertEqual(fixture.state["driver_group"], 123)

    def test_public_download_never_falls_back_to_candidate_bytes(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "example/project"}), \
                patch.object(job.os.path, "lexists", return_value=False), patch.object(job.urllib.request, "urlopen", return_value=io.BytesIO(b"different public bytes")), \
                patch.object(job.subprocess, "run") as commands:
            root = Path(temp)
            m = copy.deepcopy(manifest())
            m["files"][job.package_path(m)] = hashlib.sha256(b"accepted bytes").hexdigest()
            with self.assertRaises(ValueError):
                job.install_package(root, m, "public", root)
            commands.assert_not_called()


if __name__ == "__main__":
    unittest.main()
