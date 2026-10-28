"""Validate deployment-specific acceptance settings without cloud access or defaults."""
import hashlib
import ipaddress
import json
import os
import re


def decode(name, env):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate configuration key")
            result[key] = value
        return result
    try:
        return json.loads(env.get(name, ""), object_pairs_hook=unique)
    except (ValueError, TypeError):
        raise ValueError(name + " must contain valid JSON with no duplicate keys") from None


def valid_region(value):
    return isinstance(value, str) and re.fullmatch(r"[a-z][a-z0-9-]{1,31}", value)


def cleanup_regions(env=os.environ):
    regions = decode("RELEASE_ALIYUN_CLEANUP_REGIONS", env)
    if not isinstance(regions, list) or not 1 <= len(regions) <= 10 or not all(valid_region(r) for r in regions) or len(set(regions)) != len(regions):
        raise ValueError("RELEASE_ALIYUN_CLEANUP_REGIONS requires 1 to 10 distinct region IDs")
    return regions


def fixture_config(env=os.environ):
    config = decode("RELEASE_ALIYUN_FIXTURE_CONFIG", env)
    fields = {"region", "zone", "instance_type", "image", "disk_category", "disk_size_gib",
              "disk_performance_level", "internet_max_bandwidth_out", "spot_duration",
              "vpc_cidr", "vswitch_cidr", "swap_mib"}
    if not isinstance(config, dict) or set(config) != fields:
        raise ValueError("RELEASE_ALIYUN_FIXTURE_CONFIG requires exactly the documented fields")
    patterns = {"zone": r"[a-z][a-z0-9-]{1,40}", "instance_type": r"ecs\.[a-zA-Z0-9.-]+",
                "image": r"[A-Za-z0-9][A-Za-z0-9_.-]{1,127}"}
    if not valid_region(config["region"]) or any(not isinstance(config[k], str) or not re.fullmatch(p, config[k]) for k, p in patterns.items()):
        raise ValueError("invalid region, zone, instance type or immutable image ID")
    if not re.fullmatch(re.escape(config["region"]) + r"(?:[a-z]|-[a-z0-9-]+)", config["zone"]) or config["region"] not in cleanup_regions(env):
        raise ValueError("fixture region must match its zone and be included in cleanup regions")
    # These limits retain the live driver's bounded traffic and strict Spot policy.
    bounds = {"disk_size_gib": (20, 2048), "internet_max_bandwidth_out": (1, 1),
              "spot_duration": (0, 1), "swap_mib": (0, 2048)}
    if any(type(config[k]) is not int or not low <= config[k] <= high for k, (low, high) in bounds.items()):
        raise ValueError("disk, bandwidth, Spot duration and swap require integers within documented bounds")
    category, level = config["disk_category"], config["disk_performance_level"]
    if category not in ("cloud_efficiency", "cloud_ssd", "cloud_essd", "cloud_essd_entry", "cloud_auto") or not isinstance(level, str):
        raise ValueError("unsupported system disk category or performance level")
    if (category == "cloud_essd" and level not in ("PL0", "PL1", "PL2", "PL3")) or (category != "cloud_essd" and level != ""):
        raise ValueError("ESSD requires an explicit performance level; other disk categories require an empty level")
    try:
        vpc = ipaddress.IPv4Network(config["vpc_cidr"], strict=True)
        subnet = ipaddress.IPv4Network(config["vswitch_cidr"], strict=True)
        private = [ipaddress.IPv4Network(c) for c in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")]
        if not 8 <= vpc.prefixlen <= 28 or not 16 <= subnet.prefixlen <= 29 or not subnet.subnet_of(vpc) or not any(vpc.subnet_of(p) for p in private):
            raise ValueError()
        if str(vpc) != config["vpc_cidr"] or str(subnet) != config["vswitch_cidr"]:
            raise ValueError()
    except (ValueError, TypeError):
        raise ValueError("VPC/vSwitch must be canonical private IPv4 CIDRs with the vSwitch inside the VPC") from None
    return config


def config_digest(config):
    return hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
