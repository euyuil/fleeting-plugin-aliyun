"""Keep validation read-only and reject corrupt or unlisted distribution bytes."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from check_dist import check


class VerificationTests(unittest.TestCase):
    def test_distribution_integrity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / "candidate.tar.gz"
            package.write_bytes(b"synthetic archive")
            row = hashlib.sha256(package.read_bytes()).hexdigest() + "  " + package.name + "\n"
            (root / "SHA256SUMS").write_text(row)
            check(root)
            package.write_bytes(b"modified")
            with self.assertRaisesRegex(ValueError, "checksum"):
                check(root)
            package.write_bytes(b"synthetic archive")
            (root / "extra.deb").write_bytes(b"unlisted")
            with self.assertRaisesRegex(ValueError, "unlisted"):
                check(root)
            for invalid in ("", row + row, "0" * 64 + "  ../escape.deb\n"):
                (root / "SHA256SUMS").write_text(invalid)
                with self.assertRaises(ValueError):
                    check(root)

    def test_format_check_fails_without_rewriting(self):
        script = Path(__file__).with_name("check_format.py")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", directory], check=True)
            path = root / "sample.go"
            content = b"package sample\nfunc f( ){ }\n"
            path.write_bytes(content)
            subprocess.run(["git", "-C", directory, "add", "sample.go"], check=True)
            result = subprocess.run([sys.executable, str(script),], cwd=root, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b"sample.go", result.stdout)
            self.assertEqual(path.read_bytes(), content)

    def test_offline_live_entry_overrides_inherited_command_line_switches(self):
        with tempfile.TemporaryDirectory() as directory:
            fake = Path(directory) / "go"
            fake.write_text("#!" + sys.executable + "\nimport json, os, sys\n"
                            "print(json.dumps({'command':sys.argv[1], 'enabled':os.environ.get('RUN_LIVE_ACCEPTANCE'), 'mode':os.environ.get('LIVE_EXTENDED_MODE')}))\n")
            fake.chmod(0o755)
            result = subprocess.run(["make", "--no-print-directory", "test-live-offline", "GO=" + str(fake),
                                     "RUN_LIVE_ACCEPTANCE=1", "LIVE_EXTENDED_MODE=cloud-smoke"],
                                    cwd=Path(__file__).resolve().parents[1], capture_output=True, text=True, check=True)
            calls = [json.loads(line) for line in result.stdout.splitlines() if line.startswith("{")]
            tests = [call for call in calls if call["command"] == "test"]
            self.assertEqual(len(tests), 1)
            self.assertEqual((tests[0]["enabled"], tests[0]["mode"]), ("", ""))
