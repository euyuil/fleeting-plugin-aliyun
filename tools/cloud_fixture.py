"""Own short-lived, explicitly labelled pre-release fixtures in a separate account."""
import argparse
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import tarfile
import tempfile
import time

from release_candidate import MODES, digest, revision, verify, write_json
from cloud_diagnostics import (CloudOperationError, diagnostic_error, duration_ms,
                               driver_diagnostic, matching_error_templates as matching_error_templates,
                               read_driver_diagnostic)
from cloud_config import cleanup_regions, fixture_config, config_digest

PURPOSE = "pre-release-acceptance"
DRILL_MODE = "cleanup-drill"
JOB_SCENARIOS = ("smoke", "failure", "cancel", "timeout", "capacity", "drain", "worker-loss")
JOB_MODES = tuple(f"job-{phase}-{executor}" + ("" if scenario == "smoke" else "-" + scenario)
                  for phase in ("candidate", "public") for executor in ("instance", "docker-autoscaler") for scenario in JOB_SCENARIOS)
SPECS = {
    "vpc": ("vpc", "DescribeVpcs", "Vpcs", "Vpc", "VpcId", "VpcName"),
    "vswitch": ("vpc", "DescribeVSwitches", "VSwitches", "VSwitch", "VSwitchId", "VSwitchName"),
    "sg": ("ecs", "DescribeSecurityGroups", "SecurityGroups", "SecurityGroup", "SecurityGroupId", "SecurityGroupName"),
    "key": ("ecs", "DescribeKeyPairs", "KeyPairs", "KeyPair", "KeyPairName", "KeyPairName"),
    "template": ("ecs", "DescribeLaunchTemplates", "LaunchTemplateSets", "LaunchTemplateSet", "LaunchTemplateId", "LaunchTemplateName"),
    "group": ("ess", "DescribeScalingGroups", "ScalingGroups", "ScalingGroup", "ScalingGroupId", "ScalingGroupName"),
    "instance": ("ecs", "DescribeInstances", "Instances", "Instance", "InstanceId", "InstanceName"),
    "disk": ("ecs", "DescribeDisks", "Disks", "Disk", "DiskId", "DiskName"),
    "eni": ("ecs", "DescribeNetworkInterfaces", "NetworkInterfaceSets", "NetworkInterfaceSet", "NetworkInterfaceId", "NetworkInterfaceName"),
}


def preflight(env=os.environ, *, read_only=False, cleanup_only=False):
    if read_only and cleanup_only:
        raise ValueError("select either read-only or cleanup-only access")
    required = ("RELEASE_ALIYUN_ACCOUNT_ID", "RELEASE_ALIYUN_ROLE_ARN", "RELEASE_ALIYUN_OIDC_PROVIDER_ARN")
    if any(not env.get(k) for k in required):
        raise ValueError("configure the dedicated pre-release account")
    if not read_only:
        if env.get("RELEASE_CLOUD_CLEANUP_ENABLED") != "true":
            raise ValueError("explicitly enable independent cloud cleanup before cloud writes")
        if not cleanup_only and env.get("RELEASE_CLOUD_ACCEPTANCE_ENABLED") != "true":
            raise ValueError("explicitly enable cloud acceptance before resource creation")
    account = env["RELEASE_ALIYUN_ACCOUNT_ID"]
    if not re.fullmatch(r"[0-9]{16}", account):
        raise ValueError("expected Alibaba account ID must have 16 digits")
    identities = [(required[1], "role"), (required[2], "oidc-provider")]
    if env.get("RELEASE_ALIYUN_CLEANUP_ROLE_ARN"):
        identities.append(("RELEASE_ALIYUN_CLEANUP_ROLE_ARN", "role"))
    for key, resource in identities:
        if not re.fullmatch(r"acs:ram::" + account + ":" + resource + r"/[A-Za-z0-9._-]+", env[key]):
            raise ValueError("role and OIDC provider must belong to the dedicated expected account")
    if env.get("GITHUB_REF") != "refs/heads/main":
        raise ValueError("cloud operations require the protected main workflow")
    return account


def labels(project, sha, run, attempt, mode, expires):
    revision(sha)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", project):
        raise ValueError("an exact owner/repository is required")
    if mode not in (*MODES, *JOB_MODES, DRILL_MODE) or any(not re.fullmatch(r"[1-9][0-9]*", v) for v in (run, attempt)):
        raise ValueError("invalid scenario or run identity")
    return {"fleeting-purpose": PURPOSE, "fleeting-project": project, "fleeting-candidate": sha,
            "fleeting-ci-run": run, "fleeting-ci-attempt": attempt, "fleeting-scenario": mode,
            "fleeting-fixture-expires": expires}


