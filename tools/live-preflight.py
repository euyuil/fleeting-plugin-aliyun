#!/usr/bin/env python3
"""Check workflow inputs without contacting cloud or GitLab services."""
import json
import os
import pathlib
required = ["ALIYUN_REGION", "ALIYUN_SCALING_GROUP_ID", "GITLAB_PROJECT_ID", "GITLAB_FIXTURE_REF", "GITLAB_FIXTURE_SHA", "GITLAB_RUNNER_TAG", "GITLAB_EXPECTED_RUNNER_ID", "GITLAB_RUNNER_TOKEN", "GITLAB_API_TOKEN", "WORKER_SSH_PRIVATE_KEY", "ALIYUN_CLEANUP_ROLE_ARN", "ALIYUN_ROLE_ARN", "ALIYUN_OIDC_PROVIDER_ARN", "ALLOWED_INSTANCE_TYPE", "ALLOWED_IMAGE_ID", "ALLOWED_ZONE_ID", "MANAGER_CIDR"]
missing = [key for key in required if not os.environ.get(key)]
if os.environ.get("ENABLE_ACCEPTANCE_JANITOR") != "true":
    missing.append("ENABLE_ACCEPTANCE_JANITOR=true")
folder = pathlib.Path(".live") / ("run-" + os.environ["GITHUB_RUN_ID"] + "-" + os.environ["GITHUB_RUN_ATTEMPT"])
folder.mkdir(mode=0o700, parents=True, exist_ok=True)
if missing:
    (folder / "result.json").write_text(json.dumps({"schema_version": 1, "result": "NOT_RUN_MISSING_CONFIGURATION", "mode": "smoke", "missing_configuration": missing, "cleanup_result": "NOT_RUN", "leftover_resources": []}, indent=2) + "\n")
    raise SystemExit("Missing configuration: " + ", ".join(missing))
key = pathlib.Path(os.environ.get("WORKER_SSH_KEY", ".live/worker-key"))
key.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
key.write_text(os.environ["WORKER_SSH_PRIVATE_KEY"])
key.chmod(0o600)
