"""Protect sealed publication bytes, post-publication scope and release completion."""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import job_acceptance as job
import public_release as public


def fixture():
    content = {"dependencies.json": b"[]"}
    for arch in ("amd64", "arm64"):
        for suffix in (f"1.2.3_linux_{arch}.tar.gz", f"1.2.3_{arch}.deb"):
            content["fleeting-plugin-aliyun_" + suffix] = suffix.encode()
    content["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n"
                                      for name, value in sorted(content.items()) if name.endswith((".deb", ".tar.gz"))).encode()
    files = {"dist/" + name: hashlib.sha256(value).hexdigest() for name, value in content.items()}
    for arch in ("amd64", "arm64"):
        files.update({f"dist/packages/linux/{arch}/{name}": hashlib.sha256((arch + name).encode()).hexdigest() for name in public.PAYLOAD})
    manifest = {"candidate_sha": "a" * 40, "version": "1.2.3", "normal_binary": "dist/packages/linux/amd64/plugin", "files": files}
    return manifest, content


class PublicReleaseTests(unittest.TestCase):
    def test_anonymous_downloads_match_sealed_bytes_and_complete_inventory(self):
        manifest, content = fixture()
        calls = []

        def read(url, timeout):
            calls.append(url)
            self.assertEqual(timeout, 60)
            if url.startswith("https://api.github.com/"):
                return io.BytesIO(json.dumps({"assets": [{"name": name} for name in content]}).encode())
            return io.BytesIO(content[url.rsplit("/", 1)[1]])

        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"GITHUB_RUN_ID": "10", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "example/plugin"}):
            result = public.download(manifest, "arm64", Path(temp) / "public", "example/plugin", "b" * 64, read)
        self.assertEqual(result["candidate_binding"], "sealed")
        self.assertEqual(len(result["files"]), 4)
        self.assertTrue(all(url.startswith(("https://github.com/example/plugin/", "https://api.github.com/repos/example/plugin/")) for url in calls))

    def test_public_missing_extra_or_modified_assets_never_fall_back(self):
        manifest, content = fixture()
        for fault in ("missing", "extra", "duplicate", "modified", "bad-checksums"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
                altered, expected = dict(content), copy.deepcopy(manifest)
                names = list(content)
                if fault == "missing":
                    names.pop()
                elif fault == "extra":
                    names.append("unexpected.deb")
                elif fault == "duplicate":
                    names[-1] = names[0]
                elif fault == "modified":
                    altered["fleeting-plugin-aliyun_1.2.3_amd64.deb"] = b"modified"
                else:
                    altered["SHA256SUMS"] += altered["SHA256SUMS"].splitlines(keepends=True)[0]
                    expected["files"]["dist/SHA256SUMS"] = hashlib.sha256(altered["SHA256SUMS"]).hexdigest()

                def read(url, timeout):
                    return io.BytesIO(json.dumps({"assets": [{"name": name} for name in names]}).encode()
                                      if url.startswith("https://api.github.com/") else altered[url.rsplit("/", 1)[1]])

                with self.assertRaises(ValueError):
                    public.download(expected, "amd64", Path(temp) / "public", "example/plugin", "b" * 64, read)

    def test_public_smoke_is_exact_and_never_full_acceptance(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"GITHUB_RUN_ID": "10", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "example/plugin"}):
            args = self.prepare(Path(temp))
            with patch.object(job, "verify", return_value=fixture()[0]):
                public_args = SimpleNamespace(**vars(args), phase="public", receipts=args.job_receipts, operation="public-smoke-gate")
                job.public_smoke_gate(public_args)
                summary = job.acceptance_summary(public_args)
                self.assertEqual((summary["scope"], summary["public_ready"], summary["full_suite"], summary["release_ready"]),
                                 ("public-smoke", True, False, False))
                with self.assertRaises(ValueError):
                    job.gate(public_args)
                with self.assertRaises(ValueError):
                    job.debug_gate(public_args)
                (args.job_receipts / "docker-autoscaler-smoke.json").unlink()
                with self.assertRaises(ValueError):
                    job.public_smoke_gate(public_args)
            for phase, cases in (("candidate", "instance/smoke,docker-autoscaler/smoke"), ("public", "all"), ("public", "instance/smoke")):
                with self.assertRaises(ValueError):
                    job.workflow_profile("public-smoke", phase, cases)

    def test_release_completion_requires_same_candidate_and_all_public_proof(self):
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {"GITHUB_RUN_ID": "10", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "example/plugin"}), \
                patch.object(public, "verify", return_value=fixture()[0]), patch.object(job, "verify", return_value=fixture()[0]):
            args = self.prepare(Path(temp))
            self.assertEqual(public.gate(args)["scope"], "release-completion")
            mutations = [
                (args.candidate_summary, "full_suite", False),
                (args.candidate_summary, "validation_profile", "debug"),
                (args.candidate_summary, "run_attempt", "2"),
                (args.job_receipts / "instance-smoke.json", "validation_profile", "debug"),
                (args.job_receipts / "instance-smoke.json", "cleanup_result", "FAIL"),
                (args.job_receipts / "instance-smoke.json", "gitlab_config_sha256", "9" * 64),
                (args.platform_receipts / "download-receipt-ubuntu-24.04-arm-arm64.json", "candidate_binding", "not-provided"),
                (args.platform_receipts / "install-receipt-debian-ubuntu-26.04-amd64.json", "package_sha256", "9" * 64),
                (args.platform_receipts / "install-receipt-ubuntu-26.04-arm-arm64.json", "payload_sha256", {"plugin": "9" * 64}),
                (args.platform_receipts / "install-receipt-ubuntu-26.04-amd64.json", "pinned_reference", "registry.invalid/plugin:1.2.3"),
                (args.platform_receipts / "install-receipt-ubuntu-26.04-amd64.json", "run_attempt", "2"),
                (args.platform_receipts / "install-receipt-ubuntu-26.04-amd64.json", "runner_image", "ubuntu-24.04"),
                (args.platform_receipts / "install-receipt-ubuntu-26.04-amd64.json", "reference", "ghcr.io/other/plugin:1.2.3"),
                (args.platform_receipts / "install-receipt-debian-ubuntu-26.04-amd64.json", "manifest_sha256", "9" * 64),
            ]
            for path, key, value in mutations:
                before = path.read_text()
                receipt = json.loads(before)
                receipt[key] = value
                path.write_text(json.dumps(receipt))
                with self.subTest(path=path.name, key=key), self.assertRaises(ValueError):
                    public.gate(args)
                path.write_text(before)
            extra = args.platform_receipts / "unexpected.json"
            extra.write_text("{}")
            with self.assertRaises(ValueError):
                public.gate(args)
            extra.unlink()
            (args.platform_receipts / "download-receipt-ubuntu-24.04-amd64.json").unlink()
            with self.assertRaises(FileNotFoundError):
                public.gate(args)

    def prepare(self, root):
        manifest, _ = fixture()
        platform, jobs = root / "platform", root / "jobs"
        platform.mkdir()
        jobs.mkdir()
        identity = {"candidate_sha": "a" * 40, "version": "1.2.3", "manifest_sha256": "b" * 64, "run_id": "10", "run_attempt": "1"}
        configs = {"fixture_config_sha256": "1" * 64, "gitlab_config_sha256": "2" * 64}
        candidate = {**identity, **configs, "schema_version": 2, "result": "PASS", "phase": "candidate", "validation_profile": "full",
                     "scope": "full-suite", "receipt_schema_version": 3, "recovery_policies": {scenario: job.recovery_policy(scenario) for _, scenario in job.selected_cases()},
                     "full_suite": True, "release_ready": True, "selected_cases": ["/".join(c) for c in job.selected_cases()], "omitted_cases": []}
        summary = root / "candidate.json"
        summary.write_text(json.dumps(candidate))
        for executor in job.EXECUTORS:
            receipt = {**identity, **configs, "schema_version": 3, "validation_profile": "public-smoke", "recovery_policy": "standard-v1",
                       "job_scenario": "smoke", "job_count": 1, "result": "PASS", "cleanup_result": "PASS", "leftover_resources": 0,
                       "phase": "public", "executor": executor, "manager_mode": "systemd", "distribution_source": "anonymous-release",
                       "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                       "package_sha256": manifest["files"]["dist/fleeting-plugin-aliyun_1.2.3_amd64.deb"]}
            (jobs / (executor + "-smoke.json")).write_text(json.dumps(receipt))
        for runner, arch in public.PLATFORMS:
            files = {name: value for name, value in public.assets(manifest).items()
                     if name in ("SHA256SUMS", "dependencies.json") or name.endswith((f"_linux_{arch}.tar.gz", f"_{arch}.deb"))}
            payload = {name: manifest["files"][f"dist/packages/linux/{arch}/{name}"] for name in public.PAYLOAD}
            downloaded = {**identity, "schema_version": 1, "result": "PASS", "scope": "public-distribution", "candidate_binding": "sealed",
                          "architecture": arch, "anonymous_download": True, "files": files}
            oci = {**identity, "runner_image": runner, "reference": "ghcr.io/example/plugin:1.2.3", "version": "1.2.3", "revision": "a" * 40, "os": "linux", "architecture": arch, "binary_sha256": payload["plugin"],
                   "anonymous_manifest": True, "release_bytes_match": True, "plugin_protocol": True, "empty_configuration_rejected": True,
                   "payload_sha256": payload, "manifest_digest": "sha256:" + "c" * 64, "platform_manifest_digest": "sha256:" + "d" * 64,
                   "pinned_reference": "ghcr.io/example/plugin@sha256:" + "c" * 64}
            package = next(name for name in files if name.endswith(".deb"))
            debian = {**identity, "version": "1.2.3", "architecture": arch, "runner_image": runner, "package": package, "package_sha256": files[package],
                      "anonymous_download": True, "native_binary_match": True, "install_smoke": True, "lifecycle": False}
            for prefix, value in (("download-receipt-", downloaded), ("install-receipt-", oci), ("install-receipt-debian-", debian)):
                (platform / (prefix + runner + "-" + arch + ".json")).write_text(json.dumps(value))
        return SimpleNamespace(root=root, digest="b" * 64, sha="a" * 40, version="1.2.3", platform_receipts=platform,
                               job_receipts=jobs, candidate_summary=summary)


if __name__ == "__main__":
    unittest.main()
