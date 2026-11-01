"""Reuse a completed full candidate without rebuilding its publication bytes."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

from cloud_config import config_digest, decode
from gitlab_fixture import prepare
from job_acceptance import check_receipt, gitlab_configuration, selected_cases
from public_release import PLATFORMS, check_candidate_summary, require_fields
from release_candidate import MODES, digest, revision, verify, write_json


def number(value):
    if not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]{0,18}", value):
        raise ValueError("an exact workflow run and attempt are required")
    return value


def github(path):
    result = subprocess.run(["gh", "api", path], capture_output=True, text=True, timeout=45)
    if result.returncode:
        raise ValueError("cannot verify the accepted GitHub run or artifacts")
    return json.loads(result.stdout)


def resolve(run_id, attempt, version, read=github):
    number(run_id)
    number(attempt)
    repository = os.environ["GITHUB_REPOSITORY"]
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid publication repository")
    base = f"repos/{repository}/actions/runs/{run_id}"
    latest = read(base)
    run = read(base + "/attempts/" + attempt)
    for value in (latest, run):
        require_fields(value, {"id": int(run_id), "run_attempt": int(attempt), "status": "completed", "conclusion": "success",
                               "event": "workflow_dispatch", "path": ".github/workflows/cloud-acceptance.yml", "head_branch": "main"})
        if value.get("repository", {}).get("full_name") != repository or value.get("head_repository", {}).get("full_name") != repository:
            raise ValueError("accepted run belongs to another repository")
    sha = run["head_sha"]
    revision(sha)
    if latest["head_sha"] != sha:
        raise ValueError("accepted attempt source changed")
    subprocess.run(["git", "merge-base", "--is-ancestor", sha, "HEAD"], check=True, capture_output=True)
    jobs = read(base + "/attempts/" + attempt + "/jobs?per_page=100")
    required_jobs = {"gate", *(f"scenario ({mode})" for mode in MODES),
                     *(f"platforms / install ({runner}, {arch})" for runner, arch in PLATFORMS)}
    job_names = [job.get("name") for job in jobs.get("jobs", [])]
    if (jobs.get("total_count") != len(jobs.get("jobs", [])) or not jobs["jobs"] or
            len(set(job_names)) != len(job_names) or not required_jobs.issubset(job_names) or
            any(job.get("status") != "completed" or job.get("conclusion") != "success" for job in jobs["jobs"])):
        raise ValueError("accepted workflow has missing, failed, cancelled or skipped jobs")
    for job in jobs["jobs"]:
        require_fields(job, {"run_id": int(run_id), "run_attempt": int(attempt), "head_sha": sha})
    inventory = read(base + "/artifacts?per_page=100")
    entries = inventory.get("artifacts", [])
    if inventory.get("total_count") != len(entries) or len({a["name"] for a in entries}) != len(entries):
        raise ValueError("accepted artifact inventory is incomplete or ambiguous")
    artifacts = {a["name"]: a for a in entries}
    names = [f"cloud-candidate-{run_id}-{attempt}", f"job-summary-{run_id}-{attempt}-candidate"]
    names += [f"job-receipt-{run_id}-{attempt}-candidate-{executor}-{case}" for executor, case in selected_cases()]
    selected = []
    for name in names:
        artifact = artifacts.get(name)
        if (not isinstance(artifact, dict) or artifact.get("expired") is not False or
                type(artifact.get("id")) is not int or artifact["id"] <= 0 or
                not re.fullmatch(r"sha256:[0-9a-f]{64}", artifact.get("digest", "")) or
                artifact.get("workflow_run", {}).get("id") != int(run_id) or
                artifact["workflow_run"].get("head_sha") != sha):
            raise ValueError("accepted candidate or exact case artifact is missing, expired or foreign")
        selected.append(artifact)
    return {"schema_version": 1, "repository": repository, "accepted_run_id": run_id, "accepted_run_attempt": attempt,
            "candidate_sha": sha, "version": version, "original_artifact_id": selected[0]["id"],
            "original_artifact_digest": selected[0]["digest"], "summary_artifact_id": selected[1]["id"],
            "receipt_artifact_ids": ",".join(str(a["id"]) for a in selected[2:])}


def runtime_configuration(source, sha, run_id, attempt):
    config = gitlab_configuration(decode("RELEASE_GITLAB_CONFIG", os.environ))
    actual = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=source, text=True).strip()
    if actual != sha:
        raise ValueError("runtime fixture templates are not the accepted source")
    with contextlib.chdir(source):
        runtime = prepare(config, sha, os.environ["RELEASE_GITLAB_API_TOKEN"], run_id=run_id, attempt=attempt, read_only=True)
    return config_digest(config), config_digest(runtime)


def accepted_payload(resolution, root, summary, receipts, source, archive, destination):
    candidate = json.loads(summary.read_text())
    manifest_digest = candidate.get("manifest_sha256", "")
    if not re.fullmatch(r"[0-9a-f]{64}", manifest_digest):
        raise ValueError("accepted summary lacks its manifest digest")
    identity = {key: resolution[key] for key in ("candidate_sha", "version")}
    identity.update(manifest_sha256=manifest_digest, run_id=resolution["accepted_run_id"], run_attempt=resolution["accepted_run_attempt"])
    check_candidate_summary(candidate, identity)
    root.mkdir(parents=True, exist_ok=False)
    with tarfile.open(archive) as bundle:
        bundle.extractall(root, filter="data")
    manifest = verify(root, manifest_digest, identity["candidate_sha"], identity["version"])
    expected = {f"{executor}-{case}.json" for executor, case in selected_cases()}
    if {p.name for p in receipts.glob("*.json")} != expected:
        raise ValueError("all eighteen exact accepted receipts are required")
    hashes = {}
    for executor, case in selected_cases():
        path = receipts / f"{executor}-{case}.json"
        receipt = json.loads(path.read_text())
        check_receipt(receipt, manifest, manifest_digest, "candidate", executor, identity["run_id"], identity["run_attempt"], case, "full")
        require_fields(receipt, {key: candidate[key] for key in ("fixture_config_sha256", "gitlab_config_sha256")})
        hashes[f"{executor}/{case}"] = digest(path)
    configured, runtime = runtime_configuration(source, identity["candidate_sha"], identity["run_id"], identity["run_attempt"])
    fixture = config_digest(decode("RELEASE_ALIYUN_JOB_FIXTURE_CONFIG", os.environ))
    if runtime != candidate["gitlab_config_sha256"] or fixture != candidate["fixture_config_sha256"]:
        raise ValueError("current configuration differs from the accepted full run")
    destination.mkdir(parents=True, exist_ok=False)
    shutil.copyfile(archive, destination / "candidate.tar")
    if digest(archive) != digest(destination / "candidate.tar"):
        raise ValueError("reused candidate tar bytes changed")
    shutil.copyfile(summary, destination / "candidate-summary.json")
    proof = {**resolution, "result": "PASS", "scope": "accepted-candidate-reuse", "manifest_sha256": manifest_digest,
             "candidate_tar_sha256": digest(archive), "fixture_config_sha256": fixture,
             "configured_gitlab_config_sha256": configured, "accepted_gitlab_config_sha256": runtime,
             "publication_run_id": number(os.environ["GITHUB_RUN_ID"]), "publication_run_attempt": number(os.environ["GITHUB_RUN_ATTEMPT"]),
             "receipt_sha256": hashes}
    write_json(destination / "reuse-proof.json", proof)
    return proof


def configuration_proof(reuse, summary, public_receipts, source):
    candidate = json.loads(summary.read_text())
    run, attempt = number(os.environ["GITHUB_RUN_ID"]), number(os.environ["GITHUB_RUN_ATTEMPT"])
    require_fields(reuse, {"schema_version": 1, "result": "PASS", "scope": "accepted-candidate-reuse",
                           "publication_run_id": run, "publication_run_attempt": attempt,
                           **{key: candidate[key] for key in ("candidate_sha", "version", "manifest_sha256", "fixture_config_sha256")},
                           "accepted_run_id": candidate["run_id"], "accepted_run_attempt": candidate["run_attempt"],
                           "accepted_gitlab_config_sha256": candidate["gitlab_config_sha256"]})
    configured, accepted = runtime_configuration(source, candidate["candidate_sha"], candidate["run_id"], candidate["run_attempt"])
    configured_public, public = runtime_configuration(source, candidate["candidate_sha"], run, attempt)
    fixture = config_digest(decode("RELEASE_ALIYUN_JOB_FIXTURE_CONFIG", os.environ))
    if configured != configured_public or configured != reuse["configured_gitlab_config_sha256"] or accepted != candidate["gitlab_config_sha256"] or fixture != candidate["fixture_config_sha256"]:
        raise ValueError("accepted or publication configuration changed")
    for path in public_receipts.glob("*.json"):
        require_fields(json.loads(path.read_text()), {"fixture_config_sha256": fixture, "gitlab_config_sha256": public})
    return {"schema_version": 1, "result": "PASS", "scope": "runtime-config-binding",
            **{key: candidate[key] for key in ("candidate_sha", "version", "manifest_sha256")},
            "run_id": run, "run_attempt": attempt, "accepted_run_id": candidate["run_id"], "accepted_run_attempt": candidate["run_attempt"],
            "configured_gitlab_config_sha256": configured, "accepted_gitlab_config_sha256": accepted,
            "public_gitlab_config_sha256": public, "fixture_config_sha256": fixture}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("resolve", "verify", "config-proof"))
    for name in ("run", "attempt", "version"):
        parser.add_argument("--" + name)
    for name in ("resolution", "root", "summary", "receipts", "source", "archive", "destination", "proof", "receipt"):
        parser.add_argument("--" + name, type=Path)
    args = parser.parse_args()
    required = {"resolve": ("run", "attempt", "version", "receipt"),
                "verify": ("resolution", "root", "summary", "receipts", "source", "archive", "destination"),
                "config-proof": ("proof", "summary", "receipts", "source", "receipt")}[args.operation]
    if any(getattr(args, key) is None for key in required):
        parser.error("missing required " + args.operation + " argument")
    if args.operation == "resolve":
        result = resolve(args.run, args.attempt, args.version)
        write_json(args.receipt, result)
        outputs = {"candidate_sha": result["candidate_sha"], "artifact_id": str(result["original_artifact_id"]),
                   "summary_artifact_id": str(result["summary_artifact_id"]), "receipt_artifact_ids": result["receipt_artifact_ids"]}
    elif args.operation == "verify":
        result = accepted_payload(json.loads(args.resolution.read_text()), args.root, args.summary, args.receipts, args.source, args.archive, args.destination)
        outputs = {"candidate_sha": result["candidate_sha"], "manifest_sha256": result["manifest_sha256"], "release_ready": "true"}
    else:
        result = configuration_proof(json.loads(args.proof.read_text()), args.summary, args.receipts, args.source)
        write_json(args.receipt, result)
        outputs = {}
    if outputs:
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as stream:
            stream.write("".join(f"{key}={value}\n" for key, value in outputs.items()))


if __name__ == "__main__":
    main()
