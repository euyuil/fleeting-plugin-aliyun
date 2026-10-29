"""Manually abandon one owned fixture to exercise independent janitor recovery."""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

from cloud_config import config_digest, fixture_config
from cloud_diagnostics import diagnostic_error
from cloud_drill_diagnostics import WriterDiagnostic, read_writer_diagnostic
from cloud_fixture import Cloud, DRILL_MODE, Fixture, labels, preflight, require_linux, stop_process_group, verify_drill_creation
from release_candidate import revision, write_json


def drill_context(confirmed):
    require_linux()
    preflight()
    if (not confirmed or os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch" or
            os.environ.get("GITHUB_WORKFLOW") != "Cloud cleanup drill"):
        raise ValueError("the manual cleanup drill requires explicit abandonment confirmation")
    sha = revision(os.environ["GITHUB_SHA"])
    if subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip() != sha:
        raise ValueError("drill source differs from the dispatched main commit")
    config = fixture_config()
    expires = (datetime.now(timezone.utc) + timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
    tags = labels(os.environ["GITHUB_REPOSITORY"], sha, os.environ["GITHUB_RUN_ID"],
                  os.environ["GITHUB_RUN_ATTEMPT"], DRILL_MODE, expires)
    address = subprocess.check_output(["curl", "-4", "--fail", "--silent", "--show-error", "--max-time", "15", "https://api.ipify.org"], text=True).strip()
    return tags, config, str(ipaddress.IPv4Address(address)) + "/32"


def checkpoint(tags, config):
    return {"schema_version": 1, "mode": DRILL_MODE, "source_sha": tags["fleeting-candidate"],
            "run_id": tags["fleeting-ci-run"], "run_attempt": tags["fleeting-ci-attempt"],
            "fixture_config_sha256": config_digest(config), "workers_observed": 1}


def wait_for_worker(owner, config, diagnostic=None):
    deadline = time.monotonic() + 600
    while time.monotonic() < deadline:
        if diagnostic:
            diagnostic.phase("worker_inventory")
        workers = owner.owned("instance")
        if len(workers) > 1 or len(owner.state["ids"].get("instance", [])) > 1:
            raise ValueError("cleanup drill exceeded its one-worker budget")
        if workers:
            if diagnostic:
                diagnostic.phase("worker_configuration")
            worker = workers[0]
            if any(worker.get(key) != value for key, value in {
                "InstanceType": config["instance_type"], "ImageId": config["image"],
                "ZoneId": config["zone"], "SpotStrategy": "SpotAsPriceGo", "InstanceChargeType": "PostPaid",
            }.items()):
                raise ValueError("cleanup drill worker differs from the configured Spot fixture")
            if worker.get("Status") == "Running":
                if diagnostic:
                    diagnostic.phase("creation_verification")
                if verify_drill_creation(owner, worker["InstanceId"]):
                    return
        time.sleep(3)
    raise RuntimeError("cleanup drill did not observe one running owned worker")


def writer(directory):
    # This subprocess has a normal finally-cleanup path. The supervisor's SIGKILL
    # must bypass it; simply skipping cleanup would not exercise process loss.
    diagnostic = WriterDiagnostic(directory)
    owner = None
    primary_error = cleanup_error = None
    try:
        diagnostic.phase("context")
        context = json.loads((directory / "context.json").read_text())
        tags, config, cidr = context["tags"], context["config"], context["cidr"]
        diagnostic.phase("authentication")
        owner = Fixture(Cloud(config["region"]), directory / "fixture", tags)
        diagnostic.phase("fixture_setup")
        owner.prepare(cidr, config)
        group = owner.state["ids"]["group"]
        token = hashlib.sha256(json.dumps([DRILL_MODE, group, tags], sort_keys=True).encode()).hexdigest()
        diagnostic.phase("scale_request")
        owner.cloud.call("ess", "ScaleWithAdjustment", ScalingGroupId=group,
                         AdjustmentType="TotalCapacity", AdjustmentValue=1, SyncActivity="false", ClientToken=token)
        wait_for_worker(owner, config, diagnostic)
        diagnostic.phase("checkpoint")
        temporary = directory / "checkpoint.tmp"
        write_json(temporary, checkpoint(tags, config))
        temporary.replace(directory / "checkpoint.json")
        diagnostic.phase("await_interruption")
        # If the supervisor disappears without killing us, do not wait forever.
        time.sleep(120)
        raise RuntimeError("cleanup drill supervisor did not interrupt at the checkpoint")
    except BaseException as error:
        primary_error = error
        diagnostic.failure(error)
    finally:
        # Once this exists, the supervisor must not claim it bypassed cleanup.
        # Check only after SIGKILL/exit proof, eliminating the check/cleanup race.
        if owner is not None:
            try:
                (directory / "cleanup-started").touch(exist_ok=False)
                diagnostic.cleanup("RUNNING")
                owner.cleanup()
            except BaseException as error:
                cleanup_error = error
                diagnostic.cleanup("FAIL", error)
            else:
                diagnostic.cleanup("PASS")
    if isinstance(cleanup_error, (KeyboardInterrupt, SystemExit)) and not isinstance(primary_error, (KeyboardInterrupt, SystemExit)):
        raise cleanup_error
    if primary_error is not None:
        raise primary_error
    if cleanup_error is not None:
        raise cleanup_error


def interrupt_at_checkpoint(process, path, expected, timeout=1200):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("cleanup drill writer exited before the interruption checkpoint")
        if path.exists():
            data = json.loads(path.read_text())
            if (not isinstance(data, dict) or data.keys() != expected.keys() or
                    any(type(data[k]) is not type(v) or data[k] != v for k, v in expected.items())):
                raise ValueError("cleanup drill checkpoint does not match this run and fixture")
            stop_process_group(process)
            if process.returncode != -signal.SIGKILL:
                raise RuntimeError("cleanup drill did not prove forced writer termination")
            return
        time.sleep(0.2)
    raise TimeoutError("cleanup drill checkpoint deadline")


def run(directory, receipt, confirmed):
    tags, config, cidr = drill_context(confirmed)
    # Never reuse a previous writer or checkpoint, even when a run is retried.
    directory.mkdir(parents=True, exist_ok=False, mode=0o700)
    write_json(directory / "context.json", {"tags": tags, "config": config, "cidr": cidr})
    expected = checkpoint(tags, config)
    result = {**expected, "result": "FAIL", "workers_observed": 0,
              "writer_terminated": False, "cleanup_result": "UNKNOWN"}
    process = None
    interrupted = False
    writer_stopped = False
    primary_error = cleanup_error = None
    try:
        with (directory / "writer.log").open("w") as output:
            process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "writer", "--directory", str(directory.resolve())],
                                       stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            interrupt_at_checkpoint(process, directory / "checkpoint.json", expected)
            writer_stopped = True
            if (directory / "cleanup-started").exists():
                raise RuntimeError("cleanup drill writer already entered cleanup; interruption is not proven")
        result.update(result="INTERRUPTED", workers_observed=1, writer_terminated=True, cleanup_result="PENDING_INDEPENDENT_RECOVERY")
        interrupted = True
    except BaseException as error:
        primary_error = error
        result["error"] = diagnostic_error(error)
    finally:
        try:
            if not interrupted and process is not None:
                # No fallback deletion until the owned writer is proven stopped.
                if not writer_stopped:
                    stop_process_group(process)
                result["writer_terminated"] = True
                Fixture(Cloud(config["region"], cleanup_only=True), directory / "fixture", tags).cleanup()
                result["cleanup_result"] = "PASS"
        except BaseException as error:
            cleanup_error = error
            result["cleanup_result"] = "FAIL"
            result["cleanup_error"] = diagnostic_error(error)
        finally:
            result["writer"] = read_writer_diagnostic(directory, process.returncode if process is not None else None)
            write_json(receipt, result)
    if isinstance(cleanup_error, (KeyboardInterrupt, SystemExit)) and not isinstance(primary_error, (KeyboardInterrupt, SystemExit)):
        raise cleanup_error
    if primary_error is not None:
        raise primary_error
    if cleanup_error is not None:
        raise cleanup_error
    # The source workflow must fail. Janitor success cannot turn it into a
    # release receipt or hide the intentional interruption.
    return 86


def main():
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("run", "writer"))
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--confirm-abandon-owned-fixture", action="store_true")
    args = parser.parse_args()
    if args.operation == "writer":
        writer(args.directory)
    else:
        if args.receipt is None:
            parser.error("--receipt is required")
        return run(args.directory, args.receipt, args.confirm_abandon_owned_fixture)


if __name__ == "__main__":
    sys.exit(main())
