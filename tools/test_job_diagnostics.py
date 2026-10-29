"""Diagnostics must select the owned case and never publish private API data."""
import copy
import io
import json
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

import job_diagnostics as diagnostic

SHA, FIXTURE = "a" * 40, "b" * 40
START, END = "2026-01-01T00:00:00Z", "2026-01-01T00:30:00Z"
CONFIG = {"url": "https://gitlab.com", "project_id": "1", "runner_id": "2"}
SSH = "ERROR: Job failed: run exit (exit code: 0): wait: remote command exited without exit status or exit signal"


class Client:
    def __init__(self):
        self.calls = []
        self.ref = "acceptance/" + SHA
        self.pipeline = {"id": 3, "ref": self.ref, "sha": FIXTURE, "created_at": "2026-01-01T00:10:00.123Z", "status": "failed"}
        self.pipelines = [self.pipeline]
        self.variables = [{"key": k, "value": v} for k, v in {"FLEETING_EXECUTION_MODE": "instance", "FLEETING_JOB_SCENARIO": "worker-loss",
                          "FLEETING_EXPECTED_RUNNER_ID": "2", "FLEETING_NONCE": "c" * 48}.items()]
        self.jobs = [{"id": 4, "pipeline": {"id": 3}, "commit": {"id": FIXTURE}, "ref": self.ref, "runner": {"id": 2},
                      "status": "failed", "failure_reason": "script_failure", "private": "private-secret"}]
        self.trace = ("00O \x1b[31;1m" + SSH + "\x1b[0;m\r\nprivate-secret").encode()
        self.project = {"id": 1, "visibility": "private"}

    def branch(self, ref):
        assert ref == self.ref
        return FIXTURE

    def request(self, path, *, raw=False):
        # No body/mutation interface is provided: every supported call is a GET.
        self.calls.append(path)
        if path.startswith("/pipelines?"):
            return self.pipelines
        return {"": self.project, "/pipelines/3/variables?per_page=100": self.variables,
                "/pipelines/3": self.pipeline, "/pipelines/3/jobs?per_page=100&include_retried=true": self.jobs,
                "/jobs/4/trace": self.trace}[path]


def inspect(client):
    return diagnostic.inspect(client, CONFIG, SHA, "instance", "worker-loss", START, END)


