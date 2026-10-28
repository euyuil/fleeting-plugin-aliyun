"""Reject stale, incomplete, modified and unsuccessfully cleaned release candidates."""
import copy
import json
import io
from pathlib import Path
import tempfile
import tarfile
import unittest
from unittest import mock

import release_candidate as candidate


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "candidate"
        self.receipts = Path(self.temporary.name) / "receipts"
        self.receipts.mkdir()
        self.sha = "a" * 40
        self.version = "1.2.3-rc.1"
        patcher = mock.patch("release_candidate.build_info")
        self.mock_build_info = patcher.start()
        self.addCleanup(patcher.stop)
        names = ["dist/SHA256SUMS", "dist/dependencies.json", "responsegate/plugin"]
        for arch in ("amd64", "arm64"):
            names.extend([f"dist/packages/linux/{arch}/plugin",
                          f"dist/fleeting-plugin-aliyun_{self.version}_linux_{arch}.tar.gz"])
        for name in names:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(name)
        for arch in ("amd64", "arm64"):
            binary = (self.root / f"dist/packages/linux/{arch}/plugin").read_bytes()
            with tarfile.open(self.root / f"dist/fleeting-plugin-aliyun_{self.version}_linux_{arch}.tar.gz", "w:gz") as archive:
                member = tarfile.TarInfo("fleeting-plugin-aliyun")
                member.size = len(binary)
                archive.addfile(member, io.BytesIO(binary))
        with mock.patch("subprocess.check_output", return_value=self.sha + "\n"):
            self.digest = candidate.create(self.root, self.sha, self.version)
        self.manifest = candidate.verify(self.root, self.digest, self.sha, self.version)
        for mode in candidate.MODES:
            binary = self.manifest["fault_binary" if mode == candidate.MODES[-1] else "normal_binary"]
            receipt = {"schema_version": 1, "result": "PASS", "cleanup_result": "PASS",
                       "candidate_sha": self.sha, "version": self.version,
                       "manifest_sha256": self.digest, "mode": mode, "run_id": "123", "run_attempt": "1",
                       "plugin_sha256": self.manifest["files"][binary],
                       "response_gate_instrumented": mode == candidate.MODES[-1], "leftover_resources": 0}
            candidate.write_json(self.receipts / (mode + ".json"), receipt)

    def gate(self):
        candidate.gate(self.root, self.receipts, self.digest, self.sha, self.version, "123", "1")

    def test_complete_same_candidate_passes(self):
        self.gate()

    def test_candidate_changes_are_rejected(self):
        for change in ("binary", "added", "missing", "manifest"):
            with self.subTest(change=change):
                path = self.root / {"binary": "dist/packages/linux/amd64/plugin", "added": "extra",
                                    "missing": "dist/SHA256SUMS", "manifest": "candidate.json"}[change]
                before = path.read_bytes() if path.exists() else None
                if change == "missing":
                    path.unlink()
                else:
                    path.write_text("modified")
                with self.assertRaises(ValueError):
                    self.gate()
                if before is None:
                    path.unlink()
                else:
                    path.write_bytes(before)

    def test_receipt_failures_and_identity_mismatches_block_publication(self):
        path = self.receipts / "cloud-smoke.json"
        original = json.loads(path.read_text())
        mutations = {"result": "NOT_RUN_MISSING_CONFIGURATION", "cleanup_result": "CLEANUP_FAILED",
                     "candidate_sha": "b" * 40, "version": "1.2.4", "run_id": "124", "run_attempt": "2",
                     "manifest_sha256": "0" * 64, "plugin_sha256": "0" * 64, "leftover_resources": 1,
                     "response_gate_instrumented": True}
        for key, value in mutations.items():
            with self.subTest(key=key):
                receipt = copy.deepcopy(original)
                receipt[key] = value
                candidate.write_json(path, receipt)
                with self.assertRaises(ValueError):
                    self.gate()
        candidate.write_json(path, original)

    def test_missing_or_duplicate_modes_are_rejected(self):
        path = self.receipts / "cloud-scale.json"
        before = path.read_bytes()
        path.unlink()
        with self.assertRaises(ValueError):
            self.gate()
        path.write_bytes(before)
        (self.receipts / "duplicate.json").write_bytes(before)
        with self.assertRaises(ValueError):
            self.gate()

    def test_archive_and_oci_binary_must_match_even_when_checksums_are_resealed(self):
        (self.root / "dist/packages/linux/amd64/plugin").write_text("different binary")
        with mock.patch("subprocess.check_output", return_value=self.sha + "\n"):
            with self.assertRaisesRegex(ValueError, "binaries differ"):
                candidate.create(self.root, self.sha, self.version)

    def test_other_candidate_version_and_run_are_rejected(self):
        for sha, version, run in (("b" * 40, self.version, "123"), (self.sha, "2.0.0", "123"),
                                  (self.sha, self.version, "999")):
            with self.assertRaises(ValueError):
                candidate.gate(self.root, self.receipts, self.digest, sha, version, run, "1")

    def test_expected_run_identity_is_mandatory_even_if_receipts_omit_it(self):
        for path in self.receipts.glob("*.json"):
            receipt = json.loads(path.read_text())
            receipt.pop("run_id")
            receipt.pop("run_attempt")
            candidate.write_json(path, receipt)
        for value in (None, "", "0", "-1", "abc", 123):
            for run, attempt in ((value, "1"), ("123", value)):
                with self.subTest(run=run, attempt=attempt):
                    with self.assertRaisesRegex(ValueError, "identifiers are required"):
                        candidate.gate(self.root, self.receipts, self.digest, self.sha, self.version, run, attempt)


class BuildIdentityTests(unittest.TestCase):
    def test_wrong_source_dirty_build_platform_and_instrumentation_are_refused(self):
        def info(command, **_kwargs):
            path = command[-1]
            settings = {"GOOS": "linux", "GOARCH": "arm64" if "arm64" in path else "amd64",
                        "vcs.revision": "a" * 40, "vcs.modified": "false"}
            if "responsegate" in path:
                settings["-tags"] = "responsegate"
            settings.update(changes)
            return json.dumps({"Path": "github.com/euyuil/fleeting-plugin-aliyun/cmd/fleeting-plugin-aliyun",
                               "Settings": [{"Key": k, "Value": v} for k, v in settings.items()]})
        changes = {}
        with mock.patch("subprocess.check_output", side_effect=info):
            candidate.build_info(Path("candidate"), "a" * 40)
            for changes in ({"vcs.revision": "b" * 40}, {"vcs.modified": "true"}, {"GOOS": "darwin"},
                            {"GOARCH": "arm64"}, {"-tags": "responsegate"}, {"-tags": "componenttest"}):
                with self.subTest(changes=changes):
                    with self.assertRaisesRegex(ValueError, "build tags differ"):
                        candidate.build_info(Path("candidate"), "a" * 40)


if __name__ == "__main__":
    unittest.main()
