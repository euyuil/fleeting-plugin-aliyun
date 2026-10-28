"""Exercise fixture ownership, partial writes, account isolation and failed cleanup offline."""
import argparse
import base64
import copy
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import subprocess
import shutil
import ssl
import sys
import tempfile
import threading
import traceback
import unittest
from unittest import mock
from urllib.parse import parse_qs, quote, urlsplit

import cloud_fixture as fixture
from test_cloud_config import CONFIG, CONFIG_ENV

STS_ENV = {"ALIBABA_CLOUD_ACCESS_KEY_ID": "synthetic-sts-id",
           "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "synthetic-sts-secret",
           "ALIBABA_CLOUD_SECURITY_TOKEN": "synthetic-sts-token"}


class FakeCloud:
    def __init__(self):
        self.region = CONFIG["region"]
        self.rows = {kind: [] for kind in fixture.SPECS}
        self.calls = []
        self.fail_create = False

    def inventory(self, kind):
        return copy.deepcopy(self.rows[kind])

    def call(self, service, action, **params):
        self.calls.append((service, action, params))
        creates = {"CreateVpc": "vpc", "CreateVSwitch": "vswitch", "CreateSecurityGroup": "sg",
                   "ImportKeyPair": "key", "CreateLaunchTemplate": "template", "CreateScalingGroup": "group"}
        if action in creates:
            kind = creates[action]
            spec = fixture.SPECS[kind]
            identity = params.get(spec[4], "test-" + kind)
            prefix = "TemplateTag" if kind == "template" else "Tag"
            tags = [{"Key": v, "Value": params[k.replace(".Key", ".Value")]}
                    for k, v in params.items() if k.startswith(prefix + ".") and k.endswith(".Key")]
            row = {spec[4]: identity, spec[5]: params[spec[5]], "Tags": {"Tag": tags}, "Status": "Available"}
            for key in ("VpcId", "LaunchTemplateId"):
                if key in params:
                    row[key] = params[key]
            if kind == "group":
                row["VpcId"] = "test-vpc"
            self.rows[kind].append(row)
            if self.fail_create:
                raise RuntimeError("lost accepted response")
            return {spec[4]: identity, "LaunchTemplateVersionNumber": 1}
        if action == "DescribeScalingInstances":
            return {"TotalCount": 0, "ScalingInstances": {"ScalingInstance": []}}
        if action.startswith("Delete"):
            for kind, rows in self.rows.items():
                field = fixture.SPECS[kind][4]
                identity = json.loads(params["KeyPairNames"])[0] if kind == "key" and "KeyPairNames" in params else params.get(field)
                if identity:
                    self.rows[kind] = [row for row in rows if row[field] != identity]
        return {}


