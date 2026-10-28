#!/usr/bin/env python3
"""Parse examples and verify finite autoscaler limits and executor boundaries."""
import pathlib
import argparse
import subprocess
import tempfile
import tomllib
parser = argparse.ArgumentParser()
parser.add_argument("--runner")
args = parser.parse_args()
for path in pathlib.Path("examples").glob("*/config.toml"):
    with path.open("rb") as stream:
        document = tomllib.load(stream)
    runner = document["runners"][0]
    config = runner["autoscaler"]
    assert document["concurrent"] == 1
    assert config["max_instances"] == 1
    assert config["capacity_per_instance"] == 1
    assert config["max_use_count"] == 1
    assert config["connector_config"]["use_static_credentials"] is True
    assert set(config["plugin_config"]) == {"region", "scaling_group_id"}
    if runner["shell"] == "sh":
        assert runner["feature_flags"]["FF_POSIXLY_CORRECT_ESCAPES"] is True
    if args.runner:
        with tempfile.TemporaryDirectory() as folder:
            config = pathlib.Path(folder) / "config.toml"
            config.write_bytes(path.read_bytes())
            (pathlib.Path(folder) / ".runner_system_id").write_text("s_000000000000")
            result = subprocess.run([str(pathlib.Path(args.runner).resolve()), "list", "--config", str(config)], capture_output=True, text=True)
            if result.returncode != 0:
                raise SystemExit("pinned Runner rejected " + str(path))
    print(str(path) + ": PASS")
