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
import time
import urllib.request

from cloud_config import config_digest, decode, fixture_config
from cloud_fixture import JOB_SCENARIOS, Cloud, Fixture, labels, preflight, require_linux, stop_process_group
from cloud_diagnostics import api_error, diagnostic_error, driver_diagnostic, duration_ms, source_error_templates
from debian_package import debian_version
from debian_upgrade import baseline, fetch_baseline, require_newer, verify_package
from gitlab_fixture import prepare as prepare_gitlab_fixture
from job_diagnostics import outcome as job_outcome
from manager_diagnostics import manager_diagnostic
from release_candidate import digest, revision, verify, write_json

EXECUTORS = ("instance", "docker-autoscaler")
PHASES = ("candidate", "public")


def selected_cases(value="all"):
    allowed = {f"{executor}/{scenario}" for executor in EXECUTORS for scenario in JOB_SCENARIOS}
    if value == "all":
        return [(executor, scenario) for executor in EXECUTORS for scenario in JOB_SCENARIOS]
    if not isinstance(value, str) or len(value) > 2048 or any(char in value for char in "\r\n\x00"):
        raise ValueError("invalid explicit job case selection")
    cases = [item.strip() for item in value.split(",")]
    if not cases or len(cases) != len(set(cases)) or any(case not in allowed for case in cases):
        raise ValueError("job selection requires unique executor/scenario pairs")
    return [tuple(case.split("/")) for case in cases]


def workflow_cases(value, mode):
    # An explicitly empty manual jobs selector must never expand paid coverage.
    # Reusable release callers have no debug input and retain their full gate.
    if value == "" and mode != "jobs":
        value = "all"
    selected_cases(value)
    if mode != "jobs" and value != "all":
        raise ValueError("targeted job selection is restricted to jobs debug mode")
    return value


def workflow_profile(profile, phase, cases):
    if profile not in ("full", "debug") or phase not in PHASES:
        raise ValueError("unknown job acceptance profile or phase")
    selected = selected_cases(cases)
    if profile == "debug" and phase != "candidate":
        raise ValueError("debug validation is only available before publication")
    if profile == "full" and cases != "all":
        raise ValueError("full validation cannot select partial cases")
    return selected


def job_timing(scenario, now):
    if scenario not in JOB_SCENARIOS:
        raise ValueError("unknown job acceptance scenario")
    budget = 20 if scenario in ("smoke", "upgrade") else 40
    deadline = now + timedelta(minutes=budget)
    expires = (deadline + timedelta(minutes=40)).replace(second=0, microsecond=0)
    return {key: value.strftime("%Y-%m-%dT%H:%M:%SZ") for key, value in
            (("ACCEPTANCE_STARTED_AT", now), ("ACCEPTANCE_DEADLINE_AT", deadline), ("ACCEPTANCE_EXPIRES_AT", expires))}


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


def install_package(root, manifest, phase, directory, scenario="smoke"):
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
    previous = None
    installed = package
    expected_binary = manifest["files"][manifest["normal_binary"]]
    if scenario == "upgrade":
        require_newer(manifest["version"])
        previous = fetch_baseline(directory / "previous", "amd64")
        pin = baseline("amd64")
        verify_package(previous, pin["package_sha256"], pin["version"], "amd64")
        installed, expected_binary = previous, pin["binary_sha256"]
    runner = subprocess.check_output([sys.executable, "tools/fetch-runner.py"], text=True).strip()
    with (directory / "install.log").open("w") as output:
        for command in (["sudo", "-n", "apt-get", "update"],
                        ["sudo", "-n", "apt-get", "install", "-y", "--no-install-recommends", str(installed.resolve())],
                        ["sudo", "-n", "install", "-m", "0755", runner, "/usr/bin/gitlab-runner"]):
            subprocess.run(command, check=True, stdout=output, stderr=subprocess.STDOUT, timeout=300)
    if digest("/usr/bin/fleeting-plugin-aliyun") != expected_binary:
        raise ValueError("installed Debian binary differs from the accepted artifact")
    return package.resolve(), previous.resolve() if previous else None


