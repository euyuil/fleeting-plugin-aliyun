"""Protect lane ordering and fresh-host isolation in the executable workflow."""
from pathlib import Path
import re
import unittest

from cloud_fixture import JOB_SCENARIOS


class LaneWorkflowTests(unittest.TestCase):
    def test_each_case_depends_on_all_earlier_cases_even_across_unselected_skips(self):
        text = Path(".github/workflows/job-acceptance-lane.yml").read_text().partition("\njobs:\n")[2]
        blocks = dict(re.findall(r"^  ([a-z-]+):\n(.*?)(?=^  [a-z-]+:|\Z)", text, re.M | re.S))
        self.assertEqual(set(blocks), {"selection", *JOB_SCENARIOS})
        earlier = ["selection"]
        for scenario in JOB_SCENARIOS:
            block = blocks[scenario]
            needs = re.search(r"^    needs: \[([^]]+)\]$", block, re.M)
            dependencies = [name.strip() for name in needs[1].split(",")]
            self.assertEqual(dependencies, earlier)
            self.assertIn("!contains(needs.*.result, 'failure')", block)
            self.assertIn("!contains(needs.*.result, 'cancelled')", block)
            self.assertIn("always() && !cancelled()", block)
            self.assertIn("needs.selection.outputs['" + scenario + "'] == 'true'", block)
            self.assertIn("uses: ./.github/workflows/job-acceptance-case.yml", block)
            self.assertIn("permissions:\n      contents: read\n      id-token: write\n", block)
            self.assertIn("scenario: " + scenario + "\n", block)
            earlier.append(scenario)
        # A failed smoke remains visible to cancel, even if failure was not
        # selected. Checking only the immediately preceding result loses it.
        self.assertIn("needs: [selection, smoke, failure]", blocks["cancel"])

    def test_only_executor_lanes_are_parallel_and_cases_get_fresh_hosts(self):
        outer = Path(".github/workflows/job-acceptance.yml").read_text()
        self.assertIn("max-parallel: 2", outer)
        self.assertIn("fail-fast: false", outer)
        scenario = outer.partition("\n  scenario:\n")[2].partition("\n  gate:\n")[0]
        self.assertIn("permissions:\n      contents: read\n      id-token: write\n", scenario)
        self.assertIn("uses: ./.github/workflows/job-acceptance-lane.yml", outer)
        self.assertNotIn("matrix.scenario", outer)
        case = Path(".github/workflows/job-acceptance-case.yml").read_text()
        self.assertEqual(case.count("runs-on: ubuntu-26.04"), 1)
        self.assertNotIn("matrix:", case)
        self.assertIn("ACCEPTANCE_DISPOSABLE_HOST", Path("tools/job_acceptance.py").read_text())
        self.assertIn("secrets[inputs.executor == 'instance' && 'RELEASE_GITLAB_RUNNER_TOKEN' || 'RELEASE_GITLAB_DOCKER_RUNNER_TOKEN']", case)
        for workflow in ("cloud-acceptance.yml", "release.yml"):
            source = Path(".github/workflows", workflow).read_text()
            self.assertIn("group: aliyun-pre-release-acceptance", source)
            self.assertIn("cancel-in-progress: false", source)

    def test_docker_authentication_secret_reaches_every_reusable_call_boundary(self):
        key = "RELEASE_GITLAB_DOCKER_RUNNER_TOKEN"
        for workflow in ("cloud-acceptance.yml", "job-acceptance.yml", "job-acceptance-lane.yml", "job-acceptance-case.yml"):
            self.assertIn("      " + key + ":\n        required: false", Path(".github/workflows", workflow).read_text())
        mapping = key + ": ${{ secrets." + key + " }}"
        self.assertEqual(Path(".github/workflows/cloud-acceptance.yml").read_text().count(mapping), 1)
        self.assertEqual(Path(".github/workflows/release.yml").read_text().count(mapping), 2)
        self.assertIn("secrets: inherit", Path(".github/workflows/job-acceptance.yml").read_text())
        self.assertEqual(Path(".github/workflows/job-acceptance-lane.yml").read_text().count("secrets: inherit"), 9)


if __name__ == "__main__":
    unittest.main()