def name_for(tags):
    aliases = dict(zip(MODES, ("smoke", "scale", "restart", "creating", "removing", "lost-reply")))
    aliases[DRILL_MODE] = "cleanup"
    for phase, short_phase in (("candidate", "pre"), ("public", "post")):
        for executor, short_executor in (("instance", "vm"), ("docker-autoscaler", "docker")):
            for scenario in JOB_SCENARIOS:
                suffix = "" if scenario == "smoke" else "-" + scenario
                short_suffix = "" if scenario == "smoke" else "-" + ("q" if scenario == "capacity" else scenario[0])
                aliases[f"job-{phase}-{executor}{suffix}"] = f"job-{short_phase}-{short_executor}{short_suffix}"
    identity = {k: v for k, v in tags.items() if k != "fleeting-fixture-expires"}
    token = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()[:12]
    return f"fleeting-pre-release-{aliases[tags['fleeting-scenario']]}-{tags['fleeting-ci-run'][-12:]}-{token}"


def tag_args(tags, prefix="Tag"):
    return {f"{prefix}.{n}.{field}": value for n, pair in enumerate(sorted(tags.items()), 1)
            for field, value in zip(("Key", "Value"), pair)}


def row_tags(row):
    values = row.get("Tags", {})
    if isinstance(values, dict):
        values = values.get("Tag", [])
    result = {}
    for tag in values:
        key, value = tag.get("TagKey", tag.get("Key")), tag.get("TagValue", tag.get("Value"))
        if not isinstance(key, str) or not isinstance(value, str) or key in result:
            raise ValueError("invalid or duplicated cloud resource tags")
        result[key] = value
    return result


def verified_members(response, group, owned):
    total = response.get("TotalCount")
    container = response.get("ScalingInstances")
    rows = container.get("ScalingInstance") if isinstance(container, dict) else None
    if type(total) is not int or total < 0 or not isinstance(rows, list) or len(rows) != total:
        raise ValueError("incomplete group member inventory; refusing forced deletion")
    ids = set()
    for row in rows:
        if not isinstance(row, dict):
            raise ValueError("invalid group member row; refusing forced deletion")
        identity = row.get("InstanceId")
        if not isinstance(identity, str) or not identity or identity in ids or row.get("ScalingGroupId") != group:
            raise ValueError("invalid group member identity; refusing forced deletion")
        ids.add(identity)
    if not ids <= owned:
        raise ValueError("group contains unverified members; refusing forced deletion")
    return ids