def job_cases(scenario):
    return ["capacity"] * 3 if scenario == "capacity" else (["smoke"] if scenario in ("smoke", "upgrade") else [scenario, "smoke"])


def upgrade_receipt(scenario):
    pin = baseline("amd64") if scenario == "upgrade" else None
    return {"upgrade_from_version": pin["version"], "upgrade_from_package_sha256": pin["package_sha256"]} if pin else {}


def recovery_policy(scenario):
    return {"manager-crash": "orphan-release-before-finalize-v1", "worker-loss": "observed-worker-loss-v1"}.get(scenario, "standard-v1")


def check_result(result, manifest, gitlab, executor, lock, run_id, attempt, scenario="smoke"):
    cases = job_cases(scenario)
    expected = {"result": "PASS", "cleanup_result": "PASS", "normal_release": scenario != "worker-loss",
                "mode": "smoke", "executor": executor, "manager_mode": "systemd", "job_scenario": scenario,
                "plugin_commit": manifest["candidate_sha"], "plugin_version": manifest["version"],
                "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                "runner_sha256": lock["runner"]["sha256"]["linux-amd64"], "runner_version": lock["runner"]["version"],
                "gitlab_fixture_sha": gitlab["fixture_sha"], "gitlab_project_id": gitlab["project_id"], "gitlab_runner_id": int(gitlab["runner_id"]),
                "github_run_id": run_id, "github_run_attempt": attempt, "instance_limit": 2 if scenario == "capacity" else 1,
                "creation_budget": 3 if scenario == "worker-loss" else len(cases), "evidence_incomplete": False}
    if scenario == "capacity":
        expected["capacity_verified"] = True
    if scenario == "drain":
        expected["drain_verified"] = True
    if scenario == "worker-loss":
        expected.update(worker_loss_verified=True, recovery_policy=recovery_policy(scenario))
    if scenario == "manager-crash":
        expected.update(crash_verified=True, orphan_release_verified=True, recovery_policy=recovery_policy(scenario))
    if not isinstance(result, dict) or any(type(result.get(k)) is not type(v) or result[k] != v for k, v in expected.items()):
        raise ValueError("job did not produce a passing bound systemd acceptance result")
    if scenario == "upgrade":
        pin = baseline("amd64")
        expected_upgrade = {"version": pin["version"], "package_sha256": pin["package_sha256"],
                            "candidate_package_sha256": manifest["files"][package_path(manifest)], "state_preserved": True, "rollback_verified": True}
        proof = result.get("upgrade")
        if not isinstance(proof, dict) or any(type(proof.get(k)) is not type(v) or proof[k] != v for k, v in expected_upgrade.items()):
            raise ValueError("job lacks the pinned previous-release upgrade and rollback proof")
    if any(type(result.get(k)) is not int or result[k] <= 0 for k in ("gitlab_pipeline_id", "gitlab_job_id")):
        raise ValueError("job evidence lacks concrete pipeline and job identities")
    replacement = result.get("worker_loss_replacement_id", "")
    if not isinstance(replacement, str) or (replacement and (scenario != "worker-loss" or not re.fullmatch(r"i-[a-zA-Z0-9]+", replacement))):
        raise ValueError("unexpected worker-loss replacement identity")
    instances = result.get("instances")
    if (not isinstance(instances, list) or len(instances) != len(cases) + bool(replacement) or "leftover_resources" not in result or result["leftover_resources"] or result.get("errors")
            or any(not isinstance(row, dict) or row.get("unsafe_disk_policy", False) is not False or
                   any(row.get(k) is not True for k in ("spot_verified", "inventory_complete", "deletion_verified")) for row in instances)):
        raise ValueError("job evidence lacks the exact Spot worker and deletion proof")
    steps = result.get("jobs")
    if not isinstance(steps, list) or len(steps) != len(cases) or type(result.get("manager_pid")) is not int or result["manager_pid"] <= 0:
        raise ValueError("job sequence or persistent Manager identity is missing")
    seen = {k: set() for k in ("pipeline_id", "job_id", "nonce", "instance_id")}
    for index, (step, case) in enumerate(zip(steps, cases)):
        pid = step.get("manager_pid") if isinstance(step, dict) else None
        manager_matches = type(pid) is int and pid > 0 and (pid != result["manager_pid"] if scenario in ("drain", "manager-crash") and index == 0 else pid == result["manager_pid"])
        injected = scenario == "worker-loss" and index == 0
        status, reason = {"smoke": ("success", ""), "capacity": ("success", ""), "drain": ("success", ""), "failure": ("failed", "script_failure"),
                          "worker-loss": ("failed", "runner_system_failure"),
                          "manager-crash": ("canceled", None),
                          "cancel": ("canceled", None), "timeout": ("failed", "job_execution_timeout")}[case]
        if isinstance(step, dict) and injected and executor == "instance" and step.get("failure_reason") == "script_failure":
            reason = "script_failure"
        if (not isinstance(step, dict) or step.get("scenario") != case or step.get("status") != status or
                (reason is not None and step.get("failure_reason") != reason) or step.get("normal_release") is not (not injected) or
                step.get("loss_injected", False) is not injected or step.get("loss_observed", False) is not injected or (injected and step.get("control_released") is not False) or
                step.get("crash_injected", False) is not (case == "manager-crash") or (case == "manager-crash" and step.get("control_released") is not False) or
                (step.get("ssh_transport_loss", False) is not False and (not injected or executor != "instance" or step["ssh_transport_loss"] is not True)) or
                not manager_matches or
                step.get("cancel_requested") is not (case in ("cancel", "manager-crash")) or
                step.get("finalization_source", "") != ("harness_after_recovery" if case == "manager-crash" else "") or
                (step.get("force_cancel_requested", False) is not False and (case != "manager-crash" or step["force_cancel_requested"] is not True)) or
                (case != "smoke" and step.get("checkpoint") is not True) or
                (case in ("capacity", "drain") and step.get("control_released") is not True)):
            raise ValueError("unexpected job outcome, restart or incomplete normal release")
        for key in seen:
            value = step.get(key)
            valid = (type(value) is int and value > 0) if key.endswith("_id") and key != "instance_id" else (
                isinstance(value, str) and re.fullmatch(r"[0-9a-f]{48}" if key == "nonce" else r"i-[a-zA-Z0-9]+", value))
            if not valid or value in seen[key]:
                raise ValueError("missing or reused job sequence identity")
            seen[key].add(value)
    if replacement in seen["instance_id"]:
        raise ValueError("recovery job reused a drained replacement")
    expected_workers = seen["instance_id"] | ({replacement} if replacement else set())
    if (expected_workers != {row.get("instance_id") for row in instances} or
            result["gitlab_pipeline_id"] != steps[-1]["pipeline_id"] or result["gitlab_job_id"] != steps[-1]["job_id"]):
        raise ValueError("job sequence differs from the final job or observed workers")


