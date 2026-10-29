import hashlib
import io
from pathlib import Path
import shutil
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import debian_upgrade as upgrade


class DebianUpgradeTests(unittest.TestCase):
    def test_anonymous_baseline_download_requires_both_pinned_hashes(self):
        package = b"released package bytes"
        name = "fleeting-plugin-aliyun_0.1.0~rc.3_amd64.deb"
        inventory = (hashlib.sha256(package).hexdigest() + "  " + name + "\n").encode()
        pin = {**upgrade.baseline("amd64"), "package_sha256": hashlib.sha256(package).hexdigest(),
               "checksums_sha256": hashlib.sha256(inventory).hexdigest()}
        for corrupt in (None, "inventory", "package"):
            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as temp, patch.object(upgrade, "baseline", return_value=pin), \
                    patch.object(upgrade.urllib.request, "urlopen", side_effect=[io.BytesIO(inventory if corrupt != "inventory" else b"wrong"),
                                                                              io.BytesIO(package if corrupt != "package" else b"wrong")]) as request:
                if corrupt:
                    with self.assertRaises(ValueError):
                        upgrade.fetch_baseline(Path(temp), "amd64")
                else:
                    path = upgrade.fetch_baseline(Path(temp), "amd64")
                    self.assertEqual(path.read_bytes(), package)
                    self.assertEqual(upgrade.fetch_baseline(Path(temp), "amd64"), path)
                    self.assertEqual(request.call_count, 2)
                for call in request.call_args_list:
                    self.assertTrue(call.args[0].startswith("https://github.com/euyuil/fleeting-plugin-aliyun/releases/download/v0.1.0-rc.3/"))
                    self.assertNotIn("headers", call.kwargs)

    @unittest.skipUnless(shutil.which("dpkg"), "Debian version ordering requires dpkg (native CI)")
    def test_candidate_version_must_be_a_real_upgrade(self):
        for version in ("0.1.0-dev", "0.1.0-rc.3", "0.0.9"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                upgrade.require_newer(version)
        upgrade.require_newer("0.1.0-rc.4")
        upgrade.require_newer("0.1.0")

    def test_package_bytes_and_metadata_are_checked_before_install(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(upgrade, "command") as command:
            path = Path(temp) / "previous.deb"
            path.write_bytes(b"wrong package")
            with self.assertRaises(ValueError):
                upgrade.verify_package(path, "a" * 64, "0.1.0-rc.3", "amd64")
            command.assert_not_called()
            command.side_effect = ["fleeting-plugin-aliyun", "0.1.0~rc.2"]
            with self.assertRaises(ValueError):
                upgrade.verify_package(path, upgrade.digest(path), "0.1.0-rc.3", "amd64")

    def test_running_manager_prevents_any_package_mutation(self):
        with patch.object(upgrade, "command", return_value="active") as command:
            with self.assertRaises(ValueError):
                upgrade.stopped()
            self.assertEqual(command.call_count, 1)
        for states in (("inactive", "1"), ("inactive", "0", "/system.slice/owned.service")):
            with patch.object(upgrade, "command", side_effect=states), self.assertRaises(ValueError):
                upgrade.stopped()

    def test_snapshot_covers_file_contents_directory_modes_and_account(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            private = root / "private"
            private.mkdir(mode=0o700)
            config = private / "config"
            config.write_text("synthetic configuration")
            account = SimpleNamespace(pw_uid=123, pw_gid=456, pw_dir="/synthetic/home", pw_shell="/bin/false")
            with patch.object(upgrade, "STATE", (root,)), patch.object(upgrade.pwd, "getpwnam", return_value=account):
                before = upgrade.fingerprint()
                config.write_text("changed configuration")
                self.assertNotEqual(upgrade.fingerprint(), before)
                config.write_text("synthetic configuration")
                self.assertEqual(upgrade.fingerprint(), before)
                private.chmod(0o755)
                self.assertNotEqual(upgrade.fingerprint(), before)
                private.chmod(0o700)
                account.pw_gid += 1
                self.assertNotEqual(upgrade.fingerprint(), before)
                (root / "symlink").symlink_to(config)
                with self.assertRaises(ValueError):
                    upgrade.fingerprint()

    def test_transition_requires_old_candidate_old_candidate_and_stable_state(self):
        pin = upgrade.baseline("amd64")
        for fault in (None, "initial-binary", "state", "rollback-binary", "final-binary", "active"):
            with self.subTest(fault=fault):
                installs = []
                def command(*args):
                    if args[0] == "dpkg-query":
                        return "0.1.0~rc.4" if len(installs) in (1, 3) else "0.1.0~rc.3"
                    if args[:2] == ("dpkg", "-i"):
                        installs.append(args[2])
                    return ""
                hashes = [pin["binary_sha256"], "c" * 64, pin["binary_sha256"], "c" * 64]
                for name, index in (("initial-binary", 0), ("rollback-binary", 2), ("final-binary", 3)):
                    if fault == name:
                        hashes[index] = "bad"
                with patch.object(upgrade, "require_newer"), patch.object(upgrade, "verify_package"), \
                        patch.object(upgrade, "command", side_effect=command), patch.object(upgrade, "digest", side_effect=hashes), \
                        patch.object(upgrade, "fingerprint", side_effect=["before", "changed" if fault == "state" else "before", "before", "before"]), \
                        patch.object(upgrade, "stopped", side_effect=ValueError("active") if fault == "active" else None):
                    args = (Path("candidate.deb"), "d" * 64, "c" * 64, "0.1.0-rc.4", Path("previous.deb"), "amd64")
                    if fault:
                        with self.assertRaises(ValueError):
                            upgrade.transition(*args)
                        if fault in ("active", "initial-binary"):
                            self.assertEqual(installs, [])
                    else:
                        proof = upgrade.transition(*args)
                        self.assertEqual(installs, [Path("candidate.deb"), Path("previous.deb"), Path("candidate.deb")])
                        self.assertTrue(proof["state_preserved"])
                        self.assertTrue(proof["rollback_verified"])

    def test_command_failure_does_not_expose_private_output(self):
        stdout, stderr = "synthetic private stdout", "synthetic private stderr"
        with patch.object(upgrade.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, stdout, stderr)):
            with self.assertRaises(ValueError) as error:
                upgrade.command("false")
            self.assertNotIn(stdout, str(error.exception))
            self.assertNotIn(stderr, str(error.exception))


if __name__ == "__main__":
    unittest.main()