class Cloud:
    def __init__(self, region, *, read_only=False, cleanup_only=False):
        self.region, self.read_only, self.cleanup_only = region, read_only, cleanup_only
        self.account = preflight(read_only=read_only, cleanup_only=cleanup_only)
        if any(not os.environ.get(k) for k in ("ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_SECURITY_TOKEN")):
            raise ValueError("fresh OIDC STS credentials are required; developer profiles and long-lived keys are not used")
        self.cli_profile = {
            "name": "pre-release", "mode": "StsToken", "region_id": region,
            "access_key_id": os.environ["ALIBABA_CLOUD_ACCESS_KEY_ID"],
            "access_key_secret": os.environ["ALIBABA_CLOUD_ACCESS_KEY_SECRET"],
            "sts_token": os.environ["ALIBABA_CLOUD_SECURITY_TOKEN"],
        }
        if self.call("sts", "GetCallerIdentity").get("AccountId") != self.account:
            raise ValueError("authenticated account differs from the dedicated expected account")

    def call(self, service, action, **params):
        reads = {(s[0], s[1]) for s in SPECS.values()} | {("sts", "GetCallerIdentity")}
        if self.read_only and (service, action) not in reads:
            raise ValueError("configuration checks permit only identity and resource inventories")
        cleanup_actions = reads | {("ess", "DescribeScalingInstances"), ("ess", "DescribeScalingActivities"), ("ess", "DeleteScalingGroup"),
                                   ("ecs", "DeleteInstance"), ("ecs", "DeleteDisk"), ("ecs", "DeleteNetworkInterface"),
                                   ("ecs", "DeleteLaunchTemplate"), ("ecs", "DeleteKeyPairs"), ("ecs", "DeleteSecurityGroup"),
                                   ("vpc", "DeleteVSwitch"), ("vpc", "DeleteVpc")}
        if self.cleanup_only and (service, action) not in cleanup_actions:
            raise ValueError("cleanup access permits only identity, inventories and deletion")
        command = ["aliyun", service, action, "--mode", "StsToken", "--region", self.region,
                   "--retry-count", "0", "--connect-timeout", "10", "--read-timeout", "30"]
        if service != "sts":
            params = {"RegionId": self.region, **params}
        for key, value in params.items():
            command.extend(["--" + key, str(value)])
        try:
            # CLI 3.5.2 validates the profile before reading credential overrides.
            # Isolate fresh STS credentials from developer profiles and argv.
            with tempfile.TemporaryDirectory(prefix="fleeting-cli-") as directory:
                config = Path(directory) / "config.json"
                with os.fdopen(os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
                    json.dump({"current": "pre-release", "profiles": [self.cli_profile]}, stream)
                command.extend(["--config-path", str(config), "--profile", "pre-release"])
                env = {**os.environ, "ALIBABA_CLOUD_IGNORE_PROFILE": "FALSE", "ALIBABACLOUD_IGNORE_PROFILE": "FALSE"}
                result = subprocess.run(command, capture_output=True, text=True, timeout=60, env=env)
        except (subprocess.SubprocessError, OSError):
            # UserData contains a per-run host private key. Subprocess exceptions
            # include argv, so neither their text nor exception chain may escape.
            raise CloudOperationError(service, action, unavailable=True) from None
        if result.returncode:
            raise CloudOperationError(service, action, output=result.stderr + "\n" + result.stdout)
        return json.loads(result.stdout)

    def inventory(self, kind):
        service, action, outer, inner, identity, _ = SPECS[kind]
        rows, seen, total = [], set(), None
        for page in range(1, 101):
            response = self.call(service, action, PageSize=50, PageNumber=page)
            count = response.get("TotalCount")
            items = response.get(outer, {}).get(inner)
            if type(count) is not int or count < 0 or (total is not None and count != total) or not isinstance(items, list):
                raise ValueError("incomplete cloud inventory: " + kind)
            total = count
            for row in items:
                value = row.get(identity)
                if not isinstance(value, str) or not value or value in seen:
                    raise ValueError("duplicate or missing resource identity: " + kind)
                seen.add(value)
            rows.extend(items)
            if len(rows) == total:
                return rows
            if not items or len(rows) > total:
                break
        raise ValueError("cloud pagination did not complete: " + kind)


class Fixture:
    def __init__(self, cloud, directory, tags):
        self.cloud, self.directory, self.tags = cloud, Path(directory), tags
        self.name = name_for(tags)
        self.path = self.directory / "fixture.json"
        self.state = {"tags": tags, "name": self.name, "operations": {}, "ids": {}}
        self.driver_exit_code = None
        if self.path.exists():
            self.state = json.loads(self.path.read_text())
            if self.state["tags"] != tags or self.state["name"] != self.name:
                raise ValueError("fixture receipt identity mismatch")
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)

    def save(self):
        write_json(self.path, self.state)
        self.path.chmod(0o600)

    def owned(self, kind, *, pending_tags=False):
        expected = self.tags
        rows = []
        for row in self.cloud.inventory(kind):
            actual = row_tags(row)
            exact = all(actual.get(k) == v for k, v in expected.items())
            if not exact:
                known = self.state["ids"].get(kind, [])
                known = known if isinstance(known, list) else [known]
                if row.get(SPECS[kind][4]) in known:
                    # An accepted create may appear before its atomic tags.
                    # Only readiness polling can defer missing tags; conflicts
                    # and every destructive ownership check remain strict.
                    if (pending_tags and self.state["operations"].get(kind) == "accepted"
                            and all(k not in actual or actual[k] == v for k, v in expected.items())):
                        continue
                    raise ValueError("recorded resource ownership changed: " + kind)
                continue
            if kind != "eni" and row.get(SPECS[kind][5]) != self.name:
                raise ValueError("labelled resource name mismatch: " + kind)
            vpc = row.get("VpcAttributes", {}).get("VpcId") if kind == "instance" else row.get("VpcId")
            if kind in ("vswitch", "sg", "group", "instance", "eni") and vpc != self.state["ids"].get("vpc"):
                raise ValueError("labelled resource parent mismatch: " + kind)
            if kind == "group" and row.get("LaunchTemplateId") != self.state["ids"].get("template"):
                raise ValueError("labelled group template mismatch")
            rows.append(row)
        if kind not in ("instance", "disk", "eni"):
            if len(rows) > 1:
                raise ValueError("multiple fixture resources of one kind")
            if rows:
                value = rows[0][SPECS[kind][4]]
                previous = self.state["ids"].get(kind)
                if previous is not None and previous != value:
                    raise ValueError("fixture resource identity changed")
                self.state["ids"][kind] = value
                if kind in self.state["operations"]:
                    self.state["operations"][kind] = "accepted"
                self.save()
        else:
            known = self.state["ids"].setdefault(kind, [])
            for row in rows:
                if row[SPECS[kind][4]] not in known:
                    known.append(row[SPECS[kind][4]])
            self.save()
        return rows

    def create(self, kind, action, params, tag_prefix="Tag"):
        if kind in self.state["operations"]:
            raise ValueError("fixture creation is never blindly repeated")
        self.state["operations"][kind] = "unknown"
        self.save()
        params = {**params, **tag_args(self.tags, tag_prefix)}
        if kind == "group":
            params.update({f"Tag.{n}.Propagate": "true" for n in range(1, len(self.tags) + 1)})
        if kind in ("vpc", "vswitch", "sg", "group"):
            params["ClientToken"] = hashlib.sha256((self.name + kind).encode()).hexdigest()
        body = self.cloud.call(SPECS[kind][0], action, **params)
        value = body.get(SPECS[kind][4])
        if not isinstance(value, str) or not value:
            raise ValueError("accepted fixture creation omitted its resource identity")
        self.state["ids"][kind] = value
        self.state["operations"][kind] = "accepted"
        self.save()
        return value

    def wait(self, kind, absent=False, seconds=120):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if absent:
                known = self.state["ids"].get(kind, [])
                known = known if isinstance(known, list) else [known]
                # Accepted deletion can remove tags before the resource itself.
                # Observe recorded IDs until absent without authorizing another
                # delete, then retain the strict check for any owned resources.
                rows = [row for row in self.cloud.inventory(kind) if row.get(SPECS[kind][4]) in known]
                if not rows:
                    rows = self.owned(kind)
            else:
                rows = self.owned(kind, pending_tags=True)
            if (absent and not rows) or (not absent and len(rows) == 1 and rows[0].get("Status") == "Available"):
                return
            time.sleep(3)
        raise RuntimeError("fixture observation deadline: " + kind)

    def prepare(self, cidr, config, *, worker_script=None):
        if config["region"] != self.cloud.region:
            raise ValueError("fixture region differs from the authenticated client")
        if worker_script is not None and (self.tags["fleeting-scenario"] not in JOB_MODES or not isinstance(worker_script, str) or not worker_script):
            raise ValueError("worker bootstrap is restricted to job acceptance")
        if self.state["operations"]:
            raise ValueError("existing fixture state is preserved; clean up before retrying")
        for name, kind, extra in (("worker-key", "rsa", ["-b", "3072"]), ("host-key", "ed25519", [])):
            subprocess.run(["ssh-keygen", "-q", "-t", kind, *extra, "-N", "", "-C", PURPOSE,
                            "-f", str(self.directory / name)], check=True, capture_output=True)
        ids = self.state["ids"]
        self.create("vpc", "CreateVpc", {"VpcName": self.name, "Description": PURPOSE, "CidrBlock": config["vpc_cidr"]})
        self.wait("vpc")
        self.create("vswitch", "CreateVSwitch", {"VSwitchName": self.name, "VpcId": ids["vpc"], "ZoneId": config["zone"], "CidrBlock": config["vswitch_cidr"]})
        self.wait("vswitch")
        self.create("sg", "CreateSecurityGroup", {"SecurityGroupName": self.name, "VpcId": ids["vpc"], "Description": PURPOSE})
        self.cloud.call("ecs", "AuthorizeSecurityGroup", SecurityGroupId=ids["sg"], IpProtocol="tcp", PortRange="22/22", SourceCidrIp=cidr, Description=PURPOSE)
        self.create("key", "ImportKeyPair", {"KeyPairName": self.name, "PublicKeyBody": (self.directory / "worker-key.pub").read_text().strip()})
        private = (self.directory / "host-key").read_text()
        public = (self.directory / "host-key.pub").read_text().strip()
        user_data = "#cloud-config\ndisable_root: false\nssh_pwauth: false\nssh_genkeytypes: [ed25519]\nssh_keys:\n  ed25519_private: |\n"
        user_data += "".join("    " + line + "\n" for line in private.splitlines())
        user_data += "  ed25519_public: " + public + "\nswap:\n  filename: /swapfile\n  size: " + str(config["swap_mib"] * 1024 * 1024) + "\n"
        if worker_script is not None:
            worker_key = (self.directory / "worker-key.pub").read_text().strip()
            script = "export WORKER_PUBLIC_KEY=" + shlex.quote(worker_key) + "\n" + worker_script
            user_data += "runcmd:\n  - " + json.dumps(["sh", "-c", script]) + "\n"
        self.create("template", "CreateLaunchTemplate", {
            "LaunchTemplateName": self.name, "VersionDescription": PURPOSE, "ImageId": config["image"],
            "InstanceType": config["instance_type"], "ZoneId": config["zone"], "VSwitchId": ids["vswitch"], "SecurityGroupId": ids["sg"],
            "KeyPairName": self.name, "InstanceName": self.name, "InstanceChargeType": "PostPaid",
            "SpotStrategy": "SpotAsPriceGo", "SpotDuration": config["spot_duration"], "InternetChargeType": "PayByTraffic", "InternetMaxBandwidthOut": config["internet_max_bandwidth_out"],
            "SystemDisk.Category": config["disk_category"], "SystemDisk.Size": config["disk_size_gib"],
            "SystemDisk.DeleteWithInstance": "true", "SystemDisk.DiskName": self.name, "SystemDisk.Description": PURPOSE,
            "AutoReleaseTime": self.tags["fleeting-fixture-expires"], "HttpTokens": "required", "HttpEndpoint": "enabled",
            "UserData": base64.b64encode(user_data.encode()).decode(),
            **({"SystemDisk.PerformanceLevel": config["disk_performance_level"]} if config["disk_performance_level"] else {}),
            **tag_args(self.tags)}, "TemplateTag")
        self.create("group", "CreateScalingGroup", {
            "ScalingGroupName": self.name, "GroupType": "ECS", "MinSize": 0,
            "MaxSize": 2 if self.tags["fleeting-scenario"] == "cloud-scale" or self.tags["fleeting-scenario"].endswith("-capacity") else 1, "DesiredCapacity": 0,
            "LaunchTemplateId": ids["template"], "LaunchTemplateVersion": "1", "VSwitchId": ids["vswitch"],
            "ScalingPolicy": "release", "MultiAZPolicy": "COST_OPTIMIZED", "SpotInstancePools": 1,
            "OnDemandBaseCapacity": 0, "OnDemandPercentageAboveBaseCapacity": 0, "CompensateWithOnDemand": "false",
            "SpotInstanceRemedy": "false", "AzBalance": "false", "AutoRebalance": "false", "DefaultCooldown": 0})
        self.cloud.call("ess", "EnableScalingGroup", ScalingGroupId=ids["group"])

    def cleanup(self):
        if self.state.get("driver_group"):
            raise ValueError("owned writer termination is not confirmed; independent recovery is required")
        # Recover parents first, including accepted writes whose replies were lost.
        for kind in ("vpc", "vswitch", "sg", "key", "template", "group"):
            self.owned(kind)
        for kind in ("group", "instance", "disk", "eni", "template", "key", "sg", "vswitch", "vpc"):
            rows = self.owned(kind)
            if kind == "group" and rows:
                members = self.cloud.call("ess", "DescribeScalingInstances", ScalingGroupId=rows[0]["ScalingGroupId"], PageSize=100)
                verified_members(members, rows[0]["ScalingGroupId"], {r["InstanceId"] for r in self.owned("instance")})
            for row in rows:
                identity = row[SPECS[kind][4]]
                action, params = {
                    "group": ("DeleteScalingGroup", {"ScalingGroupId": identity, "ForceDelete": "true"}),
                    "instance": ("DeleteInstance", {"InstanceId": identity, "Force": "true"}),
                    "disk": ("DeleteDisk", {"DiskId": identity}),
                    "eni": ("DeleteNetworkInterface", {"NetworkInterfaceId": identity}),
                    "template": ("DeleteLaunchTemplate", {"LaunchTemplateId": identity}),
                    "key": ("DeleteKeyPairs", {"KeyPairNames": json.dumps([identity])}),
                    "sg": ("DeleteSecurityGroup", {"SecurityGroupId": identity}),
                    "vswitch": ("DeleteVSwitch", {"VSwitchId": identity}),
                    "vpc": ("DeleteVpc", {"VpcId": identity}),
                }[kind]
                self.cloud.call(SPECS[kind][0], action, **params)
            self.wait(kind, absent=True, seconds=180 if kind == "group" else 90)
        if "unknown" in self.state["operations"].values():
            raise ValueError("ambiguous fixture creation remains unresolved")
        for _ in range(2):
            if any(self.owned(kind) for kind in SPECS):
                raise ValueError("fixture resources remain after teardown")
            time.sleep(3)
        for path in self.directory.rglob("*"):
            if path.is_file() and (path.name.startswith(("worker-key", "host-key")) or path.name == "gate-ca.pem"):
                path.unlink()