def manager_fenced():
    for field, expected in (("LoadState", "masked"), ("MainPID", "0"), ("ControlGroup", "")):
        result = subprocess.run(["sudo", "-n", "systemctl", "show", "gitlab-runner-aliyun.service", "--property=" + field, "--value"],
                                capture_output=True, text=True, timeout=15)
        if result.returncode or result.stdout.strip() != expected:
            raise ValueError("systemd Manager termination and startup fence are not confirmed")


FAILURE_STAGES = {"preflight", "manager_prepare", "job_sequence", "manager_stop", "resource_cleanup", "evidence_write"}
FAILURE_KINDS = {"unknown", "canceled", "deadline", "cloud_api", "gitlab_http", "unexpected_eof", "eof", "json_decode", "filesystem"}
GITLAB_OPERATIONS = {"runner_verify", "runner_details", "project_runners", "project_jobs", "fixture_commit", "pipeline_create",
                     "pipeline_status", "pipeline_jobs", "pipeline_cancel", "job_cancel", "job_trace", "job_artifact"}


def failure_diagnostics(result):
    if "failures_omitted" not in result:
        return {"evidence": "NOT_RECORDED"}
    values, omitted = result.get("failures", []), result["failures_omitted"]
    if not isinstance(values, list) or type(omitted) is not int or not 0 <= omitted <= 1_000_000:
        return {"evidence": "INVALID"}
    errors = result.get("errors", [])
    errors = errors if isinstance(errors, list) else []
    records = []
    for index, value in enumerate(values[:8]):
        value = value if isinstance(value, dict) else {}
        def choice(field, allowed):
            raw = value.get(field)
            return raw if isinstance(raw, str) and raw in allowed else "unknown"
        job_index, status = value.get("job_index"), value.get("http_status")
        kind = choice("kind", FAILURE_KINDS)
        cloud = api_error(value.get("service"), value.get("operation"), value.get("cloud_code"))
        service = choice("service", {"ecs", "ess", "sts", "gitlab"})
        records.append({"stage": choice("stage", FAILURE_STAGES), "kind": kind,
                        "job_index": job_index if type(job_index) is int and 1 <= job_index <= 3 else None,
                        "service": service,
                        "operation": choice("operation", GITLAB_OPERATIONS) if service == "gitlab" else cloud["action"],
                        "method": choice("method", {"GET", "POST", "PUT", "PATCH", "DELETE"}),
                        "http_status": status if kind == "gitlab_http" and type(status) is int and 100 <= status <= 599 else None,
                        "cloud_code": cloud["code"] if kind == "cloud_api" else None,
                        "matched_error_templates": source_error_templates(errors[index])
                        if index < len(errors) and isinstance(errors[index], str) else []})
    return {"evidence": "RECORDED", "records": records, "unknown_count": sum(row["kind"] == "unknown" for row in records),
            "omitted_count": min(1_000_000, omitted + max(0, len(values) - 8))}


