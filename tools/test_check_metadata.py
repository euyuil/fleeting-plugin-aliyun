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
