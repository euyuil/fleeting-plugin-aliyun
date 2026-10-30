"""Inject failures into the real concatenated bootstrap without installing tools."""
import os
from pathlib import Path
import shlex
import shutil
import stat
import subprocess
import tempfile
import unittest


@unittest.skipUnless(os.name == "posix", "worker bootstrap targets Unix")
class WorkerBootstrapTests(unittest.TestCase):
    def setUp(self):
        import pwd
        temp = tempfile.TemporaryDirectory(prefix="worker-bootstrap-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.bin, self.home, self.tmp = (self.root / name for name in ("bin", "home", "tmp"))
        for path in (self.bin, self.home, self.tmp):
            path.mkdir()
        self.state = self.root / "state"
        release = self.root / "os-release"
        release.write_text("ID=ubuntu\nVERSION_ID=26.04\nVERSION_CODENAME=resolute\n")
        keygen = shutil.which("ssh-keygen")
        subprocess.run([keygen, "-q", "-N", "", "-t", "ed25519", "-f", str(self.root / "key")], check=True)
        self.env = {**os.environ, "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                    "TMPDIR": str(self.tmp), "TEST_STATE": str(self.state), "TEST_HOME": str(self.home),
                    "TEST_USER": pwd.getpwuid(os.getuid()).pw_name, "TEST_UID": str(os.getuid()), "TEST_GID": str(os.getgid()),
                    "WORKER_USER": pwd.getpwuid(os.getuid()).pw_name, "WORKER_PUBLIC_KEY": (self.root / "key.pub").read_text().strip(),
                    "RUNNER_VERSION": "19.5.1", "RUNNER_SHA256": "synthetic", "PACKAGE_SNAPSHOT": "20261027T000000Z",
                    "INSTALL_DOCKER": "true"}
        fail = '''phase=$(cat "$TEST_STATE/phase" 2>/dev/null || :)
if test "$phase" = "${TEST_FAIL_PHASE:-none}"; then exit 17; fi
'''
        for name in ("apt-get", "systemctl", "docker", "git", "su"):
            self.shim(name, fail + "exit 0\n")
        self.shim("uname", "echo x86_64\n")
        self.shim("timeout", 'test "$1" = 10 || exit 99\nshift\nexec "$@"\n')
        self.shim("apt-cache", "printf 'Candidate: 1.0\\n'\n")
        self.shim("getent", '''printf '%s:x:%s:%s:test:%s:/bin/sh\n' "$TEST_USER" "$TEST_UID" "$TEST_GID" "$TEST_HOME"
''')
        self.shim("stat", '''for target do :; done
case "$target" in "$TEST_STATE"|"$TEST_STATE"/*) echo "${TEST_STATE_OWNER:-0}" ;; *) echo "$TEST_UID" ;; esac
''')
        self.shim("ssh-keygen", fail + "exec " + shlex.quote(keygen) + ' "$@"\n')
        self.shim("curl", fail + '''for target do :; done
printf '#!/bin/sh\nexit 0\n' > "$target"
''')
        self.shim("sha256sum", fail + "cat >/dev/null\nexit 0\n")
        source = Path("infra/bootstrap-job-worker.sh").read_text() + "\n" + Path("infra/bootstrap-linux.sh").read_text()
        self.source = source.replace("/etc/os-release", str(release)).replace("/run/fleeting-apt", str(self.root / "apt"))
        self.source = self.source.replace("/var/lib/fleeting-worker", str(self.state))
        self.source = self.source.replace("/usr/local/bin/gitlab-runner", str(self.bin / "gitlab-runner"))

    def shim(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(0o700)

    def run_bootstrap(self, **env):
        return subprocess.run(["sh"], input=self.source, env={**self.env, **env}, capture_output=True, text=True, timeout=10)

    def test_every_failure_phase_preserves_error_and_never_creates_readiness(self):
        for phase in range(1, 9):
            with self.subTest(phase=phase):
                result = self.run_bootstrap(TEST_FAIL_PHASE=str(phase))
                self.assertEqual(result.returncode, 17, result.stderr)
                self.assertFalse((self.state / "ready").exists())
                self.assertEqual((self.state / "failed").read_text(), str(phase) + "\n")
                self.assertFalse(list(self.tmp.glob("fleeting-runner.*")))
                self.assertFalse(list(self.state.glob(".state.*")))

    def test_success_verifies_tools_before_readiness_and_removes_download_temp(self):
        result = self.run_bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.state / "ready").is_file())
        self.assertFalse((self.state / "failed").exists())
        self.assertEqual((self.state / "phase").read_text(), "8\n")
        self.assertFalse(list(self.tmp.glob("fleeting-runner.*")))
        self.assertFalse(list(self.state.glob(".state.*")))

    def test_unowned_or_linked_status_cannot_modify_an_outside_file(self):
        self.state.mkdir()
        outside = self.root / "outside"
        outside.write_text("synthetic-private-content")
        (self.state / "phase").symlink_to(outside)
        result = self.run_bootstrap()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(), "synthetic-private-content")
        (self.state / "phase").unlink()
        result = self.run_bootstrap(TEST_STATE_OWNER="1000")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "ready").exists())

    def test_restrictive_umask_keeps_only_fixed_status_readable_and_error_intact(self):
        self.source = "umask 077\n" + self.source
        result = self.run_bootstrap(TEST_FAIL_PHASE="6")
        self.assertEqual(result.returncode, 17)
        for name in ("phase", "failed"):
            self.assertEqual((self.state / name).read_text(), "6\n")
            self.assertEqual(stat.S_IMODE((self.state / name).stat().st_mode), 0o644)
        self.assertFalse(list(self.tmp.glob("fleeting-runner.*")))

    def test_failed_status_publication_cannot_replace_the_bootstrap_error(self):
        move = shutil.which("mv")
        self.shim("mv", '''for target do :; done
if test "$target" = "$TEST_STATE/failed"; then exit 19; fi
exec ''' + shlex.quote(move) + ' "$@"\n')
        result = self.run_bootstrap(TEST_FAIL_PHASE="6")
        self.assertEqual(result.returncode, 17)
        self.assertFalse((self.state / "ready").exists())
        self.assertFalse(list(self.tmp.glob("fleeting-runner.*")))
        self.assertFalse(list(self.state.glob(".state.*")))

    def test_failed_status_temp_creation_does_not_reenter_the_parent_exit_trap(self):
        marker = self.root / "curl-failed"
        temporary = shutil.which("mktemp")
        self.shim("curl", 'touch "$TEST_FAILED_CURL"\nexit 17\n')
        self.shim("mktemp", '''case "$1" in "$TEST_STATE"/*)
  if test -f "$TEST_FAILED_CURL"; then exit 19; fi ;;
esac
exec ''' + shlex.quote(temporary) + ' "$@"\n')
        result = self.run_bootstrap(TEST_FAILED_CURL=str(marker))
        self.assertEqual(result.returncode, 17)
        self.assertFalse((self.state / "ready").exists())
        self.assertFalse(list(self.tmp.glob("fleeting-runner.*")))
        self.assertFalse(list(self.state.glob(".state.*")))
