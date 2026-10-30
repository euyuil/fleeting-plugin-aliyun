"""Exercise the shipped remote shell command with an isolated virtual clock."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest

from manager_diagnostics import manager_diagnostic


@unittest.skipUnless(os.name == "posix", "readiness commands target Unix workers")
class WorkerReadinessTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="worker-readiness-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.state = self.root / "state"
        self.state.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = {**os.environ, "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                    "TEST_STATE": str(self.state), "TEST_CLOCK": str(self.root / "clock"),
                    "TEST_PROBES": str(self.root / "probes"), "TEST_READY_AT": "360",
                    "TEST_OWNER_RECORD": str(self.root / "owner")}
        self.shim("timeout", '''test "$1" = 600 || exit 99
export TEST_LIMIT="$1"
shift
"$@"
code=$?
if test "$code" -eq 143; then
  if test -n "${TEST_AFTER_TIMEOUT_OWNER:-}"; then printf '%s' "$TEST_AFTER_TIMEOUT_OWNER" > "$TEST_OWNER_RECORD"; fi
  if test "${TEST_AFTER_TIMEOUT_READY:-false}" = true; then touch "$TEST_STATE/ready"; fi
  exit 124
fi
exit "$code"
''')
        self.shim("stat", 'if test -f "$TEST_OWNER_RECORD"; then cat "$TEST_OWNER_RECORD"; else printf "%s\\n" "${TEST_STATE_OWNER:-0}"; fi\n')
        self.shim("sleep", '''test "$1" = 2 || exit 99
elapsed=0
if test -f "$TEST_CLOCK"; then elapsed=$(cat "$TEST_CLOCK"); fi
elapsed=$((elapsed + 120))
printf '%s' "$elapsed" > "$TEST_CLOCK"
if test "$elapsed" -ge "$TEST_LIMIT"; then kill -TERM "$PPID"; exit 0; fi
if test "$TEST_READY_AT" -gt 0 && test "$elapsed" -ge "$TEST_READY_AT"; then touch "$TEST_STATE/ready"; fi
''')
        for name, variable in (("git", "TEST_GIT_STATUS"), ("gitlab-runner", "TEST_RUNNER_STATUS"), ("docker", "TEST_DOCKER_STATUS")):
            self.shim(name, "printf '%s\\n' " + name + ' >> "$TEST_PROBES"\n' +
                      'if test "${' + variable + ':-0}" = hang; then kill -TERM "$PPID"; exit 0; fi\n' +
                      'exit "${' + variable + ':-0}"\n')

    def shim(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(0o700)

    def run_ready(self, executor="docker-autoscaler", **env):
        with (Path("examples") / executor / "config.toml").open("rb") as stream:
            config = tomllib.load(stream)
        command = config["runners"][0]["autoscaler"]["instance_ready_command"]
        command = command.replace("/var/lib/fleeting-worker", str(self.state))
        return subprocess.run(["sh", "-c", command], env={**self.env, **env}, capture_output=True, text=True, timeout=5)

    def public_event(self, code):
        message = f"ERROR: ready up preparation failed err=ready command: Process exited with status {code} subsystem=taskscaler"
        raw = json.dumps({"MESSAGE": message}) + "\n"
        return manager_diagnostic({"manager_journal_state": "RECORDED", "manager_journal": raw})["records"][0]

    def test_preparation_can_cross_old_window_but_still_requires_every_probe(self):
        for executor in ("instance", "docker-autoscaler"):
            with self.subTest(executor=executor):
                (self.state / "ready").unlink(missing_ok=True)
                (self.root / "clock").unlink(missing_ok=True)
                (self.root / "probes").unlink(missing_ok=True)
                result = self.run_ready(executor)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertGreater(int((self.root / "clock").read_text()), 300)
                probes = (self.root / "probes").read_text().splitlines()
                self.assertEqual(probes, ["git", "gitlab-runner"] + (["docker"] if executor == "docker-autoscaler" else []))

    def test_missing_ready_still_hits_the_finite_deadline_and_reports_phase(self):
        (self.state / "phase").write_text("6\n")
        result = self.run_ready(TEST_READY_AT="0")
        self.assertEqual(result.returncode, 226)
        self.assertEqual(int((self.root / "clock").read_text()), 600)
        self.assertEqual(self.public_event(result.returncode)["bootstrap"], {"phase_observed": "runner_download", "outcome": "deadline"})
        self.assertFalse((self.root / "probes").exists())

    def test_explicit_failed_phase_never_waits_or_accepts_an_old_ready_marker(self):
        phases = ["ubuntu_packages", "docker_install", "docker_start", "base_packages", "ssh_setup", "runner_download",
                  "runner_verification", "bootstrap_probes"]
        (self.state / "ready").touch()
        for phase, name in enumerate(phases, 1):
            with self.subTest(phase=phase):
                (self.state / "failed").write_text(str(phase) + "\n")
                result = self.run_ready()
                self.assertEqual(result.returncode, 200 + phase)
                self.assertEqual(self.public_event(result.returncode)["bootstrap"], {"phase": name, "outcome": "failed"})
        self.assertFalse((self.root / "clock").exists())
        self.assertFalse((self.root / "probes").exists())

    def test_failed_probe_cannot_satisfy_readiness(self):
        (self.state / "ready").touch()
        for variable, code, phase in (("TEST_GIT_STATUS", 231, "git_probe"), ("TEST_RUNNER_STATUS", 232, "runner_probe"),
                                      ("TEST_DOCKER_STATUS", 233, "docker_probe")):
            with self.subTest(variable=variable):
                result = self.run_ready(**{variable: "1"})
                self.assertEqual(result.returncode, code)
                self.assertEqual(self.public_event(code)["bootstrap"], {"phase": phase, "outcome": "failed"})

    def test_unknown_or_linked_status_is_bounded_and_never_published(self):
        private = "PRIVATE-STATUS-CANARY"
        (self.state / "failed").write_text(private * 10000)
        result = self.run_ready()
        self.assertEqual(result.returncode, 209)
        self.assertNotIn(private, result.stdout + result.stderr + json.dumps(self.public_event(result.returncode)))
        (self.state / "failed").unlink()
        outside = self.root / "outside"
        outside.write_text("1\n" + private)
        (self.state / "phase").symlink_to(outside)
        result = self.run_ready(TEST_READY_AT="0")
        self.assertEqual(result.returncode, 209)
        self.assertNotIn(private, result.stdout + result.stderr + json.dumps(self.public_event(result.returncode)))
        (self.state / "phase").unlink()
        os.mkfifo(self.state / "phase")
        self.assertEqual(self.run_ready(TEST_READY_AT="0").returncode, 209)

    def test_probe_deadline_and_unowned_state_cannot_pass(self):
        (self.state / "ready").touch()
        result = self.run_ready(TEST_DOCKER_STATUS="hang")
        self.assertEqual(result.returncode, 234)
        self.assertEqual(self.public_event(234)["bootstrap"], {"phase_observed": "ready_marker_present", "outcome": "deadline"})
        result = self.run_ready(TEST_STATE_OWNER="1000")
        self.assertEqual(result.returncode, 209)

    def test_post_deadline_snapshot_is_checked_and_does_not_prove_probe_start(self):
        result = self.run_ready(TEST_READY_AT="0", TEST_AFTER_TIMEOUT_READY="true")
        self.assertEqual(result.returncode, 234)
        self.assertEqual(self.public_event(234)["bootstrap"], {"phase_observed": "ready_marker_present", "outcome": "deadline"})
        self.assertFalse((self.root / "probes").exists())
        (self.state / "ready").unlink()
        (self.root / "clock").unlink()
        result = self.run_ready(TEST_READY_AT="0", TEST_AFTER_TIMEOUT_OWNER="1000")
        self.assertEqual(result.returncode, 229)

    def test_phase_codes_on_unrelated_connector_errors_are_not_bootstrap_evidence(self):
        message = "ERROR: connection preparation failed err=Process exited with status 206 subsystem=taskscaler"
        value = manager_diagnostic({"manager_journal_state": "RECORDED", "manager_journal": json.dumps({"MESSAGE": message})})
        self.assertNotIn("bootstrap", value["records"][0])