class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        expires = (datetime.now(timezone.utc) + timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
        self.tags = fixture.labels("example/plugin", "a" * 40, "123", "1", "cloud-smoke", expires)
        self.cloud = FakeCloud()
        self.owner = fixture.Fixture(self.cloud, self.directory, self.tags)

    def test_test_names_tags_and_candidate_are_present_at_every_create(self):
        def keygen(command, **_kwargs):
            path = Path(command[-1])
            path.write_text("synthetic private key\n")
            path.with_suffix(".pub").write_text("synthetic public key\n")
        with mock.patch("subprocess.run", side_effect=keygen):
            self.owner.prepare("192.0.2.1/32", CONFIG)
        self.assertTrue(self.owner.name.startswith("fleeting-pre-release-smoke-123-"))
        for _, action, params in self.cloud.calls:
            if action.startswith(("Create", "Import")):
                prefix = "TemplateTag" if action == "CreateLaunchTemplate" else "Tag"
                for key, value in fixture.tag_args(self.tags, prefix).items():
                    self.assertEqual(params[key], value)
        template = next(p for _, a, p in self.cloud.calls if a == "CreateLaunchTemplate")
        self.assertEqual(template["SystemDisk.PerformanceLevel"], CONFIG["disk_performance_level"])
        self.assertEqual(template["SystemDisk.Size"], CONFIG["disk_size_gib"])
        for field, key in (("InstanceType", "instance_type"), ("ImageId", "image"), ("ZoneId", "zone"),
                           ("SystemDisk.Category", "disk_category"), ("SpotDuration", "spot_duration"),
                           ("InternetMaxBandwidthOut", "internet_max_bandwidth_out")):
            self.assertEqual(template[field], CONFIG[key])
        user_data = base64.b64decode(template["UserData"]).decode()
        self.assertIn("size: " + str(CONFIG["swap_mib"] * 1024 * 1024), user_data)
        for action, key in (("CreateVpc", "vpc_cidr"), ("CreateVSwitch", "vswitch_cidr")):
            self.assertEqual(next(p["CidrBlock"] for _, a, p in self.cloud.calls if a == action), CONFIG[key])
        self.assertEqual(template["SpotStrategy"], "SpotAsPriceGo")
        self.assertEqual(template["AutoReleaseTime"], self.tags["fleeting-fixture-expires"])
        self.assertTrue(set(fixture.tag_args(self.tags)) <= template.keys())
        group = next(p for _, a, p in self.cloud.calls if a == "CreateScalingGroup")
        self.assertEqual(group["MaxSize"], 1)
        self.assertEqual(group["CompensateWithOnDemand"], "false")
        with mock.patch("time.sleep"):
            self.owner.cleanup()
        self.assertTrue(all(not rows for rows in self.cloud.rows.values()))
        self.assertFalse((self.directory / "host-key").exists())

    def test_lost_create_reply_is_recovered_without_repeating_creation(self):
        self.cloud.fail_create = True
        with self.assertRaisesRegex(RuntimeError, "lost accepted"):
            self.owner.create("vpc", "CreateVpc", {"VpcName": self.owner.name})
        self.assertEqual(self.owner.state["operations"]["vpc"], "unknown")
        with self.assertRaisesRegex(ValueError, "never blindly repeated"):
            self.owner.create("vpc", "CreateVpc", {"VpcName": self.owner.name})
        with mock.patch("time.sleep"):
            self.owner.cleanup()
        self.assertEqual(self.owner.state["operations"]["vpc"], "accepted")
        self.assertEqual(sum(action == "CreateVpc" for _, action, _ in self.cloud.calls), 1)

    def test_unresolved_creation_cannot_be_reported_as_clean(self):
        self.owner.state["operations"]["vpc"] = "unknown"
        with self.assertRaisesRegex(ValueError, "remains unresolved"):
            self.owner.cleanup()

    def test_matching_name_without_all_tags_is_preserved(self):
        row = {"VpcId": "foreign-vpc", "VpcName": self.owner.name, "Tags": {"Tag": []}}
        self.cloud.rows["vpc"] = [row]
        with mock.patch("time.sleep"):
            self.owner.cleanup()
        self.assertEqual(self.cloud.rows["vpc"], [row])
        self.assertFalse(any(action.startswith("Delete") for _, action, _ in self.cloud.calls))

    def test_readiness_waits_for_atomic_create_tags_without_recreating(self):
        self.owner.create("vpc", "CreateVpc", {"VpcName": self.owner.name})
        ready = copy.deepcopy(self.cloud.rows["vpc"][0])
        pending = copy.deepcopy(ready)
        pending["Tags"]["Tag"] = pending["Tags"]["Tag"][:2]
        pending["Status"] = "Pending"
        with mock.patch.object(self.cloud, "inventory", side_effect=[[pending], [ready]]) as inventory:
            with mock.patch("time.sleep") as sleep:
                self.owner.wait("vpc")
        self.assertEqual(inventory.call_count, 2)
        sleep.assert_called_once_with(3)
        self.assertEqual([action for _, action, _ in self.cloud.calls], ["CreateVpc"])

    def test_missing_create_tags_time_out_and_cannot_authorize_cleanup(self):
        self.owner.create("vpc", "CreateVpc", {"VpcName": self.owner.name})
        self.cloud.rows["vpc"][0]["Tags"] = {"Tag": []}
        with mock.patch("time.monotonic", side_effect=[0, 0, 121]), mock.patch("time.sleep"):
            with self.assertRaisesRegex(RuntimeError, "observation deadline"):
                self.owner.wait("vpc")
        with self.assertRaisesRegex(ValueError, "ownership changed"):
            self.owner.cleanup()
        self.assertFalse(any(action.startswith("Delete") for _, action, _ in self.cloud.calls))

    def test_readiness_rejects_conflicting_tags_even_while_other_tags_are_missing(self):
        self.owner.create("vpc", "CreateVpc", {"VpcName": self.owner.name})
        self.cloud.rows["vpc"][0]["Tags"] = {"Tag": [{"Key": "fleeting-project", "Value": "other/project"}]}
        with mock.patch("time.sleep") as sleep:
            with self.assertRaisesRegex(ValueError, "ownership changed"):
                self.owner.wait("vpc")
        sleep.assert_not_called()

    def test_changed_ownership_or_parent_blocks_destructive_cleanup(self):
        self.owner.state["ids"]["vpc"] = "test-vpc"
        self.cloud.rows["vpc"] = [{"VpcId": "test-vpc", "VpcName": self.owner.name, "Tags": {"Tag": []}}]
        with self.assertRaisesRegex(ValueError, "ownership changed"):
            self.owner.cleanup()
        self.cloud.rows["vpc"] = []
        self.cloud.rows["sg"] = [{"SecurityGroupId": "test-sg", "SecurityGroupName": self.owner.name,
                                   "VpcId": "foreign-vpc", "Tags": {"Tag": [{"Key": k, "Value": v} for k, v in self.tags.items()]}}]
        with self.assertRaisesRegex(ValueError, "parent mismatch"):
            self.owner.cleanup()
        self.assertFalse(any(action.startswith("Delete") for _, action, _ in self.cloud.calls))

    def test_cleanup_failure_is_nonzero_and_never_a_passing_receipt(self):
        root = self.directory / "candidate"
        manifest = {"candidate_sha": "a" * 40, "version": "1.0.0"}
        args = argparse.Namespace(root=root, digest="d" * 64, sha="a" * 40, version="1.0.0", mode="cloud-smoke",
                                  directory=self.directory / "private", receipt=self.directory / "public.json")
        env = {**CONFIG_ENV, "GITHUB_REPOSITORY": "example/plugin", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1"}
        with mock.patch.dict(os.environ, env):
            with mock.patch("cloud_fixture.verify", return_value=manifest), mock.patch("cloud_fixture.Cloud"), mock.patch("cloud_fixture.require_linux"):
                with mock.patch("subprocess.check_output", return_value="192.0.2.1"), mock.patch.object(fixture.Fixture, "prepare"):
                    with mock.patch("cloud_fixture.driver", return_value="e" * 64), mock.patch.object(fixture.Fixture, "cleanup", side_effect=RuntimeError("cleanup failed")):
                        with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                            fixture.run(args)
        receipt = json.loads(args.receipt.read_text())
        self.assertEqual(receipt["result"], "PASS")
        self.assertEqual(receipt["cleanup_result"], "FAIL")
        self.assertEqual(receipt["leftover_resources"], -1)

    def test_janitor_can_recover_without_local_ledger_and_preserves_active_other_attempt(self):
        self.cloud.rows["vpc"] = [{"VpcId": "test-vpc", "VpcName": self.owner.name,
                                    "Tags": {"Tag": [{"Key": k, "Value": v} for k, v in self.tags.items()]}}]
        with mock.patch.dict(os.environ, {**CONFIG_ENV, "GITHUB_REPOSITORY": "example/plugin"}):
            with mock.patch("cloud_fixture.Cloud", return_value=self.cloud), mock.patch("time.sleep"), mock.patch("cloud_fixture.stopped_run", return_value=True):
                fixture.janitor(self.directory / "recovery", "123", "2")
                self.assertEqual(len(self.cloud.rows["vpc"]), 1)
                fixture.janitor(self.directory / "recovery", "123", "1")
                self.assertEqual(self.cloud.rows["vpc"], [])

    def test_all_cleanup_regions_are_attempted_without_fixture_config(self):
        env = {"RELEASE_ALIYUN_CLEANUP_REGIONS": '["example-1","previous-2"]',
               "RELEASE_ALIYUN_FIXTURE_CONFIG": "broken"}
        with mock.patch.dict(os.environ, env), mock.patch("cloud_fixture.Cloud", side_effect=lambda region: region):
            for error in (RuntimeError("unavailable"), TypeError("malformed tags"), KeyError("missing field")):
                with self.subTest(error=type(error)), mock.patch("cloud_fixture.janitor_region", side_effect=[error, None]) as recover:
                    with self.assertRaisesRegex(RuntimeError, "remaining regions were attempted"):
                        fixture.janitor(self.directory)
                    self.assertEqual([c.args[0] for c in recover.call_args_list], ["example-1", "previous-2"])
            with mock.patch("cloud_fixture.janitor_region", side_effect=KeyboardInterrupt):
                with self.assertRaises(KeyboardInterrupt):
                    fixture.janitor(self.directory)

    def test_unconfirmed_local_writer_prevents_fixture_deletion(self):
        self.owner.state["driver_group"] = 123
        with self.assertRaisesRegex(ValueError, "writer termination is not confirmed"):
            self.owner.cleanup()
        self.assertEqual(self.cloud.calls, [])

    def test_names_fit_ess_limit_and_remain_unique_for_long_run_attempt_and_mode(self):
        names = set()
        for mode in fixture.MODES:
            for run, attempt in (("40652199964", "1"), ("9" * 30, "8" * 30), ("9" * 30, "7" * 30)):
                tags = {**self.tags, "fleeting-scenario": mode, "fleeting-ci-run": run, "fleeting-ci-attempt": attempt}
                name = fixture.name_for(tags)
                self.assertLessEqual(len(name), 64)
                self.assertNotIn(name, names)
                names.add(name)
                self.assertEqual(name, fixture.name_for({**tags, "fleeting-fixture-expires": "other-expiry"}))

    def test_interrupted_spawn_preserves_unknown_writer_and_refuses_local_teardown(self):
        self.owner.tags["fleeting-scenario"] = fixture.MODES[-1]
        self.owner.state["ids"]["group"] = "synthetic-group"
        binary = self.directory / "candidate" / "responsegate" / "plugin"
        binary.parent.mkdir(parents=True)
        binary.write_text("synthetic binary")
        manifest = {"fault_binary": "responsegate/plugin", "candidate_sha": "a" * 40,
                    "files": {"responsegate/plugin": fixture.digest(binary)}}
        def interrupted(*_args, **kwargs):
            for key, field in (("ALIYUN_REGION", "region"), ("ALLOWED_INSTANCE_TYPE", "instance_type"),
                               ("ALLOWED_IMAGE_ID", "image"), ("ALLOWED_ZONE_ID", "zone")):
                self.assertEqual(kwargs["env"][key], CONFIG[field])
            self.assertEqual(self.owner.state["driver_group"], "starting")
            raise KeyboardInterrupt()
        with mock.patch("subprocess.Popen", side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                fixture.driver(self.owner, self.directory / "candidate", manifest, "192.0.2.1/32", CONFIG)
        with self.assertRaisesRegex(ValueError, "writer termination is not confirmed"):
            self.owner.cleanup()
        self.assertEqual(self.cloud.calls, [])

    @unittest.skipUnless(shutil.which("aliyun"), "pinned CLI is installed by CI for offline metadata checks")
    def test_fixture_requests_match_pinned_official_cli_metadata_without_credentials(self):
        version = subprocess.check_output(["aliyun", "version"], text=True)
        self.assertIn("3.5.2", version)
        def keygen(command, **_kwargs):
            path = Path(command[-1])
            path.write_text("synthetic\n")
            path.with_suffix(".pub").write_text("synthetic\n")
        with mock.patch("subprocess.run", side_effect=keygen):
            self.owner.prepare("192.0.2.1/32", CONFIG)
        with mock.patch("time.sleep"):
            self.owner.cleanup()
        def options(value):
            if isinstance(value, dict):
                result = set(value.get("options", []))
                if "name" in value:
                    result.add("--" + value["name"])
                return result.union(*(options(v) for v in value.values()))
            if isinstance(value, list):
                return set().union(*(options(v) for v in value))
            return set()
        for service, action, params in self.cloud.calls:
            metadata = json.loads(subprocess.check_output(["aliyun", service, action, "--help-all", "--cli-output", "json"], text=True))
            allowed = options(metadata["parameterSets"]["camel"])
            for key in params:
                flag = re.sub(r"\.[0-9]+(?=\.|$)", ".#", "--" + key)
                self.assertIn(flag, allowed, (service, action, flag))


class IdentityAndInventoryTests(unittest.TestCase):
    def test_preflight_flags_cannot_be_mistaken_for_read_only_mutations(self):
        for operation in ("run", "janitor", "check"):
            for flag in ("--read-only", "--cleanup"):
                result = subprocess.run([sys.executable, fixture.__file__, operation, flag], capture_output=True, text=True)
                self.assertEqual(result.returncode, 2)
                self.assertIn("apply only to preflight", result.stderr)

    def environment(self):
        return {"RELEASE_CLOUD_ACCEPTANCE_ENABLED": "true", "GITHUB_REF": "refs/heads/main",
                "RELEASE_ALIYUN_ACCOUNT_ID": "1234567890123456",
                "RELEASE_ALIYUN_ROLE_ARN": "acs:ram::1234567890123456:role/test",
                "RELEASE_ALIYUN_CLEANUP_ROLE_ARN": "acs:ram::1234567890123456:role/cleanup",
                "RELEASE_ALIYUN_OIDC_PROVIDER_ARN": "acs:ram::1234567890123456:oidc-provider/github"}

    def test_only_explicit_separate_account_config_is_accepted(self):
        env = self.environment()
        self.assertEqual(fixture.preflight(env), "1234567890123456")
        self.assertEqual(fixture.preflight({k: v for k, v in env.items() if k != "RELEASE_ALIYUN_CLEANUP_ROLE_ARN"}), "1234567890123456")
        for key, value in (("RELEASE_CLOUD_ACCEPTANCE_ENABLED", "false"), ("RELEASE_ALIYUN_ACCOUNT_ID", ""),
                           ("RELEASE_ALIYUN_ROLE_ARN", "acs:ram::9999999999999999:role/old"),
                           ("GITHUB_REF", "refs/heads/feature"),
                           ("RELEASE_ALIYUN_CLEANUP_ROLE_ARN", "acs:ram::9999999999999999:role/old")):
            with self.subTest(key=key):
                invalid = {**env, key: value}
                with self.assertRaises(ValueError):
                    fixture.preflight(invalid)
        with self.assertRaises(ValueError):
            fixture.preflight({"ALIYUN_ROLE_ARN": env["RELEASE_ALIYUN_ROLE_ARN"]})

    def test_wrong_authenticated_account_is_rejected_before_writes(self):
        env = {**self.environment(), "ALIBABA_CLOUD_ACCESS_KEY_ID": "synthetic", "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "synthetic", "ALIBABA_CLOUD_SECURITY_TOKEN": "synthetic"}
        with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(fixture.Cloud, "call", return_value={"AccountId": "9999999999999999"}) as call:
            with self.assertRaisesRegex(ValueError, "authenticated account differs"):
                fixture.Cloud(CONFIG["region"])
            self.assertEqual(call.call_count, 1)
            self.assertEqual(call.call_args.args, ("sts", "GetCallerIdentity"))

    def test_disabled_read_only_identity_still_rejects_foreign_accounts_and_writes(self):
        env = {**self.environment(), "RELEASE_CLOUD_ACCEPTANCE_ENABLED": "false",
               "ALIBABA_CLOUD_ACCESS_KEY_ID": "synthetic", "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "synthetic",
               "ALIBABA_CLOUD_SECURITY_TOKEN": "synthetic"}
        with mock.patch.dict(os.environ, env, clear=True), mock.patch("subprocess.run") as run:
            with self.assertRaises(ValueError):
                fixture.Cloud(CONFIG["region"])
            run.assert_not_called()
            run.return_value = subprocess.CompletedProcess([], 0, '{"AccountId":"1234567890123456"}')
            cloud = fixture.Cloud(CONFIG["region"], read_only=True)
            self.assertEqual(run.call_count, 1)
            for service, action in (("ecs", "RunInstances"), ("ecs", "DeleteInstance"), ("ess", "ScaleWithAdjustment")):
                with self.assertRaisesRegex(ValueError, "only identity"):
                    cloud.call(service, action)
            self.assertEqual(run.call_count, 1)
            run.return_value = subprocess.CompletedProcess([], 0, '{"AccountId":"9999999999999999"}')
            with self.assertRaisesRegex(ValueError, "authenticated account differs"):
                fixture.Cloud(CONFIG["region"], read_only=True)

    def test_configuration_check_only_reads_each_inventory_and_never_enables_acceptance(self):
        env = {**CONFIG_ENV, "RELEASE_CLOUD_ACCEPTANCE_ENABLED": "false"}
        with mock.patch.dict(os.environ, env), mock.patch("cloud_fixture.Cloud") as cloud:
            fixture.check_configuration()
            cloud.assert_called_once_with(CONFIG["region"], read_only=True)
            self.assertEqual([c.args[0] for c in cloud.return_value.inventory.call_args_list], list(fixture.SPECS))
            self.assertEqual(os.environ["RELEASE_CLOUD_ACCEPTANCE_ENABLED"], "false")
            cloud.return_value.call.assert_not_called()

    def test_cli_credentials_are_private_temporary_and_removed_on_all_outcomes(self):
        env = {**self.environment(), **STS_ENV, "ALIBABA_CLOUD_IGNORE_PROFILE": "TRUE",
               "ALIBABACLOUD_IGNORE_PROFILE": "TRUE"}
        with mock.patch.dict(os.environ, env), mock.patch.object(fixture.Cloud, "call", return_value={"AccountId": env["RELEASE_ALIYUN_ACCOUNT_ID"]}):
            cloud = fixture.Cloud(CONFIG["region"], read_only=True)
        paths = []
        for outcome in ("success", "failure", "timeout"):
            with self.subTest(outcome=outcome):
                def run(command, **kwargs):
                    config = Path(command[command.index("--config-path") + 1])
                    paths.append(config)
                    self.assertEqual(config.stat().st_mode & 0o777, 0o600)
                    self.assertEqual(config.parent.stat().st_mode & 0o777, 0o700)
                    profile = json.loads(config.read_text())
                    self.assertEqual(profile["current"], "pre-release")
                    self.assertEqual(profile["profiles"], [cloud.cli_profile])
                    self.assertEqual(command[command.index("--profile") + 1], "pre-release")
                    for value in STS_ENV.values():
                        self.assertNotIn(value, " ".join(command))
                    self.assertEqual(kwargs["env"]["ALIBABA_CLOUD_IGNORE_PROFILE"], "FALSE")
                    self.assertEqual(kwargs["env"]["ALIBABACLOUD_IGNORE_PROFILE"], "FALSE")
                    if outcome == "timeout":
                        raise subprocess.TimeoutExpired(command, 60)
                    return subprocess.CompletedProcess(command, int(outcome == "failure"), '{}', "synthetic private error")
                with mock.patch.dict(os.environ, env), mock.patch("subprocess.run", side_effect=run):
                    if outcome == "success":
                        self.assertEqual(cloud.call("sts", "GetCallerIdentity"), {})
                    else:
                        with self.assertRaises(RuntimeError) as caught:
                            cloud.call("sts", "GetCallerIdentity")
                        self.assertNotIn("synthetic private error", str(caught.exception))
                self.assertFalse(paths[-1].parent.exists())

    @unittest.skipUnless(shutil.which("aliyun"), "pinned CLI is installed by CI for offline authentication checks")
    def test_real_cli_uses_fresh_sts_credentials_against_loopback(self):
        requests = []
        account = self.environment()["RELEASE_ALIYUN_ACCOUNT_ID"]
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                params = parse_qs(urlsplit(self.path).query, keep_blank_values=True)
                params.update(parse_qs(self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode(), keep_blank_values=True))
                requests.append(params)
                body = json.dumps({"AccountId": account}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        certificates = tempfile.TemporaryDirectory(prefix="synthetic-cli-tls-")
        self.addCleanup(certificates.cleanup)
        cert, key = (str(Path(certificates.name) / name) for name in ("cert.pem", "key.pem"))
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=localhost", "-keyout", key, "-out", cert], check=True, capture_output=True)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        real_run = subprocess.run
        cli = shutil.which("aliyun")
        paths = []
        def run(command, **kwargs):
            paths.append(Path(command[command.index("--config-path") + 1]))
            # Only this synthetic loopback test accepts a self-signed certificate.
            kwargs["env"] = {**kwargs["env"], "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"}
            result = real_run([cli, *command[1:], "--endpoint", f"127.0.0.1:{server.server_port}",
                               "--skip-secure-verify"], **kwargs)
            self.assertEqual(result.returncode, 0, result.stderr)
            return result
        try:
            with mock.patch.dict(os.environ, {**self.environment(), **STS_ENV}), mock.patch("subprocess.run", side_effect=run):
                cloud = fixture.Cloud("cn-hangzhou", read_only=True)
            self.assertEqual(cloud.account, account)
            self.assertEqual(len(requests), 1)
            self.assertEqual(requests[0]["AccessKeyId"], [STS_ENV["ALIBABA_CLOUD_ACCESS_KEY_ID"]])
            self.assertEqual(requests[0]["SecurityToken"], [STS_ENV["ALIBABA_CLOUD_SECURITY_TOKEN"]])
            canonical = "&".join(quote(key, safe="~") + "=" + quote(values[0], safe="~")
                                 for key, values in sorted(requests[0].items()) if key != "Signature")
            signed = ("POST&%2F&" + quote(canonical, safe="~")).encode()
            expected = base64.b64encode(hmac.new((STS_ENV["ALIBABA_CLOUD_ACCESS_KEY_SECRET"] + "&").encode(),
                                                signed, hashlib.sha1).digest()).decode()
            self.assertEqual(requests[0]["Signature"], [expected])
            self.assertFalse(paths[0].parent.exists())
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def test_pagination_rejects_incomplete_or_duplicated_inventory(self):
        cloud = object.__new__(fixture.Cloud)
        cloud.region = CONFIG["region"]
        cloud.read_only = False
        for response in ({"Vpcs": {"Vpc": []}}, {"TotalCount": 1, "Vpcs": {"Vpc": []}},
                         {"TotalCount": 2, "Vpcs": {"Vpc": [{"VpcId": "vpc-test"}, {"VpcId": "vpc-test"}]}}):
            with mock.patch.object(cloud, "call", return_value=response):
                with self.assertRaises(ValueError):
                    cloud.inventory("vpc")

    def test_janitor_requires_completed_exact_github_attempt(self):
        tags = fixture.labels("example/plugin", "a" * 40, "123", "1", "cloud-smoke", "2030-01-01T00:00:00Z")
        run = {"id": 123, "run_attempt": 1, "repository": {"full_name": "example/plugin"}, "status": "in_progress"}
        def reply():
            return subprocess.CompletedProcess([], 0, json.dumps(run))
        with mock.patch("subprocess.run", side_effect=lambda *_a, **_kw: reply()):
            self.assertFalse(fixture.stopped_run(tags))
            run["status"] = "completed"
            self.assertTrue(fixture.stopped_run(tags))
            run["run_attempt"] = 2
            with self.assertRaisesRegex(ValueError, "identity differs"):
                fixture.stopped_run(tags)

    def test_cloud_command_errors_never_print_private_request_arguments(self):
        cloud = object.__new__(fixture.Cloud)
        cloud.region = CONFIG["region"]
        cloud.read_only = False
        cloud.cli_profile = {"name": "synthetic", "mode": "StsToken"}
        sensitive = "synthetic-host-private-key-and-access-key-and-token"
        def fail(command, **_kwargs):
            raise subprocess.TimeoutExpired(command, 60, output=sensitive, stderr=sensitive)
        with mock.patch("subprocess.run", side_effect=fail):
            try:
                cloud.call("ecs", "CreateLaunchTemplate", UserData=sensitive, SyntheticAccessKey=sensitive, SyntheticToken=sensitive)
            except RuntimeError as error:
                rendered = "".join(traceback.format_exception(error))
                self.assertNotIn(sensitive, rendered)
                self.assertNotIn("TimeoutExpired", rendered)
                self.assertIn("cloud operation unavailable: ecs/CreateLaunchTemplate", rendered)
            else:
                self.fail("timed-out request was accepted")

    def test_force_delete_requires_complete_unambiguous_owned_members(self):
        row = {"InstanceId": "i-owned", "ScalingGroupId": "group-owned"}
        valid = {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [row]}}
        self.assertEqual(fixture.verified_members(valid, "group-owned", {"i-owned"}), {"i-owned"})
        malformed = [{"TotalCount": False, "ScalingInstances": {"ScalingInstance": []}}, {"TotalCount": 0},
                     {"TotalCount": 0, "ScalingInstances": {}},
                     {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [row, row]}},
                     {"TotalCount": 2, "ScalingInstances": {"ScalingInstance": [row, row]}},
                     {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [{"InstanceId": "i-owned"}]}},
                     {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [{**row, "ScalingGroupId": "foreign"}]}},
                     {"TotalCount": 1, "ScalingInstances": {"ScalingInstance": [{**row, "InstanceId": ""}]}}]
        for response in malformed:
            with self.subTest(response=response):
                with self.assertRaises(ValueError):
                    fixture.verified_members(response, "group-owned", {"i-owned"})
        with self.assertRaises(ValueError):
            fixture.verified_members(valid, "group-owned", set())

    def test_process_exit_proof_requires_linux_procfs(self):
        with mock.patch("cloud_fixture.sys.platform", "darwin"), mock.patch("os.killpg") as kill:
            with self.assertRaisesRegex(RuntimeError, "requires Linux procfs"):
                fixture.stop_process_group(mock.Mock(pid=123))
            kill.assert_not_called()


if __name__ == "__main__":
    unittest.main()