def result_diagnostic(result, code):
    diagnostic = driver_diagnostic({"result": result.get("result"), "resources": result}, code,
                                   json.dumps(result.get("errors", [])))
    jobs = result.get("jobs")
    diagnostic["jobs"] = [job_outcome(value) for value in jobs[:3]] if isinstance(jobs, list) else []
    diagnostic["failures"] = failure_diagnostics(result)
    diagnostic["manager"] = manager_diagnostic(result)
    diagnostic["recovery_policy"] = result.get("recovery_policy") if result.get("recovery_policy") in {recovery_policy("manager-crash"), recovery_policy("worker-loss")} else None
    diagnostic["orphan_release_verified"] = result.get("orphan_release_verified") if type(result.get("orphan_release_verified")) is bool else None
    return diagnostic


def saved_driver_diagnostic(fixture, run, attempt):
    path = fixture.directory / "evidence" / f"run-{run}-{attempt}" / "result.json"
    try:
        result = json.loads(path.read_text())
    except (OSError, ValueError):
        return {"evidence": "NOT_RECORDED"}
    if not isinstance(result, dict):
        return {"evidence": "INVALID"}
    return {"evidence": "RECORDED", **result_diagnostic(result, fixture.driver_exit_code)}


def fixture_operations(fixture):
    operations = fixture.state.get("operations", {})
    if not isinstance(operations, dict):
        return {}
    return {kind: operations[kind] if operations[kind] in ("unknown", "accepted") else "invalid"
            for kind in ("vpc", "vswitch", "sg", "key", "template", "group") if kind in operations}