def driver(fixture, root, manifest, cidr, config):
    mode = fixture.tags["fleeting-scenario"]
    binary = root / manifest["fault_binary" if mode == MODES[-1] else "normal_binary"]
    expected_hash = manifest["files"][str(binary.relative_to(root))]
    if mode != MODES[-1]:
        archive = root / f"dist/fleeting-plugin-aliyun_{manifest['version']}_linux_amd64.tar.gz"
        binary = fixture.directory / "native-plugin"
        with tarfile.open(archive, "r:gz") as package:
            binary.write_bytes(package.extractfile("fleeting-plugin-aliyun").read())
    if digest(binary) != expected_hash:
        raise ValueError("test binary differs from the immutable candidate")
    binary.chmod(0o700)
    env = {k: v for k, v in os.environ.items() if not k.startswith("GITLAB_") and k not in
           ("ALIBABA_CLOUD_PROFILE", "ALIBABA_CLOUD_CREDENTIALS_FILE", "ALIBABA_CLOUD_ROLE_ARN",
            "ALIBABA_CLOUD_OIDC_TOKEN_FILE", "ALIBABA_CLOUD_OIDC_PROVIDER_ARN", "ALIYUN_COMPONENT_ENDPOINT",
            "ALIYUN_RESPONSE_GATE_ADDR", "ALIYUN_RESPONSE_GATE_CA")}
    env.update(RUN_LIVE_ACCEPTANCE="1", LIVE_EXTENDED_MODE=mode, ALIYUN_REGION=config["region"],
               ALIYUN_SCALING_GROUP_ID=fixture.state["ids"]["group"], ACCEPTANCE_PROJECT=fixture.tags["fleeting-project"],
               ALLOWED_INSTANCE_TYPE=config["instance_type"], ALLOWED_IMAGE_ID=config["image"], ALLOWED_ZONE_ID=config["zone"],
               WORKER_USER="root", WORKER_SSH_KEY=str((fixture.directory / "worker-key").resolve()),
               MANAGER_CIDR=cidr, PLUGIN_BINARY=str(binary.resolve()), PLUGIN_COMMIT=manifest["candidate_sha"],
               ACCEPTANCE_OUTPUT=str((fixture.directory / "evidence").resolve()))
    with (fixture.directory / "driver.log").open("w") as output:
        fixture.state["driver_group"] = "starting"
        fixture.save()
        process = None
        try:
            process = subprocess.Popen(["go", "test", "-v", "-tags", "live", "-count=1", "-timeout=24m", "-run", "^TestExtendedCloud$", "./tests/live"],
                                       env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            fixture.state["driver_group"] = process.pid
            fixture.save()
            code = process.wait(timeout=25 * 60)
            fixture.driver_exit_code = code
        finally:
            if process is not None:
                stop_process_group(process)
                fixture.state["driver_group"] = None
                fixture.save()
    path = fixture.directory / "evidence" / ("run-" + env["GITHUB_RUN_ID"] + "-" + env["GITHUB_RUN_ATTEMPT"]) / "extended-result.json"
    try:
        receipt = json.loads(path.read_text())
    except (OSError, ValueError):
        receipt = {}
    receipt = receipt if isinstance(receipt, dict) else {}
    resources = receipt.get("resources", {})
    resources = resources if isinstance(resources, dict) else {}
    if code or receipt.get("result") != "PASS" or receipt.get("mode") != mode or receipt.get("plugin_sha256") != expected_hash or receipt.get("plugin_commit") != manifest["candidate_sha"] or not receipt.get("normal_release") or resources.get("cleanup_result") != "PASS":
        log = (fixture.directory / "driver.log").read_text(errors="replace")
        print(json.dumps({"driver_diagnostic": driver_diagnostic(receipt, code, log)}), flush=True)
        raise ValueError("cloud scenario did not produce a passing candidate receipt")
    return expected_hash


def stop_process_group(process):
    require_linux()
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=10)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        alive = False
        for path in Path('/proc').glob('[0-9]*/stat'):
            try:
                fields = path.read_text().rsplit(')', 1)[1].split()
            except FileNotFoundError:
                continue
            if int(fields[2]) == process.pid and fields[0] != 'Z':
                alive = True
        if not alive:
            return
        time.sleep(0.1)
    raise RuntimeError("owned process group did not terminate")


