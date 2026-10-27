import pathlib
import runpy
import subprocess
import unittest
from unittest import mock

MODULE = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1] / "infra" / "wait-available.py"))


class InfrastructureWaitTests(unittest.TestCase):
    def test_query_duration_is_inside_deadline(self):
        with mock.patch("time.monotonic", side_effect=[0, 0, 60]):
            with mock.patch("subprocess.run", side_effect=subprocess.TimeoutExpired("aliyun", 5)) as call:
                with self.assertRaisesRegex(SystemExit, "deadline exceeded"):
                    MODULE["wait"]("vpc", "cn-test-1", "vpc-test")
                self.assertEqual(call.call_args.kwargs["timeout"], 5)

    def test_available_status_requires_exact_identity(self):
        reply = subprocess.CompletedProcess([], 0, '{"Vpcs":{"Vpc":[{"VpcId":"vpc-other","Status":"Available"}]}}')
        with mock.patch("subprocess.run", return_value=reply):
            with self.assertRaisesRegex(SystemExit, "identity mismatch"):
                MODULE["wait"]("vpc", "cn-test-1", "vpc-test")
