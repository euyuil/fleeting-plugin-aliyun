"""Bind real GitLab jobs on an installed systemd Manager to one release candidate."""
import argparse
from datetime import datetime, timedelta, timezone
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import urllib.request

from cloud_config import config_digest, decode, fixture_config
from cloud_fixture import JOB_SCENARIOS, Cloud, Fixture, labels, preflight, require_linux, stop_process_group
from cloud_diagnostics import driver_diagnostic
from debian_package import debian_version
from gitlab_fixture import prepare as prepare_gitlab_fixture
from release_candidate import digest, revision, verify, write_json

EXECUTORS = ("instance", "docker-autoscaler")
PHASES = ("candidate", "public")


def configuration(env=os.environ, *, read_only=False):
    preflight(env, read_only=read_only)
    if not read_only and env.get("RELEASE_JOB_ACCEPTANCE_ENABLED") != "true":
        raise ValueError("explicitly enable job acceptance before resource creation")
    job = decode("RELEASE_ALIYUN_JOB_FIXTURE_CONFIG", env)
    if not isinstance(job, dict) or set(job) != {"fixture", "job_image"}:
        raise ValueError("job fixture requires exactly fixture and job_image")
    fixture_config({**env, "RELEASE_ALIYUN_FIXTURE_CONFIG": json.dumps(job["fixture"])})
    if not isinstance(job["job_image"], str) or not re.fullmatch(r"[a-z0-9][a-z0-9.:-]*/[a-z0-9/_-]+@sha256:[0-9a-f]{64}", job["job_image"]):
        raise ValueError("job image must be an explicit registry/repository digest")
    gitlab = decode("RELEASE_GITLAB_CONFIG", env)
    fields = {"url", "project_id", "fixture_ref", "fixture_sha", "runner_id", "runner_tag"}
    if not isinstance(gitlab, dict) or set(gitlab) != fields or gitlab["url"] != "https://gitlab.com":
        raise ValueError("configure the dedicated GitLab.com acceptance project")
    if any(not isinstance(gitlab[k], str) or not re.fullmatch(r"[1-9][0-9]*", gitlab[k]) for k in ("project_id", "runner_id")):
        raise ValueError("positive project and runner identities are required")
    revision(gitlab["fixture_sha"])
    if not isinstance(gitlab["fixture_ref"], str) or not re.fullmatch(r"[A-Za-z0-9_./-]{1,128}", gitlab["fixture_ref"]):
        raise ValueError("invalid pinned fixture ref")
    if not isinstance(gitlab["runner_tag"], str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", gitlab["runner_tag"]):
        raise ValueError("invalid dedicated runner tag")
    if any(not env.get(k) for k in ("RELEASE_GITLAB_API_TOKEN", "RELEASE_GITLAB_RUNNER_TOKEN")):
        raise ValueError("configure both project-scoped GitLab credentials")
    return job, gitlab


def package_path(manifest):
    return f"dist/fleeting-plugin-aliyun_{debian_version(manifest['version'])}_amd64.deb"


def worker_script(executor):
    lock = json.loads(Path("upstream.lock.json").read_text())
    values = {"RUNNER_VERSION": lock["runner"]["version"], "RUNNER_SHA256": lock["runner"]["sha256"]["linux-amd64"],
              "WORKER_USER": "root", "PACKAGE_SNAPSHOT": lock["job_worker"]["ubuntu_snapshot"],
              "INSTALL_DOCKER": "true" if executor == "docker-autoscaler" else "false"}
    return ("set -eu\n" + "".join("export " + key + "=" + shlex.quote(value) + "\n" for key, value in values.items())
            + Path("infra/bootstrap-job-worker.sh").read_text() + "\n" + Path("infra/bootstrap-linux.sh").read_text())


def install_package(root, manifest, phase, directory):
    if os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted" or os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("installation requires a disposable GitHub-hosted machine")
    for path in ("/usr/bin/gitlab-runner", "/usr/bin/fleeting-plugin-aliyun", "/etc/gitlab-runner-aliyun",
                 "/var/lib/gitlab-runner-aliyun", "/var/lib/fleeting-plugin-aliyun", "/run/fleeting-acceptance-owner.json"):
        if os.path.lexists(path):
            raise ValueError("refusing an existing Runner or Manager installation")
    package = root / package_path(manifest)
    if phase == "public":
        # urllib sends no GitHub or registry credentials. Never fall back to the
        # private candidate when anonymous publication cannot be retrieved.
        base = f"https://github.com/{os.environ['GITHUB_REPOSITORY']}/releases/download/v{manifest['version']}"
        public = directory / "public"
        public.mkdir()
        for name in (package.name, "SHA256SUMS"):
            asset = name.replace("~", ".")
            with urllib.request.urlopen(base + "/" + asset, timeout=60) as response:
                (public / name).write_bytes(response.read())
            if digest(public / name) != manifest["files"]["dist/" + name]:
                raise ValueError("public distribution differs from the accepted candidate")
        package = public / package.name
    runner = subprocess.check_output([sys.executable, "tools/fetch-runner.py"], text=True).strip()
    with (directory / "install.log").open("w") as output:
        for command in (["sudo", "-n", "apt-get", "update"],
                        ["sudo", "-n", "apt-get", "install", "-y", "--no-install-recommends", str(package.resolve())],
                        ["sudo", "-n", "install", "-m", "0755", runner, "/usr/bin/gitlab-runner"]):
            subprocess.run(command, check=True, stdout=output, stderr=subprocess.STDOUT, timeout=300)
    if digest("/usr/bin/fleeting-plugin-aliyun") != manifest["files"][manifest["normal_binary"]]:
        raise ValueError("installed Debian binary differs from the sealed candidate")


def job_cases(scenario):
    return ["capacity"] * 3 if scenario == "capacity" else (["smoke"] if scenario == "smoke" else [scenario, "smoke"])


def check_result(result, manifest, gitlab, executor, lock, run_id, attempt, scenario="smoke"):
    cases = job_cases(scenario)
    expected = {"result": "PASS", "cleanup_result": "PASS", "normal_release": True,
                "mode": "smoke", "executor": executor, "manager_mode": "systemd", "job_scenario": scenario,
                "plugin_commit": manifest["candidate_sha"], "plugin_version": manifest["version"],
                "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                "runner_sha256": lock["runner"]["sha256"]["linux-amd64"], "runner_version": lock["runner"]["version"],
                "gitlab_fixture_sha": gitlab["fixture_sha"], "gitlab_project_id": gitlab["project_id"], "gitlab_runner_id": int(gitlab["runner_id"]),
                "github_run_id": run_id, "github_run_attempt": attempt, "instance_limit": 2 if scenario == "capacity" else 1,
                "creation_budget": len(cases), "evidence_incomplete": False}
    if scenario == "capacity":
        expected["capacity_verified"] = True
    if scenario == "drain":
        expected["drain_verified"] = True
    if not isinstance(result, dict) or any(type(result.get(k)) is not type(v) or result[k] != v for k, v in expected.items()):
        raise ValueError("job did not produce a passing bound systemd acceptance result")
    if any(type(result.get(k)) is not int or result[k] <= 0 for k in ("gitlab_pipeline_id", "gitlab_job_id")):
        raise ValueError("job evidence lacks concrete pipeline and job identities")
    instances = result.get("instances")
    if (not isinstance(instances, list) or len(instances) != len(cases) or "leftover_resources" not in result or result["leftover_resources"] or result.get("errors")
            or any(not isinstance(row, dict) or any(row.get(k) is not True for k in ("spot_verified", "inventory_complete", "deletion_verified")) for row in instances)):
        raise ValueError("job evidence lacks the exact Spot worker and deletion proof")
    steps = result.get("jobs")
    if not isinstance(steps, list) or len(steps) != len(cases) or type(result.get("manager_pid")) is not int or result["manager_pid"] <= 0:
        raise ValueError("job sequence or persistent Manager identity is missing")
    seen = {k: set() for k in ("pipeline_id", "job_id", "nonce", "instance_id")}
    for index, (step, case) in enumerate(zip(steps, cases)):
        pid = step.get("manager_pid") if isinstance(step, dict) else None
        manager_matches = type(pid) is int and pid > 0 and (pid != result["manager_pid"] if scenario == "drain" and index == 0 else pid == result["manager_pid"])
        status, reason = {"smoke": ("success", ""), "capacity": ("success", ""), "drain": ("success", ""), "failure": ("failed", "script_failure"),
                          "cancel": ("canceled", None), "timeout": ("failed", "job_execution_timeout")}[case]
        if (not isinstance(step, dict) or step.get("scenario") != case or step.get("status") != status or
                (reason is not None and step.get("failure_reason") != reason) or step.get("normal_release") is not True or
                not manager_matches or
                step.get("cancel_requested") is not (case == "cancel") or (case != "smoke" and step.get("checkpoint") is not True) or
                (case in ("capacity", "drain") and step.get("control_released") is not True)):
            raise ValueError("unexpected job outcome, restart or incomplete normal release")
        for key in seen:
            value = step.get(key)
            valid = (type(value) is int and value > 0) if key.endswith("_id") and key != "instance_id" else (
                isinstance(value, str) and re.fullmatch(r"[0-9a-f]{48}" if key == "nonce" else r"i-[a-zA-Z0-9]+", value))
            if not valid or value in seen[key]:
                raise ValueError("missing or reused job sequence identity")
            seen[key].add(value)
    if (seen["instance_id"] != {row.get("instance_id") for row in instances} or
            result["gitlab_pipeline_id"] != steps[-1]["pipeline_id"] or result["gitlab_job_id"] != steps[-1]["job_id"]):
        raise ValueError("job sequence differs from the final job or observed workers")


def manager_fenced():
    for field, expected in (("LoadState", "masked"), ("MainPID", "0"), ("ControlGroup", "")):
        result = subprocess.run(["sudo", "-n", "systemctl", "show", "gitlab-runner-aliyun.service", "--property=" + field, "--value"],
                                capture_output=True, text=True, timeout=15)
        if result.returncode or result.stdout.strip() != expected:
            raise ValueError("systemd Manager termination and startup fence are not confirmed")


def driver(fixture, root, manifest, config, gitlab, executor, cidr, binary, scenario="smoke"):
    env = {k: v for k, v in os.environ.items() if k not in
           ("ALIBABA_CLOUD_PROFILE", "ALIBABA_CLOUD_CREDENTIALS_FILE", "ALIBABA_CLOUD_ROLE_ARN",
            "ALIBABA_CLOUD_OIDC_TOKEN_FILE", "ALIBABA_CLOUD_OIDC_PROVIDER_ARN", "ALIYUN_COMPONENT_ENDPOINT",
            "ALIYUN_RESPONSE_GATE_ADDR", "ALIYUN_RESPONSE_GATE_CA", "PLUGIN_REFERENCE")}
    env.update(RUN_LIVE_ACCEPTANCE="1", ACCEPTANCE_MANAGER_MODE="systemd", ACCEPTANCE_DISPOSABLE_HOST="true",
               ACCEPTANCE_EXECUTOR=executor, ACCEPTANCE_JOB_SCENARIO=scenario, ACCEPTANCE_JOB_IMAGE=config["job_image"] if executor == "docker-autoscaler" else "",
               ALIYUN_REGION=config["fixture"]["region"], ALIYUN_SCALING_GROUP_ID=fixture.state["ids"]["group"],
               ACCEPTANCE_PROJECT=fixture.tags["fleeting-project"], MANAGER_CIDR=cidr,
               ALLOWED_INSTANCE_TYPE=config["fixture"]["instance_type"], ALLOWED_IMAGE_ID=config["fixture"]["image"],
               ALLOWED_ZONE_ID=config["fixture"]["zone"], WORKER_USER="root",
               WORKER_SSH_KEY=str((fixture.directory / "worker-key").resolve()),
               PLUGIN_BINARY=str((root / manifest["normal_binary"]).resolve()), PLUGIN_COMMIT=manifest["candidate_sha"],
               PLUGIN_VERSION=manifest["version"], RUNNER_BINARY="/usr/bin/gitlab-runner",
               GITLAB_URL=gitlab["url"], GITLAB_PROJECT_ID=gitlab["project_id"], GITLAB_FIXTURE_REF=gitlab["fixture_ref"],
               GITLAB_FIXTURE_SHA=gitlab["fixture_sha"], GITLAB_EXPECTED_RUNNER_ID=gitlab["runner_id"], GITLAB_RUNNER_TAG=gitlab["runner_tag"],
               GITLAB_API_TOKEN=os.environ["RELEASE_GITLAB_API_TOKEN"], GITLAB_RUNNER_TOKEN=os.environ["RELEASE_GITLAB_RUNNER_TOKEN"],
               ACCEPTANCE_OUTPUT=str((fixture.directory / "evidence").resolve()))
    with (fixture.directory / "driver.log").open("w") as output:
        fixture.state["driver_group"] = "starting"
        fixture.save()
        process = None
        try:
            process = subprocess.Popen([str(binary.resolve()), "smoke"], env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            fixture.state["driver_group"] = process.pid
            fixture.save()
            code = process.wait(timeout=(30 if scenario == "smoke" else 50) * 60)
        finally:
            if process is not None:
                stop_process_group(process)
                if Path("/run/fleeting-acceptance-owner.json").exists():
                    subprocess.run([str(binary.resolve()), "cleanup"], env=env, stdout=output, stderr=subprocess.STDOUT, check=True, timeout=10 * 60)
                    manager_fenced()
                fixture.state["driver_group"] = None
                fixture.save()
    path = Path(env["ACCEPTANCE_OUTPUT"]) / f"run-{env['GITHUB_RUN_ID']}-{env['GITHUB_RUN_ATTEMPT']}" / "result.json"
    result = json.loads(path.read_text())
    if code:
        diagnostic = driver_diagnostic({"result": result.get("result"), "resources": result}, code,
                                       json.dumps(result.get("errors", [])))
        print(json.dumps({"job_diagnostic": diagnostic}))
        raise ValueError("real GitLab job acceptance failed; private diagnostics retained")
    lock = json.loads(Path("upstream.lock.json").read_text())
    check_result(result, manifest, gitlab, executor, lock, env["GITHUB_RUN_ID"], env["GITHUB_RUN_ATTEMPT"], scenario)


def run(args):
    require_linux()
    config, gitlab = configuration()
    manifest = verify(args.root, args.digest, args.sha, args.version)
    gitlab = prepare_gitlab_fixture(gitlab, args.sha, os.environ["RELEASE_GITLAB_API_TOKEN"])
    args.directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    install_package(args.root, manifest, args.phase, args.directory)
    expires = (datetime.now(timezone.utc) + timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
    mode = f"job-{args.phase}-{args.executor}" + ("" if args.scenario == "smoke" else "-" + args.scenario)
    tags = labels(os.environ["GITHUB_REPOSITORY"], args.sha, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"], mode, expires)
    fixture = Fixture(Cloud(config["fixture"]["region"]), args.directory / "fixture", tags)
    address = subprocess.check_output(["curl", "-4", "--fail", "--silent", "--show-error", "--max-time", "15", "https://api.ipify.org"], text=True).strip()
    cidr = str(ipaddress.IPv4Address(address)) + "/32"
    status = cleanup = "FAIL"
    try:
        fixture.prepare(cidr, config["fixture"], worker_script=worker_script(args.executor))
        driver(fixture, args.root, manifest, config, gitlab, args.executor, cidr, args.driver, args.scenario)
        status = "PASS"
    finally:
        try:
            fixture.cleanup()
            cleanup = "PASS"
        finally:
            # Publish only checksums and verdicts, never raw project/runner/cloud
            # identities, logs, configuration, job artifacts or credentials.
            write_json(args.receipt, {"schema_version": 2, "result": status, "cleanup_result": cleanup,
                       "job_scenario": args.scenario, "job_count": len(job_cases(args.scenario)),
                       "candidate_sha": args.sha, "version": args.version, "manifest_sha256": args.digest,
                       "phase": args.phase, "executor": args.executor, "manager_mode": "systemd",
                       "distribution_source": "sealed-candidate" if args.phase == "candidate" else "anonymous-release",
                       "run_id": tags["fleeting-ci-run"], "run_attempt": tags["fleeting-ci-attempt"],
                       "fixture_config_sha256": config_digest(config), "gitlab_config_sha256": config_digest(gitlab),
                       "package_sha256": manifest["files"][package_path(manifest)],
                       "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                       "leftover_resources": 0 if cleanup == "PASS" else -1})


def check_receipt(receipt, manifest, manifest_digest, phase, executor, run_id, attempt, scenario="smoke"):
    expected = {"schema_version": 2, "job_scenario": scenario, "job_count": len(job_cases(scenario)), "result": "PASS", "cleanup_result": "PASS", "leftover_resources": 0,
                "candidate_sha": manifest["candidate_sha"], "version": manifest["version"], "manifest_sha256": manifest_digest,
                "phase": phase, "executor": executor, "manager_mode": "systemd", "run_id": run_id, "run_attempt": attempt,
                "distribution_source": "sealed-candidate" if phase == "candidate" else "anonymous-release",
                "plugin_sha256": manifest["files"][manifest["normal_binary"]], "package_sha256": manifest["files"][package_path(manifest)]}
    if not isinstance(receipt, dict) or any(type(receipt.get(k)) is not type(v) or receipt[k] != v for k, v in expected.items()):
        raise ValueError("job receipt is missing, failed, stale or belongs to another distribution")
    for key in ("fixture_config_sha256", "gitlab_config_sha256"):
        if not isinstance(receipt.get(key), str) or not re.fullmatch(r"[0-9a-f]{64}", receipt[key]):
            raise ValueError("job receipt lacks a configuration binding")


def gate(args):
    manifest = verify(args.root, args.digest, args.sha, args.version)
    paths = list(args.receipts.glob("*.json"))
    expected = {f"{executor}-{scenario}.json" for executor in EXECUTORS for scenario in JOB_SCENARIOS}
    if {p.name for p in paths} != expected:
        raise ValueError("all exact executor/scenario receipts are required")
    configs = set()
    for executor in EXECUTORS:
        for scenario in JOB_SCENARIOS:
            receipt = json.loads((args.receipts / f"{executor}-{scenario}.json").read_text())
            check_receipt(receipt, manifest, args.digest, args.phase, executor, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"], scenario)
            configs.add((receipt["fixture_config_sha256"], receipt["gitlab_config_sha256"]))
    if len(configs) != 1:
        raise ValueError("executor jobs used different fixture configurations")


def main():
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("preflight", "run", "gate"))
    parser.add_argument("--root", type=Path)
    parser.add_argument("--digest")
    parser.add_argument("--sha")
    parser.add_argument("--version")
    parser.add_argument("--phase", choices=PHASES)
    parser.add_argument("--executor", choices=EXECUTORS)
    parser.add_argument("--scenario", choices=JOB_SCENARIOS, default="smoke")
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--receipts", type=Path)
    parser.add_argument("--driver", type=Path, default=Path("bin/aliyun-acceptance"))
    parser.add_argument("--read-only", action="store_true")
    args = parser.parse_args()
    if args.read_only and args.operation != "preflight":
        parser.error("--read-only applies only to configuration preflight")
    if args.operation != "preflight" and any(getattr(args, field) is None for field in ("root", "digest", "sha", "version", "phase")):
        parser.error("run and gate require a complete candidate identity and phase")
    if args.operation == "run" and any(getattr(args, field) is None for field in ("executor", "directory", "receipt")):
        parser.error("run requires an executor and private evidence and public receipt paths")
    if args.operation == "gate" and args.receipts is None:
        parser.error("gate requires the executor receipt directory")
    if args.operation == "preflight":
        configuration(read_only=args.read_only)
    elif args.operation == "gate":
        gate(args)
    else:
        run(args)


if __name__ == "__main__":
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print("Job acceptance failed (" + type(error).__name__ + "); inspect private diagnostics.", file=sys.stderr)
        sys.exit(1)