def require_linux():
    if sys.platform != "linux" or not Path('/proc/self/stat').is_file():
        raise RuntimeError("the live driver requires Linux procfs to prove writer termination")


def stopped_run(tags):
    endpoint = f"repos/{tags['fleeting-project']}/actions/runs/{tags['fleeting-ci-run']}/attempts/{tags['fleeting-ci-attempt']}"
    result = subprocess.run(["gh", "api", endpoint], capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise ValueError("cannot independently verify the owning GitHub run")
    run = json.loads(result.stdout)
    if str(run.get("id")) != tags["fleeting-ci-run"] or str(run.get("run_attempt")) != tags["fleeting-ci-attempt"] or run.get("repository", {}).get("full_name") != tags["fleeting-project"]:
        raise ValueError("owning GitHub run identity differs")
    return run.get("status") == "completed"


def run(args):
    require_linux()
    config = fixture_config()
    manifest = verify(args.root, args.digest, args.sha, args.version)
    expires = (datetime.now(timezone.utc) + timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
    tags = labels(os.environ["GITHUB_REPOSITORY"], args.sha, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"], args.mode, expires)
    fixture = Fixture(Cloud(config["region"]), args.directory, tags)
    address = subprocess.check_output(["curl", "-4", "--fail", "--silent", "--show-error", "--max-time", "15", "https://api.ipify.org"], text=True).strip()
    cidr = str(ipaddress.IPv4Address(address)) + "/32"
    status = cleanup_status = "FAIL"
    plugin_hash = ""
    diagnostics = {"fixture_setup": {"result": "UNKNOWN"}, "driver": {"result": "UNKNOWN"},
                   "fixture_cleanup": {"result": "UNKNOWN"}}
    primary_error = cleanup_error = None
    stage, started = "fixture_setup", time.monotonic()
    try:
        fixture.prepare(cidr, config)
        diagnostics[stage] = {"result": "PASS", "duration_ms": duration_ms(started)}
        stage, started = "driver", time.monotonic()
        plugin_hash = driver(fixture, args.root, manifest, cidr, config)
        status = "PASS"
        diagnostics[stage] = {"result": "PASS", "duration_ms": duration_ms(started)}
    except BaseException as error:
        # Preserve cancellation as well as normal failure, but always attempt the
        # existing guarded cleanup and persist both outcomes before propagating.
        primary_error = error
        diagnostics[stage] = {"result": "FAIL", "duration_ms": duration_ms(started),
                              "error": diagnostic_error(error)}
    finally:
        started = time.monotonic()
        try:
            fixture.cleanup()
            cleanup_status = "PASS"
            diagnostics["fixture_cleanup"] = {"result": "PASS", "duration_ms": duration_ms(started)}
        except BaseException as error:
            cleanup_error = error
            diagnostics["fixture_cleanup"] = {"result": "FAIL", "duration_ms": duration_ms(started),
                                               "error": diagnostic_error(error)}
        diagnostics["driver"]["evidence"] = read_driver_diagnostic(
            fixture.directory, tags["fleeting-ci-run"], tags["fleeting-ci-attempt"], fixture.driver_exit_code)
        receipt = {"schema_version": 1, "result": status, "cleanup_result": cleanup_status,
                   "candidate_sha": args.sha, "version": args.version, "manifest_sha256": args.digest,
                   "mode": args.mode, "run_id": tags["fleeting-ci-run"], "run_attempt": tags["fleeting-ci-attempt"],
                   "fixture_config_sha256": config_digest(config),
                   "plugin_sha256": plugin_hash, "response_gate_instrumented": args.mode == MODES[-1],
                   "leftover_resources": 0 if cleanup_status == "PASS" else -1, "diagnostics": diagnostics}
        # The console fallback survives a failure to write/upload the receipt.
        print(json.dumps({"cloud_diagnostic": diagnostics}), flush=True)
        write_json(args.receipt, receipt)
    if isinstance(cleanup_error, (KeyboardInterrupt, SystemExit)) and not isinstance(primary_error, (KeyboardInterrupt, SystemExit)):
        raise cleanup_error
    if primary_error is not None:
        raise primary_error
    if cleanup_error is not None:
        raise cleanup_error


def verify_drill_creation(owner, identity=None):
    group = owner.state["ids"]["group"]
    response = owner.cloud.call("ess", "DescribeScalingActivities", ScalingGroupId=group, PageNumber=1, PageSize=50)
    total = response.get("TotalCount")
    rows = response.get("ScalingActivities", {}).get("ScalingActivity")
    if type(total) is not int or not isinstance(rows, list) or not 0 <= total == len(rows) <= 50:
        raise ValueError("cleanup drill activity history is incomplete")
    activities, instances, created, pending = set(), set(), 0, False
    for row in rows:
        activity = row.get("ScalingActivityId")
        count = row.get("CreatedCapacity", 0)
        ids = row.get("CreatedInstances", {}).get("CreatedInstance", [])
        status = row.get("StatusCode")
        if (not isinstance(activity, str) or not activity or activity in activities or row.get("ScalingGroupId") != group or
                type(count) is not int or count < 0 or not isinstance(ids, list) or
                any(not isinstance(value, str) or not value.startswith("i-") for value in ids) or len(set(ids)) != len(ids) or
                status not in ("Successful", "InProgress")):
            raise ValueError("cleanup drill activity is failed, unknown or malformed")
        activities.add(activity)
        if status == "Successful" and count != len(ids):
            raise ValueError("cleanup drill activity creation evidence is incomplete")
        created += count
        instances.update(ids)
        pending |= status == "InProgress"
    if created > 1 or len(instances) > 1 or (identity is not None and instances - {identity}):
        raise ValueError("cleanup drill activity history exceeds its one-worker budget")
    if pending or created != 1 or len(instances) != 1:
        return False
    members = owner.cloud.call("ess", "DescribeScalingInstances", ScalingGroupId=group, PageSize=100)
    current = {row["InstanceId"] for row in owner.owned("instance")}
    if len(owner.state["ids"].get("instance", [])) > 1 or current - instances:
        raise ValueError("cleanup drill observed a replacement worker")
    members = verified_members(members, group, current)
    return members == {identity} if identity is not None else members <= instances


def janitor(directory, completed_run=None, completed_attempt=None, *, receipt=None, require_recovery=False):
    if completed_run or completed_attempt:
        if any(not isinstance(v, str) or not re.fullmatch(r"[1-9][0-9]*", v) for v in (completed_run, completed_attempt)):
            raise ValueError("both completed run and attempt must be positive identifiers")
    if require_recovery and not completed_run:
        raise ValueError("a recovery drill requires an exact completed run and attempt")
    result = {"schema_version": 1, "result": "FAIL", "cleaned_fixtures": 0, "leftover_resources": -1,
              "source_run_id": completed_run, "source_run_attempt": completed_attempt,
              "scope": "completed-run" if completed_run else "expired", "recovery_required": require_recovery,
              "drill_budget_verified": False if require_recovery else None}
    try:
        failures = 0
        budget_verified = True
        for region in cleanup_regions():
            try:
                recovered = janitor_region(Cloud(region, cleanup_only=True), directory / region, completed_run, completed_attempt, require_recovery=require_recovery)
                result["cleaned_fixtures"] += recovered["cleaned_fixtures"]
                if require_recovery:
                    budget_verified &= recovered["drill_budget_verified"]
            except Exception:
                # A malformed response in one region must not strand other regions.
                # Process termination still propagates (BaseException).
                failures += 1
        if failures:
            raise RuntimeError("cleanup failed in one or more configured regions; remaining regions were attempted")
        if require_recovery and result["cleaned_fixtures"] != 1:
            raise RuntimeError("cleanup drill requires exactly one recovered fixture; an empty scan is not proof")
        result["leftover_resources"] = 0
        if require_recovery:
            result["drill_budget_verified"] = budget_verified
        if require_recovery and not result["drill_budget_verified"]:
            raise RuntimeError("fixture cleanup completed but the one-worker drill budget is not proven")
        result.update(result="PASS", leftover_resources=0)
    finally:
        print(json.dumps(result), flush=True)
        if receipt is not None:
            write_json(receipt, result)
    return result


def janitor_region(cloud, directory, completed_run, completed_attempt, *, require_recovery=False):
    project = os.environ["GITHUB_REPOSITORY"]
    fixtures = {}
    # Every atomic create is labelled; scanning all kinds also recovers a setup
    # interrupted before its VPC/group response was saved locally.
    for kind in SPECS:
        for row in cloud.inventory(kind):
            tags = row_tags(row)
            if tags.get("fleeting-purpose") != PURPOSE or tags.get("fleeting-project") != project:
                continue
            expected = labels(project, tags.get("fleeting-candidate", ""), tags.get("fleeting-ci-run", ""),
                              tags.get("fleeting-ci-attempt", ""), tags.get("fleeting-scenario", ""), tags.get("fleeting-fixture-expires", ""))
            expired = datetime.strptime(expected["fleeting-fixture-expires"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc) < datetime.now(timezone.utc)
            completed = (completed_run, completed_attempt) == (expected["fleeting-ci-run"], expected["fleeting-ci-attempt"])
            if (completed_run and not completed) or (not completed_run and not expired):
                continue
            if not stopped_run(expected):
                continue
            fixtures[name_for(expected)] = expected
    budget_verified = True
    for name, tags in fixtures.items():
        owner = Fixture(cloud, directory / name, tags)
        if require_recovery:
            try:
                for kind in ("vpc", "template", "group"):
                    owner.owned(kind)
                if tags["fleeting-scenario"] != DRILL_MODE or not verify_drill_creation(owner):
                    budget_verified = False
            except Exception:
                # An unknown/failed budget observation must not strand resources.
                # Cleanup retains its independent, strict ownership checks.
                budget_verified = False
        owner.cleanup()
    return {"cleaned_fixtures": len(fixtures), "drill_budget_verified": budget_verified}


def check_configuration():
    fixture_config()
    for region in cleanup_regions():
        cloud = Cloud(region, read_only=True)
        for kind in SPECS:
            cloud.inventory(kind)
    print(json.dumps({"result": "PASS", "scope": "identity-and-inventory-only",
                      "write_permissions_verified": False, "resources_created": 0}))


def main():
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("preflight", "run", "janitor", "check"))
    parser.add_argument("--root", type=Path)
    parser.add_argument("--digest")
    parser.add_argument("--sha")
    parser.add_argument("--version")
    parser.add_argument("--mode", choices=MODES)
    parser.add_argument("--directory", type=Path, default=Path(".live/cloud"))
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--read-only", action="store_true", help="allow disabled acceptance during configuration preflight")
    parser.add_argument("--cleanup", action="store_true", help="validate only identity and recovery regions")
    parser.add_argument("--completed-run")
    parser.add_argument("--completed-attempt")
    parser.add_argument("--require-recovery", action="store_true", help="require exactly one recovered drill fixture")
    args = parser.parse_args()
    if args.require_recovery and args.operation != "janitor":
        parser.error("--require-recovery applies only to janitor")
    if (args.read_only or args.cleanup) and args.operation != "preflight":
        parser.error("--read-only and --cleanup apply only to preflight; use check for read-only cloud access")
    if args.operation == "preflight":
        preflight(read_only=args.read_only, cleanup_only=args.cleanup)
        cleanup_regions() if args.cleanup else fixture_config()
        if os.environ.get("GITHUB_ACTIONS") == "true":
            for key, value in os.environ.items():
                if key.startswith("RELEASE_ALIYUN_") and value:
                    for line in value.splitlines():
                        if line:
                            print("::add-mask::" + line.replace("%", "%25").replace("\r", "%0D"))
    elif args.operation == "check":
        check_configuration()
    elif args.operation == "janitor":
        janitor(args.directory, args.completed_run, args.completed_attempt, receipt=args.receipt, require_recovery=args.require_recovery)
    else:
        run(args)


if __name__ == "__main__":
    main()
