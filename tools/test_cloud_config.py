"""Keep private deployment configuration external, strict, and recoverable offline."""
import json
import unittest

from cloud_config import cleanup_regions, config_digest, fixture_config

# Synthetic configuration deliberately differs from any deployed test account.
CONFIG = {"region": "example-1", "zone": "example-1a", "instance_type": "ecs.synthetic.small",
          "image": "synthetic-image", "disk_category": "cloud_essd", "disk_size_gib": 40,
          "disk_performance_level": "PL1", "internet_max_bandwidth_out": 1, "spot_duration": 0,
          "vpc_cidr": "172.20.0.0/16", "vswitch_cidr": "172.20.2.0/24", "swap_mib": 256}
CONFIG_ENV = {"RELEASE_ALIYUN_FIXTURE_CONFIG": json.dumps(CONFIG),
              "RELEASE_ALIYUN_CLEANUP_REGIONS": json.dumps([CONFIG["region"]])}


class CloudConfigurationTests(unittest.TestCase):
    def test_explicit_configuration_has_no_deployed_defaults(self):
        self.assertEqual(fixture_config(CONFIG_ENV), CONFIG)
        for field in CONFIG:
            config = {k: v for k, v in CONFIG.items() if k != field}
            with self.subTest(field=field), self.assertRaises(ValueError):
                fixture_config({**CONFIG_ENV, "RELEASE_ALIYUN_FIXTURE_CONFIG": json.dumps(config)})
        with self.assertRaises(ValueError):
            fixture_config({})
        with self.assertRaises(ValueError):
            fixture_config({"ALIYUN_REGION": "legacy-1"})

    def test_malformed_ambiguous_and_unsafe_config_is_rejected(self):
        for raw in ('null', '[]', '{"region":"example-1","region":"other-1"}', 'bad json'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                fixture_config({**CONFIG_ENV, "RELEASE_ALIYUN_FIXTURE_CONFIG": raw})
        invalid = {"unknown": "extra", "region": "other-1", "zone": "other-1a", "instance_type": "--option",
                   "image": "$(command)", "disk_size_gib": True, "swap_mib": "512", "spot_duration": 2,
                   "internet_max_bandwidth_out": 100, "disk_category": "unknown", "disk_performance_level": "",
                   "vpc_cidr": "203.0.113.0/24", "vswitch_cidr": "172.21.0.0/24"}
        for key, value in invalid.items():
            with self.subTest(key=key), self.assertRaises(ValueError):
                fixture_config({**CONFIG_ENV, "RELEASE_ALIYUN_FIXTURE_CONFIG": json.dumps({**CONFIG, key: value})})

    def test_cleanup_keeps_previous_regions_and_ignores_broken_fixture_config(self):
        env = {**CONFIG_ENV, "RELEASE_ALIYUN_FIXTURE_CONFIG": "invalid",
               "RELEASE_ALIYUN_CLEANUP_REGIONS": '["example-1","previous-2"]'}
        self.assertEqual(cleanup_regions(env), ["example-1", "previous-2"])
        for raw in ('[]', '["example-1","example-1"]', '[false]', '{}', '["../unsafe"]'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                cleanup_regions({"RELEASE_ALIYUN_CLEANUP_REGIONS": raw})

    def test_non_essd_has_no_performance_level_and_digest_binds_settings(self):
        config = {**CONFIG, "disk_category": "cloud_ssd", "disk_performance_level": ""}
        self.assertEqual(fixture_config({**CONFIG_ENV, "RELEASE_ALIYUN_FIXTURE_CONFIG": json.dumps(config)}), config)
        self.assertNotEqual(config_digest(CONFIG), config_digest(config))
        self.assertEqual(config_digest(CONFIG), config_digest(dict(reversed(list(CONFIG.items())))))


if __name__ == "__main__":
    unittest.main()
