"""Regression for root bootstrap following a user-controlled SSH directory link."""
import os
import pathlib
import subprocess
import tempfile
import unittest


@unittest.skipUnless(os.name == "posix", "bootstrap targets Unix images")
class BootstrapPathTests(unittest.TestCase):
    def test_linked_ssh_directory_cannot_change_outside_files(self):
        import pwd
        script = pathlib.Path(__file__).resolve().parents[1] / "infra" / "bootstrap-linux.sh"
        with tempfile.TemporaryDirectory(prefix="bootstrap-path-test-") as folder:
            root = pathlib.Path(folder)
            home, outside, binaries = root / "home", root / "outside", root / "bin"
            for path in (home, outside, binaries):
                path.mkdir()
            (home / ".ssh").symlink_to(outside, target_is_directory=True)
            subprocess.run(["ssh-keygen", "-q", "-N", "", "-t", "ed25519", "-f", str(root / "key")], check=True)
            user = pwd.getpwuid(os.getuid()).pw_name
            shims = {
                "apt-get": "exit 0\n",
                "uname": "echo aarch64\n",
                "getent": "printf '%s:x:%s:%s:test:%s:/bin/sh\\n' \"$USER\" \"$TEST_UID\" \"$TEST_GID\" \"$TEST_HOME\"\n",
                "curl": "exit 99\n",
            }
            for name, body in shims.items():
                path = binaries / name
                path.write_text("#!/bin/sh\n" + body)
                path.chmod(0o755)
            env = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ["PATH"], USER=user,
                       TEST_HOME=str(home), TEST_UID=str(os.getuid()), TEST_GID=str(os.getgid()), WORKER_USER=user,
                       WORKER_PUBLIC_KEY=(root / "key.pub").read_text().strip(),
                       RUNNER_VERSION="19.5.1", RUNNER_SHA256="synthetic", GIT_PACKAGE_VERSION="synthetic",
                       CURL_PACKAGE_VERSION="synthetic", CA_PACKAGE_VERSION="synthetic", SSH_PACKAGE_VERSION="synthetic",
                       COREUTILS_PACKAGE_VERSION="synthetic")
            source = script.read_text().replace("/var/lib/fleeting-worker", str(root / "state"))
            result = subprocess.run(["sh"], input=source, env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Unsafe worker SSH directory", result.stderr)
            self.assertFalse((outside / "authorized_keys").exists())