def driver(fixture, root, manifest, config, gitlab, executor, cidr, binary, scenario="smoke", packages=None):
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
    env.update(fixture.state["job_timing"])
    remaining = (datetime.strptime(env["ACCEPTANCE_DEADLINE_AT"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
                 - datetime.now(timezone.utc)).total_seconds()
    if remaining <= 0:
        raise ValueError("fixed job acceptance execution deadline expired during setup")
    if scenario == "upgrade":
        if packages is None or packages[1] is None:
            raise ValueError("upgrade requires both verified Debian artifacts")
        env.update(ACCEPTANCE_CANDIDATE_PACKAGE=str(packages[0]), ACCEPTANCE_PREVIOUS_PACKAGE=str(packages[1]),
                   ACCEPTANCE_CANDIDATE_PACKAGE_SHA256=manifest["files"][package_path(manifest)])
    with (fixture.directory / "driver.log").open("w") as output:
        fixture.state["driver_group"] = "starting"
        fixture.save()
        process = None
        try:
            process = subprocess.Popen([str(binary.resolve()), "smoke"], env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            fixture.state["driver_group"] = process.pid
            fixture.save()
            code = process.wait(timeout=remaining + 10 * 60)
            fixture.driver_exit_code = code
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
        print(json.dumps({"job_diagnostic": result_diagnostic(result, code)}))
        raise ValueError("real GitLab job acceptance failed; private diagnostics retained")
    lock = json.loads(Path("upstream.lock.json").read_text())
    check_result(result, manifest, gitlab, executor, lock, env["GITHUB_RUN_ID"], env["GITHUB_RUN_ATTEMPT"], scenario)


def run(args):
    require_linux()
    config, gitlab = configuration()
    manifest = verify(args.root, args.digest, args.sha, args.version)
    if args.scenario == "upgrade":
        require_newer(manifest["version"])
    gitlab = prepare_gitlab_fixture(gitlab, args.sha, os.environ["RELEASE_GITLAB_API_TOKEN"])
    args.directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    packages = install_package(args.root, manifest, args.phase, args.directory, args.scenario)
    timing = job_timing(args.scenario, datetime.now(timezone.utc))
    expires = timing["ACCEPTANCE_EXPIRES_AT"]
    mode = f"job-{args.phase}-{args.executor}" + ("" if args.scenario == "smoke" else "-" + args.scenario)
    tags = labels(os.environ["GITHUB_REPOSITORY"], args.sha, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"], mode, expires)
    fixture = Fixture(Cloud(config["fixture"]["region"]), args.directory / "fixture", tags)
    fixture.state["job_timing"] = timing
    fixture.save()
    address = subprocess.check_output(["curl", "-4", "--fail", "--silent", "--show-error", "--max-time", "15", "https://api.ipify.org"], text=True).strip()
    cidr = str(ipaddress.IPv4Address(address)) + "/32"
    status = cleanup = "FAIL"
    diagnostics = {"fixture_setup": {"result": "UNKNOWN"}, "driver": {"result": "UNKNOWN"},
                   "fixture_cleanup": {"result": "UNKNOWN"}}
    primary_error = cleanup_error = None
    stage, started = "fixture_setup", time.monotonic()
    try:
        fixture.prepare(cidr, config["fixture"], worker_script=worker_script(args.executor))
        diagnostics[stage] = {"result": "PASS", "duration_ms": duration_ms(started)}
        stage, started = "driver", time.monotonic()
        driver(fixture, args.root, manifest, config, gitlab, args.executor, cidr, args.driver, args.scenario, packages)
        status = "PASS"
        diagnostics[stage] = {"result": "PASS", "duration_ms": duration_ms(started)}
    except BaseException as error:
        primary_error = error
        diagnostics[stage] = {"result": "FAIL", "duration_ms": duration_ms(started),
                              "error": diagnostic_error(error), "operations": fixture_operations(fixture)}
    finally:
        started = time.monotonic()
        try:
            fixture.cleanup()
            cleanup = "PASS"
            diagnostics["fixture_cleanup"] = {"result": "PASS", "duration_ms": duration_ms(started)}
        except BaseException as error:
            cleanup_error = error
            diagnostics["fixture_cleanup"] = {"result": "FAIL", "duration_ms": duration_ms(started),
                                               "error": diagnostic_error(error), "operations": fixture_operations(fixture)}
        diagnostics["driver"]["evidence"] = saved_driver_diagnostic(fixture, tags["fleeting-ci-run"], tags["fleeting-ci-attempt"])
        # Keep both failures observable even if receipt persistence fails. Public
        # diagnostics contain only allowlisted facts, never raw state or errors.
        print(json.dumps({"job_orchestration_diagnostic": diagnostics}), flush=True)
        write_json(args.receipt, {"schema_version": 3, "validation_profile": getattr(args, "profile", "full"),
                   "recovery_policy": recovery_policy(args.scenario), "result": status, "cleanup_result": cleanup,
                   "job_scenario": args.scenario, "job_count": len(job_cases(args.scenario)),
                   "candidate_sha": args.sha, "version": args.version, "manifest_sha256": args.digest,
                   "phase": args.phase, "executor": args.executor, "manager_mode": "systemd",
                   "distribution_source": "sealed-candidate" if args.phase == "candidate" else "anonymous-release",
                   "run_id": tags["fleeting-ci-run"], "run_attempt": tags["fleeting-ci-attempt"],
                   "fixture_config_sha256": config_digest(config), "gitlab_config_sha256": config_digest(gitlab),
                   "package_sha256": manifest["files"][package_path(manifest)],
                   "plugin_sha256": manifest["files"][manifest["normal_binary"]],
                   "leftover_resources": 0 if cleanup == "PASS" else -1, "diagnostics": diagnostics, **upgrade_receipt(args.scenario)})
    if isinstance(cleanup_error, (KeyboardInterrupt, SystemExit)) and not isinstance(primary_error, (KeyboardInterrupt, SystemExit)):
        raise cleanup_error
    if primary_error is not None:
        raise primary_error
    if cleanup_error is not None:
        raise cleanup_error


def check_receipt(receipt, manifest, manifest_digest, phase, executor, run_id, attempt, scenario="smoke", profile="full"):
    expected = {"schema_version": 3, "validation_profile": profile, "recovery_policy": recovery_policy(scenario), "job_scenario": scenario, "job_count": len(job_cases(scenario)), "result": "PASS", "cleanup_result": "PASS", "leftover_resources": 0,
                "candidate_sha": manifest["candidate_sha"], "version": manifest["version"], "manifest_sha256": manifest_digest,
                "phase": phase, "executor": executor, "manager_mode": "systemd", "run_id": run_id, "run_attempt": attempt,
                "distribution_source": "sealed-candidate" if phase == "candidate" else "anonymous-release",
                "plugin_sha256": manifest["files"][manifest["normal_binary"]], "package_sha256": manifest["files"][package_path(manifest)], **upgrade_receipt(scenario)}
    if not isinstance(receipt, dict) or any(type(receipt.get(k)) is not type(v) or receipt[k] != v for k, v in expected.items()):
        raise ValueError("job receipt is missing, failed, stale or belongs to another distribution")
    for key in ("fixture_config_sha256", "gitlab_config_sha256"):
        if not isinstance(receipt.get(key), str) or not re.fullmatch(r"[0-9a-f]{64}", receipt[key]):
            raise ValueError("job receipt lacks a configuration binding")


def validate_receipts(args, cases, profile="full"):
    manifest = verify(args.root, args.digest, args.sha, args.version)
    paths = list(args.receipts.glob("*.json"))
    expected = {f"{executor}-{scenario}.json" for executor, scenario in cases}
    if {p.name for p in paths} != expected:
        raise ValueError("all exact selected executor/scenario receipts are required")
    configs = set()
    for executor, scenario in cases:
        receipt = json.loads((args.receipts / f"{executor}-{scenario}.json").read_text())
        check_receipt(receipt, manifest, args.digest, args.phase, executor, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"], scenario, profile)
        configs.add((receipt["fixture_config_sha256"], receipt["gitlab_config_sha256"]))
    if len(configs) != 1:
        raise ValueError("executor jobs used different fixture configurations")


def gate(args):
    # Release callers always require all eighteen cases, regardless of any debug
    # selector supplied by an interactive caller or an earlier workflow step.
    validate_receipts(args, selected_cases())


def debug_gate(args):
    if args.phase != "candidate":
        raise ValueError("debug receipts cannot validate a public release")
    validate_receipts(args, selected_cases(args.cases), "debug")


def acceptance_summary(args):
    full = args.operation == "gate"
    selected = selected_cases() if full else selected_cases(args.cases)
    keys = ["/".join(case) for case in selected]
    binding = json.loads((args.receipts / ("-".join(selected[0]) + ".json")).read_text())
    return {"schema_version": 2, "result": "PASS", "scope": "full-suite" if full else "selected-cases",
            "validation_profile": "full" if full else "debug", "receipt_schema_version": 3,
            "recovery_policies": {scenario: recovery_policy(scenario) for _, scenario in selected},
            "fixture_config_sha256": binding["fixture_config_sha256"], "gitlab_config_sha256": binding["gitlab_config_sha256"],
            "full_suite": full, "release_ready": full, "candidate_sha": args.sha, "version": args.version,
            "manifest_sha256": args.digest, "phase": args.phase, "run_id": os.environ["GITHUB_RUN_ID"],
            "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"], "selected_cases": keys,
            "omitted_cases": ["/".join(case) for case in selected_cases() if case not in selected]}


def main():
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("preflight", "run", "gate", "debug-gate"))
    parser.add_argument("--root", type=Path)
    parser.add_argument("--digest")
    parser.add_argument("--sha")
    parser.add_argument("--version")
    parser.add_argument("--phase", choices=PHASES)
    parser.add_argument("--profile", choices=("full", "debug"), default="full")
    parser.add_argument("--executor", choices=EXECUTORS)
    parser.add_argument("--scenario", choices=JOB_SCENARIOS, default="smoke")
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--receipts", type=Path)
    parser.add_argument("--cases", default="all", help="debug only: all or comma-separated executor/scenario pairs")
    parser.add_argument("--summary", type=Path)
    parser.add_argument("--driver", type=Path, default=Path("bin/aliyun-acceptance"))
    parser.add_argument("--read-only", action="store_true")
    args = parser.parse_args()
    if args.operation == "gate" and args.profile != "full":
        parser.error("the complete gate requires the full validation profile")
    if args.cases != "all" and args.operation != "debug-gate":
        parser.error("only debug-gate accepts a selected-case list; the full gate always requires all cases")
    if args.read_only and args.operation != "preflight":
        parser.error("--read-only applies only to configuration preflight")
    if args.operation != "preflight" and any(getattr(args, field) is None for field in ("root", "digest", "sha", "version", "phase")):
        parser.error("run and gate require a complete candidate identity and phase")
    if args.operation == "run" and any(getattr(args, field) is None for field in ("executor", "directory", "receipt")):
        parser.error("run requires an executor and private evidence and public receipt paths")
    if args.operation == "run":
        workflow_profile(args.profile, args.phase, "all" if args.profile == "full" else args.executor + "/" + args.scenario)
    if args.operation in ("gate", "debug-gate") and args.receipts is None:
        parser.error("gate requires the executor receipt directory")
    if args.operation == "preflight":
        configuration(read_only=args.read_only)
        if args.version:
            require_newer(args.version)
    elif args.operation in ("gate", "debug-gate"):
        (gate if args.operation == "gate" else debug_gate)(args)
        if args.summary:
            write_json(args.summary, acceptance_summary(args))
    else:
        run(args)


if __name__ == "__main__":
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print("Job acceptance failed (" + type(error).__name__ + "); inspect private diagnostics.", file=sys.stderr)
        sys.exit(1)
