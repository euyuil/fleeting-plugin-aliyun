"""Public installation must fail closed after attempting all owned cleanup."""
import importlib.util
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


class DebianInstallCleanupTests(unittest.TestCase):
    def test_failed_purge_state_cleanup_or_identity_removal_never_passes(self):
        spec = importlib.util.spec_from_file_location("debian_lifecycle_test", Path(__file__).with_name("test-debian-lifecycle.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        original_rmtree = shutil.rmtree
        for fault in ("", "purge", "state", "identity"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                paths = {name: root / name for name in ("CONFIG", "HOME", "RECEIPT", "FIXTURE", "UNIT", "RUNTIME_UNIT")}
                for name in ("CONFIG", "HOME", "RECEIPT", "FIXTURE"):
                    paths[name].mkdir()
                wrapper = root / "runner"
                wrapper.write_text("synthetic wrapper")
                calls, user = [], [True]

                def command(args, **kwargs):
                    calls.append(args[0])
                    code = 0
                    if args[0] == "getent":
                        code = 0 if user[0] else 2
                    elif args[0] == "userdel":
                        if fault == "identity":
                            code = 1
                        else:
                            user[0] = False
                    elif args[0] == "dpkg" and fault == "purge":
                        code = 1
                    return SimpleNamespace(returncode=code, stdout="", stderr="")

                def remove(path, **kwargs):
                    if fault == "state" and path == paths["CONFIG"]:
                        raise OSError("synthetic cleanup failure")
                    original_rmtree(path, **kwargs)

                with patch.multiple(module, CLI=str(root / "plugin"), **paths), \
                        patch.object(module.subprocess, "run", side_effect=command), patch.object(module.shutil, "rmtree", side_effect=remove):
                    if fault:
                        with self.assertRaises(RuntimeError):
                            module.cleanup_install(wrapper)
                    else:
                        module.cleanup_install(wrapper)
                self.assertIn("dpkg", calls)
                self.assertIn("userdel", calls)
                self.assertFalse(wrapper.exists())
                self.assertFalse(paths["FIXTURE"].exists())


if __name__ == "__main__":
    unittest.main()
