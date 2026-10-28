#!/usr/bin/env python3
"""Exercise real dpkg/systemd on a disposable Linux host; never contact providers."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import time

SERVICE = "gitlab-runner-aliyun.service"
CLI = "/usr/bin/fleeting-plugin-aliyun"
CONFIG = Path("/etc/gitlab-runner-aliyun")
HOME = Path("/var/lib/gitlab-runner-aliyun")
RECEIPT = Path("/var/lib/fleeting-plugin-aliyun")
FIXTURE = Path("/var/lib/fleeting-debian-test")


def run(*args, ok=True):
    p = subprocess.run([str(a) for a in args], capture_output=True, text=True, timeout=90)
    if ok and p.returncode:
        raise RuntimeError(f"local command failed: {args[0]}\n{p.stdout}\n{p.stderr}")
    if not ok and p.returncode == 0:
        raise AssertionError(f"command unexpectedly succeeded: {args[0]}")
    return p.stdout.strip()


def wait_for(predicate, description):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise AssertionError("timed out: " + description)


def snapshot():
    return {str(path): (hashlib.sha256(path.read_bytes()).hexdigest(),
                        path.stat().st_uid, path.stat().st_gid, path.stat().st_mode & 0o777)
            for directory in (CONFIG, HOME, RECEIPT) for path in directory.rglob("*") if path.is_file()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--disposable-host", action="store_true", required=True)
    parser.add_argument("--runner", type=Path, required=True)
    parser.add_argument("--packages", type=Path, default=Path("dist"))
    args = parser.parse_args()
    if os.geteuid() != 0 or platform.system() != "Linux" or not Path("/run/systemd/system").is_dir():
        raise SystemExit("requires root on a disposable Linux systemd host")
    runner = args.runner.resolve()
    import json
    lock = json.loads((Path(__file__).resolve().parents[1] / "upstream.lock.json").read_text())
    arch = {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]
    assert hashlib.sha256(runner.read_bytes()).hexdigest() == lock["runner"]["sha256"]["linux-" + arch]
    packages = list(args.packages.resolve().glob(f"fleeting-plugin-aliyun_*_{arch}.deb"))
    if len(packages) != 1:
        raise SystemExit("expected exactly one native-architecture Debian package")
    package = packages[0]
    for deb in args.packages.resolve().glob("fleeting-plugin-aliyun_*.deb"):
        package_arch = run("dpkg-deb", "-f", deb, "Architecture")
        assert package_arch in ("amd64", "arm64")
        with tempfile.TemporaryDirectory(prefix="deb-payload-") as extraction:
            run("dpkg-deb", "-x", deb, extraction)
            payload = Path(extraction)
            expected = args.packages / "packages/linux" / package_arch / "plugin"
            assert hashlib.sha256((payload / "usr/bin/fleeting-plugin-aliyun").read_bytes()).digest() == hashlib.sha256(expected.read_bytes()).digest()
            docs = payload / "usr/share/doc/fleeting-plugin-aliyun"
            assert (docs / "LICENSE").is_file() and (docs / "dependencies.json").is_file()
            assert any((docs / "_licenses").iterdir())
    reserved = [Path(CLI), Path("/usr/bin/gitlab-runner"), CONFIG, HOME, RECEIPT, FIXTURE,
                Path("/usr/lib/systemd/system") / SERVICE, Path("/etc/systemd/system") / SERVICE]
    if any(p.exists() or p.is_symlink() for p in reserved) or subprocess.run(
            ["getent", "passwd", "gitlab-runner-aliyun"], stdout=subprocess.DEVNULL).returncode == 0:
        raise SystemExit("refusing a host with an existing installation or test fixture")
    FIXTURE.mkdir(mode=0o755)
    (FIXTURE / "work").mkdir(mode=0o777)
    (FIXTURE / "work").chmod(0o777)
    shutil.copyfile(runner, FIXTURE / "real-runner")
    (FIXTURE / "real-runner").chmod(0o755)
    # The real Runner parses config. Only its daemon is replaced by a fake busy
    # job, so systemd signal/drain tests cannot contact GitLab or a cloud API.
    wrapper = Path("/usr/bin/gitlab-runner")
    wrapper.write_text('''#!/usr/bin/python3
import os, signal, sys, time
from pathlib import Path
root = Path("/var/lib/fleeting-debian-test")
if len(sys.argv) < 2 or sys.argv[1] != "run":
    os.execv(str(root / "real-runner"), ["gitlab-runner"] + sys.argv[1:])
work = root / "work"
if (work / "fail").exists():
    sys.exit(1)
(work / "started").touch()
def drain(*_):
    (work / "draining").touch()
    while not (work / "finish").exists():
        time.sleep(0.1)
    sys.exit(0)
def aborted(*_):
    (work / "aborted").touch()
    sys.exit(1)
signal.signal(signal.SIGQUIT, drain)
signal.signal(signal.SIGTERM, aborted)
while True:
    time.sleep(0.1)
''')
    wrapper.chmod(0o755)
    try:
        # A local manual installation must never be overwritten by apt.
        local_unit = Path("/etc/systemd/system") / SERVICE
        local_unit.write_text("# unrelated manual installation\n")
        try:
            run("dpkg", "-i", package, ok=False)
            assert local_unit.read_text() == "# unrelated manual installation\n"
        finally:
            local_unit.unlink()
        run("apt-get", "install", "--no-install-recommends", "-y", package)
        run("systemd-analyze", "verify", "/usr/lib/systemd/system/" + SERVICE)
        assert run("systemctl", "show", SERVICE, "--property=ActiveState", "--value") == "inactive"
        run("systemctl", "is-enabled", SERVICE, ok=False)
        with tempfile.TemporaryDirectory(prefix="debian-lifecycle-") as temporary:
            temporary = Path(temporary)
            private = temporary / "private"
            private.mkdir(mode=0o700)
            key = private / "key"
            run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
            root = Path(__file__).resolve().parents[1]
            for mode in ("instance", "docker-autoscaler"):
                # Exercise both examples with the real pinned Runner parser.
                config = private / (mode + ".toml")
                config.write_text((root / "examples" / mode / "config.toml").read_text()
                                  .replace("https://gitlab.com", "https://gitlab.invalid")
                                  .replace("REPLACE_WITH_PROJECT_RUNNER_AUTH_TOKEN", "synthetic-runner-token")
                                  .replace("REPLACE_WITH_DEDICATED_GROUP_ID", "synthetic-scaling-group")
                                  .replace("REPLACE_WITH_PINNED_JOB_IMAGE_DIGEST", "alpine:3.21"))
                config.chmod(0o600)
                # Setup preserves the first pool. Use doctor on a locally edited
                # second executor after testing the complete lifecycle below.
                if mode == "instance":
                    setup = [CLI, "manager", "setup", "--config", config, "--ssh-key", key]
                    run(*setup)
                    before = snapshot()
                    run(*setup)
                    assert snapshot() == before
                else:
                    docker_config = config
            uid = int(run("id", "-u", "gitlab-runner-aliyun"))
            assert CONFIG.stat().st_uid == 0 and CONFIG.stat().st_mode & 0o777 == 0o750
            assert (CONFIG / "config.toml").stat().st_uid == uid
            assert (CONFIG / "manager.env").stat().st_uid == 0
            # The service account cannot replace root's environment or temp paths.
            run("runuser", "-u", "gitlab-runner-aliyun", "--", "touch", CONFIG / "attacker", ok=False)
            os.chdir("/root")  # inaccessible inherited cwd must not break doctor
            run(CLI, "manager", "doctor")
            (CONFIG / "config.toml").chmod(0o644)
            run(CLI, "manager", "start", ok=False)
            (CONFIG / "config.toml").chmod(0o600)
            pending = Path("/run/fleeting-plugin-aliyun-package-operation")
            run("dpkg", "--unpack", package)
            assert pending.exists()
            run(CLI, "manager", "start", ok=False)
            run("dpkg", "--configure", "fleeting-plugin-aliyun")
            assert not pending.exists()
            # Exercise failed-state recovery, without changing the installed
            # service identity or using a drop-in that doctor would reject.
            unit = Path("/usr/lib/systemd/system") / SERVICE
            unit_text = unit.read_text()
            unit.write_text(unit_text.replace("Restart=on-failure", "Restart=no"))
            run("systemctl", "daemon-reload")
            (FIXTURE / "work/fail").touch()
            run(CLI, "manager", "start")
            wait_for(lambda: run("systemctl", "show", SERVICE, "--property=ActiveState", "--value") == "failed", "failed daemon")
            run(CLI, "manager", "stop", ok=False)
            run("dpkg", "-i", package, ok=False)
            unit.write_text(unit_text)
            (FIXTURE / "work/fail").unlink()
            run("systemctl", "daemon-reload")
            run("systemctl", "reset-failed", SERVICE)
            run(CLI, "manager", "start")
            wait_for(lambda: (FIXTURE / "work/started").exists(), "fake job start")
            run(CLI, "manager", "status")
            run("dpkg", "-i", package, ok=False)
            run("dpkg", "--remove", "fleeting-plugin-aliyun", ok=False)
            assert run("systemctl", "show", SERVICE, "--property=ActiveState", "--value") == "active"
            stop = subprocess.Popen([CLI, "manager", "stop"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            wait_for(lambda: (FIXTURE / "work/draining").exists(), "SIGQUIT drain")
            assert "deactivating" in run(CLI, "manager", "status")
            assert stop.poll() is None
            assert not (FIXTURE / "work/aborted").exists()
            (FIXTURE / "work/finish").touch()
            stdout, stderr = stop.communicate(timeout=30)
            assert stop.returncode == 0, stderr
            before = snapshot()
            # Repack the same tested payload at a newer test-only package version.
            unpacked = temporary / "upgrade"
            run("dpkg-deb", "-R", package, unpacked)
            control = unpacked / "DEBIAN/control"
            version = run("dpkg-deb", "-f", package, "Version")
            control.write_text(control.read_text().replace("Version: " + version + "\n", "Version: " + version + ".test.1\n"))
            newer = temporary / "upgrade.deb"
            run("dpkg-deb", "--root-owner-group", "--build", unpacked, newer)
            run("dpkg", "-i", newer)
            assert snapshot() == before
            assert run("systemctl", "show", SERVICE, "--property=ActiveState", "--value") == "inactive"
            run("dpkg", "-i", package)  # rollback
            assert snapshot() == before
            run(CLI, "manager", "doctor")
            run("dpkg", "--remove", "fleeting-plugin-aliyun")
            assert snapshot() == before
            assert not Path(CLI).exists()
            run("dpkg", "-i", package)
            run(CLI, "manager", "doctor")
            # Reconfiguration is explicit: stop, back up/edit, then doctor.
            content = docker_config.read_text().replace(
                'plugin = "ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.2"',
                'plugin = "/usr/bin/fleeting-plugin-aliyun"').replace(
                'key_path = "/run/secrets/worker-key"', 'key_path = "/etc/gitlab-runner-aliyun/worker.key"')
            (CONFIG / "config.toml").write_text(content)
            run(CLI, "manager", "doctor")
            before = snapshot()
            run("dpkg", "--purge", "fleeting-plugin-aliyun")
            assert snapshot() == before
        print("PASS: real package install/reinstall, both configs, ownership, systemd drain, active-operation guards, upgrade/rollback, remove/purge and preserved state; no provider calls")
    finally:
        (FIXTURE / "work/finish").touch()
        # Only paths reserved and verified absent by this test are cleaned up.
        subprocess.run(["systemctl", "stop", SERVICE], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=45)
        subprocess.run(["dpkg", "--purge", "fleeting-plugin-aliyun"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wrapper.unlink(missing_ok=True)
        for directory in (CONFIG, HOME, RECEIPT, FIXTURE):
            shutil.rmtree(directory, ignore_errors=True)
        subprocess.run(["userdel", "gitlab-runner-aliyun"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
