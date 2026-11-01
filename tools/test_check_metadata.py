"""Protect the consistency gate against plausible independent metadata edits."""
import copy
import json
from pathlib import Path
import shutil
import tempfile
import unittest

import check_metadata as metadata

SOURCE = Path(__file__).resolve().parents[1]


class MetadataTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for name in ("go.mod", "upstream.lock.json", "tools/requirements-dev.txt", "acceptance/status.json"):
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(SOURCE / name, target)
        shutil.copytree(SOURCE / ".github/workflows", self.root / ".github/workflows")

    def test_current_metadata_is_consistent(self):
        metadata.check(self.root)

    def test_action_drift_and_unpinned_actions_are_rejected(self):
        path = self.root / ".github/workflows/ci.yml"
        original = path.read_text()
        pin = json.loads((self.root / "upstream.lock.json").read_text())["actions"]["checkout"]
        for replacement in ("0" * 40, "main"):
            path.write_text(original.replace(pin, replacement))
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                metadata.check(self.root)

    def test_module_drift_and_duplicate_go_version_are_rejected(self):
        path = self.root / "upstream.lock.json"
        original = json.loads(path.read_text())
        for mutate in (lambda value: value.update(go="1.0.0"),
                       lambda value: value.update(credentials="github.com/aliyun/credentials-go@v0.0.1")):
            changed = copy.deepcopy(original)
            mutate(changed)
            path.write_text(json.dumps(changed))
            with self.assertRaises(ValueError):
                metadata.check(self.root)

    def test_receipt_identity_and_partial_evidence_requirements(self):
        status = json.loads((self.root / "acceptance/status.json").read_text())
        changed = copy.deepcopy(status)
        changed["published_release"]["installation_receipts"][0]["revision"] = "0" * 40
        with self.assertRaisesRegex(ValueError, "identity"):
            metadata.check_status(changed)
        changed = copy.deepcopy(status)
        row = next(c for c in changed["capabilities"] if c["live_validation"] == "partial_pass")
        del row["gap"]
        with self.assertRaisesRegex(ValueError, "scope and gap"):
            metadata.check_status(changed)

    def test_broken_local_link_is_rejected(self):
        (self.root / "README.md").write_text("[missing](docs/missing.md)\n")
        with self.assertRaisesRegex(ValueError, "broken local"):
            metadata.check(self.root)

    def test_unverified_publication_cannot_claim_installation_pass(self):
        status = json.loads((self.root / "acceptance/status.json").read_text())
        release = status["published_release"]
        release["installation_receipts"] = []
        with self.assertRaisesRegex(ValueError, "receipt"):
            metadata.check_status(status)
        for arch in ("amd64", "arm64"):
            release["linux_" + arch + "_runner_installation"] = "not_run"
        metadata.check_status(status)

    def test_debian_pass_requires_matching_receipts(self):
        original = self.debian_lifecycle_pass_fixture()
        for receipts in ([], original["published_release"]["debian_installation_receipts"][:1]):
            status = copy.deepcopy(original)
            status["published_release"]["debian_installation_receipts"] = receipts
            with self.subTest(receipts=len(receipts)), self.assertRaisesRegex(ValueError, "Debian PASS"):
                metadata.check_status(status)

    def test_debian_receipt_identity_and_success_are_checked(self):
        original = self.debian_lifecycle_pass_fixture()
        mutations = {"version": "9.9.9", "architecture": "s390x", "package": "other.deb",
                     "release_asset": "other.deb", "package_sha256": "0" * 64,
                     "anonymous_download": False, "native_binary_match": False, "lifecycle": False}
        for field, value in mutations.items():
            status = copy.deepcopy(original)
            status["published_release"]["debian_installation_receipts"][0][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "Debian"):
                metadata.check_status(status)

    def debian_lifecycle_pass_fixture(self):
        # Lifecycle rejection tests need an explicit claim, independent of the latest release's scope.
        status = json.loads((self.root / "acceptance/status.json").read_text())
        for arch in ("amd64", "arm64"):
            status["published_release"]["linux_" + arch + "_debian_lifecycle"] = "pass"
        for receipt in status["published_release"]["debian_installation_receipts"]:
            receipt["lifecycle"] = True
        metadata.check_status(status)
        return status

    def test_debian_receipts_need_unique_architectures_and_recorded_assets(self):
        original = json.loads((self.root / "acceptance/status.json").read_text())
        status = copy.deepcopy(original)
        receipts = status["published_release"]["debian_installation_receipts"]
        receipts.append(copy.deepcopy(receipts[0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            metadata.check_status(status)
        status = copy.deepcopy(original)
        status["published_release"]["release_assets"] = []
        with self.assertRaisesRegex(ValueError, "asset checksum"):
            metadata.check_status(status)

    def test_older_releases_without_debian_evidence_remain_valid(self):
        status = json.loads((self.root / "acceptance/status.json").read_text())
        release = status["published_release"]
        del release["debian_installation_receipts"]
        del release["release_assets"]
        for arch in ("amd64", "arm64"):
            del release["linux_" + arch + "_debian_lifecycle"]
        metadata.check_status(status)