class JobDiagnosticTests(unittest.TestCase):
    def test_owned_case_projects_only_safe_fields(self):
        client = Client()
        result = inspect(client)
        self.assertEqual(result["pipeline_status"], "failed")
        self.assertEqual(result["job"]["failure_reason"], "script_failure")
        self.assertEqual(result["trace"]["ssh_exit_codes"], [0])
        self.assertNotIn("private", json.dumps(result))
        self.assertNotIn(FIXTURE, json.dumps(result))
        self.assertEqual(client.calls[-1], "/jobs/4/trace")

    def test_ambiguous_unowned_or_incomplete_selection_never_reads_trace(self):
        changes = (
            lambda c: c.project.update(visibility="public"),
            lambda c: c.project.update(id=9),
            lambda c: c.pipelines.append(copy.deepcopy(c.pipeline)),
            lambda c: c.pipelines.extend([c.pipeline] * 99),
            lambda c: c.pipeline.update(ref="unrelated"),
            lambda c: c.pipeline.update(sha="d" * 40),
            lambda c: c.pipeline.update(created_at="2026-01-01T01:00:00Z"),
            lambda c: c.variables[0].update(value="docker-autoscaler"),
            lambda c: c.variables[2].update(value="9"),
            lambda c: c.variables.append(c.variables[0]),
            lambda c: c.jobs.append(copy.deepcopy(c.jobs[0])),
            lambda c: c.jobs[0]["runner"].update(id=9),
            lambda c: c.jobs[0]["pipeline"].update(id=9),
            lambda c: c.jobs[0]["commit"].update(id="d" * 40),
        )
        for change in changes:
            with self.subTest(change=change):
                client = Client()
                change(client)
                with self.assertRaises(ValueError):
                    inspect(client)
                self.assertFalse(any(path.endswith("/trace") for path in client.calls))

    def test_window_requires_utc_order_and_bounded_duration(self):
        for start, end in ((END, START), (START, START), (START, "2026-01-01T03:00:00Z"), (START.removesuffix("Z"), END)):
            client = Client()
            with self.assertRaises(ValueError):
                diagnostic.inspect(client, CONFIG, SHA, "instance", "worker-loss", start, end)
            self.assertEqual(client.calls, [])

    def test_upgrade_selects_its_smoke_job(self):
        client = Client()
        client.variables[1]["value"] = "smoke"
        value = diagnostic.inspect(client, CONFIG, SHA, "instance", "upgrade", START, END)
        self.assertEqual(value["job"]["status"], "failed")

    def test_trace_framing_csi_and_exit_codes_are_classified_not_copied(self):
        trace = ("2026-01-01T00:00:00.123456Z 00O \x1b[0K\x1b[31;1m" + SSH.replace("code: 0", "code: -1") +
                 "\x1b[0;m\r\n00E ERROR: Job failed: exit status 255\nprivate-secret").encode()
        value = diagnostic.trace_diagnostic(trace)
        self.assertEqual(value["ssh_exit_codes"], [-1])
        self.assertFalse(value["ssh_message_with_sgr_only"])
        self.assertTrue(value["ssh_message_with_csi_removed"])
        self.assertEqual(value["script_exit_codes"], [255])
        for line in ("01+ " + SSH, "$ echo " + SSH, SSH + " private-secret", SSH.replace("code: 0", "code: 999")):
            self.assertEqual(diagnostic.trace_diagnostic(line.encode())["ssh_exit_codes"], [])

    def test_unknown_and_malformed_values_cannot_escape_projection(self):
        for value in ("private-secret", [], {}, True, 10, None):
            result = diagnostic.outcome({"status": value, "failure_reason": value, "checkpoint": value, "private": "private-secret"})
            self.assertEqual(result["status"], "unknown")
            self.assertEqual(result["failure_reason"], "unknown")
            self.assertNotIn("private", json.dumps(result))
        self.assertEqual(diagnostic.outcome([]), {"status": "unknown", "failure_reason": "unknown"})

    def test_unrecognized_terminal_error_has_only_fixed_words_and_bounded_code(self):
        line = "ERROR: Job failed: run exit (exit code: 137): Process exited with status 137 from signal KILL"
        value = diagnostic.trace_diagnostic(("00E " + line).encode())["terminal_errors"][0]
        self.assertEqual(value["connector_exit_code"], 137)
        self.assertEqual(value["words"][-6:], ["with", "status", "?", "from", "signal", "KILL"])
        private = "ERROR: Job failed: read tcp 192.0.2.1:22 private-project private-token private.example.invalid i-private: connection reset by peer"
        value = diagnostic.trace_diagnostic((private + "\n").encode() * 10)
        self.assertEqual(len(value["terminal_errors"]), 4)
        for item in value["terminal_errors"]:
            self.assertTrue(set(item["words"]) <= diagnostic.ERROR_WORDS | {"?"})
            self.assertIsNone(item["connector_exit_code"])
        self.assertNotIn("private", json.dumps(value))
        self.assertNotIn("192", json.dumps(value))
        self.assertEqual(len(diagnostic.error_outline("error " * 1000)["words"]), 64)
        for code in ("999", "-2", "123456789"):
            self.assertIsNone(diagnostic.error_outline(line.replace("code: 137", "code: " + code))["connector_exit_code"])

    def test_keepalive_wrapped_missing_exit_is_visible_without_raw_trace(self):
        line = SSH.replace("ERROR: Job failed: ", "ERROR: Job failed: ssh connection closed by keepalive (keepalive probe timed out after 30s): ")
        result = diagnostic.trace_diagnostic(("00E \x1b[31;1m" + line + "\x1b[0;m\r\n").encode())
        self.assertTrue(result["ssh_message_with_sgr_only"])
        self.assertEqual(result["ssh_exit_codes"], [0])
        self.assertEqual(result["terminal_errors"][0]["connector_exit_code"], 0)
        self.assertNotIn("30s", json.dumps(result))

    def test_cli_failure_never_prints_exception_or_private_api_fields(self):
        args = ["job_diagnostics.py", "--sha", SHA, "--executor", "instance", "--scenario", "worker-loss",
                "--started-at", START, "--completed-at", END]
        with patch("sys.argv", args), patch.object(diagnostic, "decode", return_value=CONFIG), \
                patch.object(diagnostic, "ReadOnlyGitLab", side_effect=ValueError("private-secret")), redirect_stdout(io.StringIO()) as output:
            self.assertEqual(diagnostic.main(), 1)
        self.assertNotIn("private", output.getvalue())
        self.assertEqual(json.loads(output.getvalue())["diagnostic"], "unavailable")

    def test_read_client_rejects_mutation_arguments_and_unrelated_routes(self):
        client = diagnostic.ReadOnlyGitLab(CONFIG, "synthetic-token")
        with patch.object(diagnostic.GitLab, "request", return_value={}) as request, redirect_stdout(io.StringIO()) as output:
            client.request("/jobs/4/trace", raw=True)
            request.assert_called_once_with("/jobs/4/trace", raw=True)
            with self.assertRaises(TypeError):
                client.request("/pipelines/3", {"private": "private-secret"})
            with self.assertRaises(ValueError):
                client.request("/pipelines/3/cancel")
        self.assertEqual(json.loads(output.getvalue()), {"gitlab_read": "trace"})


if __name__ == "__main__":
    unittest.main()
