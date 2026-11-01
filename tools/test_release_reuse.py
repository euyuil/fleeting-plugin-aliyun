"""Protect the existing full gate when publication reuses an earlier run."""
import copy
import io
import json
import os
from pathlib import Path
import re
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import job_acceptance as job
import release_reuse as reuse
import test_public_release as release_tests
from test_public_release import fixture


class ReleaseReuseTests(unittest.TestCase):
    def test_reuse_skips_new_candidate_cases_and_keeps_every_public_completion_gate(self):
        text = Path(".github/workflows/release.yml").read_text().partition("\njobs:\n")[2]
        jobs = dict(re.findall(r"^  ([a-z_]+):\n(.*?)(?=^  [a-z_]+:|\Z)", text, re.M | re.S))
        self.assertIn("if: inputs.accepted_run_id == ''", jobs["acceptance"])
        self.assertIn("if: inputs.accepted_run_id != ''", jobs["reuse"])
        self.assertIn('success/skipped || test "$ACCEPTANCE_RESULT/$REUSE_RESULT" = skipped/success', jobs["candidate"])
        self.assertIn("needs: [candidate, publish, verify, public_jobs]", jobs["completion"])
        for result in ("CANDIDATE_RESULT", "PUBLISH_RESULT", "VERIFY_RESULT", "PUBLIC_RESULT"):
            self.assertIn('test "$' + result + '" = success', jobs["completion"])
        self.assertIn('test "$PUBLIC_BOUND" = true && test "$PUBLIC_READY" = true', jobs["completion"])
        self.assertIn("profile: public-smoke", jobs["public_jobs"])
        self.assertIn("cases: instance/smoke,docker-autoscaler/smoke", jobs["public_jobs"])

    def test_runtime_resolution_reads_the_accepted_templates_without_enabling_writes(self):
        configured = {"url": "https://gitlab.com", "project_id": "123", "api_user_id": "789", "fixture_ref": "main", "fixture_sha": "a" * 40,
                      "runners": {"instance": {"runner_id": "456", "runner_tag": "instance"},
                                  "docker-autoscaler": {"runner_id": "457", "runner_tag": "docker"}}}
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, RELEASE_GITLAB_API_TOKEN="synthetic-token"), \
                patch.object(reuse, "decode", return_value=configured), patch.object(reuse.subprocess, "check_output", return_value="b" * 40 + "\n"), \
                patch.object(reuse, "prepare", return_value={**configured, "fixture_ref": "acceptance/scoped", "fixture_sha": "c" * 40}) as prepare:
            reuse.runtime_configuration(Path(temp), "b" * 40, "9", "1")
            prepare.assert_called_once_with(configured, "b" * 40, "synthetic-token", run_id="9", attempt="1", read_only=True)

    def metadata(self):
        run = {"id": 9, "run_attempt": 1, "status": "completed", "conclusion": "success", "head_sha": "a" * 40,
               "event": "workflow_dispatch", "path": ".github/workflows/cloud-acceptance.yml", "head_branch": "main",
               "repository": {"full_name": "example/plugin"}, "head_repository": {"full_name": "example/plugin"}}
        names = ["cloud-candidate-9-1", "job-summary-9-1-candidate"]
        names += [f"job-receipt-9-1-candidate-{e}-{s}" for e, s in job.selected_cases()]
        artifacts = [{"id": i + 1, "name": name, "expired": False, "digest": "sha256:" + "b" * 64,
                      "workflow_run": {"id": 9, "head_sha": "a" * 40}} for i, name in enumerate(names)]
        jobs = [{"name": name, "status": "completed", "conclusion": "success", "run_id": 9, "run_attempt": 1, "head_sha": "a" * 40}
                for name in ["gate", *(f"scenario ({mode})" for mode in reuse.MODES),
                             *(f"platforms / install ({runner}, {arch})" for runner, arch in reuse.PLATFORMS)]]
        return {"repos/example/plugin/actions/runs/9": run,
                "repos/example/plugin/actions/runs/9/attempts/1": copy.deepcopy(run),
                "repos/example/plugin/actions/runs/9/attempts/1/jobs?per_page=100":
                    {"total_count": len(jobs), "jobs": jobs},
                "repos/example/plugin/actions/runs/9/artifacts?per_page=100": {"total_count": len(artifacts), "artifacts": artifacts}}

    def test_only_complete_same_repository_main_attempt_and_exact_artifacts_resolve(self):
        data = self.metadata()
        with patch.dict(os.environ, GITHUB_REPOSITORY="example/plugin"), patch.object(reuse.subprocess, "run") as ancestor:
            result = reuse.resolve("9", "1", "1.2.3", data.__getitem__)
        ancestor.assert_called_once_with(["git", "merge-base", "--is-ancestor", "a" * 40, "HEAD"], check=True, capture_output=True)
        self.assertEqual((result["candidate_sha"], result["original_artifact_id"], result["summary_artifact_id"]), ("a" * 40, 1, 2))
        self.assertEqual(len(result["receipt_artifact_ids"].split(",")), 18)
        for fault in ("attempt", "source", "repository", "workflow", "failed", "skipped", "incomplete", "expired", "missing", "duplicate",
                      "missing-plugin", "missing-platform", "foreign-job", "job-attempt", "job-source"):
            altered = self.metadata()
            latest = altered["repos/example/plugin/actions/runs/9"]
            jobs = altered["repos/example/plugin/actions/runs/9/attempts/1/jobs?per_page=100"]
            inventory = altered["repos/example/plugin/actions/runs/9/artifacts?per_page=100"]
            if fault == "attempt":
                latest["run_attempt"] = 2
            elif fault == "source":
                latest["head_sha"] = "c" * 40
            elif fault == "repository":
                latest["head_repository"]["full_name"] = "other/plugin"
            elif fault == "workflow":
                latest["path"] = ".github/workflows/ci.yml"
            elif fault == "failed":
                latest["conclusion"] = "failure"
            elif fault == "skipped":
                jobs["jobs"][0]["conclusion"] = "skipped"
            elif fault == "incomplete":
                jobs["total_count"] += 1
            elif fault == "expired":
                inventory["artifacts"][0]["expired"] = True
            elif fault == "missing":
                inventory["artifacts"].pop()
                inventory["total_count"] -= 1
            elif fault in ("missing-plugin", "missing-platform"):
                prefix = "scenario (" if fault == "missing-plugin" else "platforms / install ("
                jobs["jobs"].remove(next(j for j in jobs["jobs"] if j["name"].startswith(prefix)))
                jobs["total_count"] -= 1
            elif fault == "foreign-job":
                jobs["jobs"][0]["run_id"] = 8
            elif fault == "job-attempt":
                jobs["jobs"][0]["run_attempt"] = 2
            elif fault == "job-source":
                jobs["jobs"][0]["head_sha"] = "d" * 40
            else:
                inventory["artifacts"].append(copy.deepcopy(inventory["artifacts"][0]))
                inventory["total_count"] += 1
            with self.subTest(fault=fault), patch.dict(os.environ, GITHUB_REPOSITORY="example/plugin"), patch.object(reuse.subprocess, "run"), self.assertRaises(ValueError):
                reuse.resolve("9", "1", "1.2.3", altered.__getitem__)

    def accepted(self, root):
        args = release_tests.PublicReleaseTests().prepare(root)
        candidate = json.loads(args.candidate_summary.read_text())
        candidate.update(run_id="9")
        args.candidate_summary.write_text(json.dumps(candidate))
        receipts = root / "accepted"
        receipts.mkdir()
        manifest, _ = fixture()
        for e, case in job.selected_cases():
            row = {**candidate, "schema_version": 3, "job_scenario": case, "job_count": len(job.job_cases(case)),
                   "recovery_policy": job.recovery_policy(case), "executor": e, "manager_mode": "systemd",
                   "distribution_source": "sealed-candidate", "cleanup_result": "PASS", "leftover_resources": 0,
                   "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                   "package_sha256": manifest["files"]["dist/fleeting-plugin-aliyun_1.2.3_amd64.deb"], **job.upgrade_receipt(case)}
            (receipts / f"{e}-{case}.json").write_text(json.dumps(row))
        archive = root / "candidate.tar"
        with tarfile.open(archive, "w") as tar:
            info = tarfile.TarInfo("marker")
            info.size = 8
            tar.addfile(info, io.BytesIO(b"accepted"))
        resolution = {"schema_version": 1, "accepted_run_id": "9", "accepted_run_attempt": "1", "candidate_sha": "a" * 40,
                      "version": "1.2.3", "original_artifact_id": 1, "original_artifact_digest": "sha256:" + "b" * 64}
        return args, candidate, receipts, archive, resolution

    def test_tar_is_copied_unchanged_only_after_full_summary_receipts_and_configuration_pass(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, GITHUB_RUN_ID="10", GITHUB_RUN_ATTEMPT="1"), \
                patch.object(reuse, "verify", return_value=fixture()[0]), patch.object(reuse, "runtime_configuration", return_value=("4" * 64, "2" * 64)), \
                patch.object(reuse, "decode", return_value={}), patch.object(reuse, "config_digest", return_value="1" * 64):
            root = Path(temp)
            args, candidate, receipts, archive, resolution = self.accepted(root)
            result = reuse.accepted_payload(resolution, root / "unpacked", args.candidate_summary, receipts, root / "source", archive, root / "payload")
            self.assertEqual(archive.read_bytes(), (root / "payload/candidate.tar").read_bytes())
            self.assertEqual((result["accepted_run_id"], result["publication_run_id"], len(result["receipt_sha256"])), ("9", "10", 18))
            for field, value in (("validation_profile", "debug"), ("run_attempt", "2"), ("omitted_cases", ["instance/failure"])):
                changed = {**candidate, field: value}
                args.candidate_summary.write_text(json.dumps(changed))
                with self.subTest(field=field), self.assertRaises(ValueError):
                    reuse.accepted_payload(resolution, root / "must-not-extract", args.candidate_summary, receipts, root / "source", archive, root / "must-not-publish")
                self.assertFalse((root / "must-not-publish").exists())

    def test_read_only_config_proof_binds_both_runs_and_rejects_changed_configuration(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, GITHUB_RUN_ID="10", GITHUB_RUN_ATTEMPT="1"), \
                patch.object(reuse, "decode", return_value={}), patch.object(reuse, "config_digest", return_value="1" * 64):
            root = Path(temp)
            args, candidate, _, _, resolution = self.accepted(root)
            proof = {**resolution, "result": "PASS", "scope": "accepted-candidate-reuse", "manifest_sha256": "b" * 64,
                     "fixture_config_sha256": "1" * 64, "configured_gitlab_config_sha256": "4" * 64,
                     "accepted_gitlab_config_sha256": "2" * 64, "publication_run_id": "10", "publication_run_attempt": "1"}
            for p in args.job_receipts.glob("*.json"):
                x = json.loads(p.read_text())
                x["gitlab_config_sha256"] = "3" * 64
                p.write_text(json.dumps(x))
            with patch.object(reuse, "runtime_configuration", side_effect=[("4" * 64, "2" * 64), ("4" * 64, "3" * 64)]):
                result = reuse.configuration_proof(proof, args.candidate_summary, args.job_receipts, root / "source")
            self.assertEqual((result["accepted_gitlab_config_sha256"], result["public_gitlab_config_sha256"]), ("2" * 64, "3" * 64))
            for values in ([("5" * 64, "2" * 64), ("4" * 64, "3" * 64)], [("4" * 64, "9" * 64), ("4" * 64, "3" * 64)], [("4" * 64, "2" * 64), ("4" * 64, "9" * 64)]):
                with self.subTest(values=values), patch.object(reuse, "runtime_configuration", side_effect=values), self.assertRaises(ValueError):
                    reuse.configuration_proof(proof, args.candidate_summary, args.job_receipts, root / "source")
            with self.assertRaises(ValueError):
                reuse.configuration_proof({**proof, "publication_run_attempt": "2"}, args.candidate_summary, args.job_receipts, root / "source")


if __name__ == "__main__":
    unittest.main()
