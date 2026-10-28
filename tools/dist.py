#!/usr/bin/env python3
"""Build native archives and the directory consumed by fleeting-artifact."""
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
from debian_package import build as build_debian
from package_layout import stage_oci_notices, write_archive

root = pathlib.Path(__file__).resolve().parents[1]
version = os.environ.get("VERSION", "0.1.0-dev")
revision = os.environ.get("REVISION") or subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
built_at = os.environ.get("BUILT_AT") or subprocess.check_output(["git", "show", "-s", "--format=%cI", "HEAD"], text=True).strip()
subprocess.run(["go", "run", "./tools/distribution", "-validate-version", version], cwd=root, check=True)
dist = root / "dist"
dist.mkdir(exist_ok=True)
ldflags = f"-X main.version={version} -X main.revision={revision} -X main.builtAt={built_at}"
# Include all dependency licenses from selected module versions, including bundled code notices.
modules = [json.loads(line) for line in subprocess.check_output(["go", "list", "-m", "-json", "all"], text=True).replace("\n}\n", "\n}\x00").split("\x00") if line.strip()]
licenses = dist / "_licenses"
if licenses.exists():
    shutil.rmtree(licenses)
licenses.mkdir(exist_ok=True)
compiled = set()
for architecture in ("amd64", "arm64"):
    env = dict(os.environ, GOOS="linux", GOARCH=architecture, CGO_ENABLED="0")
    output = subprocess.check_output(["go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./cmd/fleeting-plugin-aliyun"], text=True, env=env)
    compiled.update(output.split())
inventory = []
for module in modules:
    if module.get("Main") or module["Path"] not in compiled:
        continue
    directory = pathlib.Path(module.get("Dir", ""))
    names = []
    if directory.is_dir():
        for source in sorted(directory.iterdir()):
            if source.is_file() and source.name.lower().startswith(("license", "copying", "notice", "copyright")):
                folder = licenses / (module["Path"].replace("/", "_") + "_" + module["Version"])
                folder.mkdir(exist_ok=True)
                shutil.copyfile(source, folder / source.name)
                names.append(str((folder / source.name).relative_to(dist)))
    if not names:
        raise SystemExit("missing dependency license: " + module["Path"])
    inventory.append({"module": module["Path"], "version": module["Version"], "licenses": names, "source_archive": "https://proxy.golang.org/" + "".join("!" + char.lower() if char.isupper() else char for char in module["Path"]) + "/@v/" + module["Version"] + ".zip"})
goroot = pathlib.Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
stdlib_license = licenses / "Go"
stdlib_license.mkdir(exist_ok=True)
for name in ("LICENSE", "PATENTS"):
    source = next((path for path in (goroot / name, goroot.parent / name) if path.is_file()), None)
    if source is None:
        raise SystemExit("missing Go toolchain notice: " + name)
    if name == "LICENSE" and "The Go Authors" not in source.read_text():
        raise SystemExit("cannot identify Go toolchain license")
    shutil.copyfile(source, stdlib_license / name)
(dist / "dependencies.json").write_text(json.dumps(inventory, indent=2) + "\n")
checksums = []
epoch = int(subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], text=True).strip())
notice_entries = [
    ("LICENSE", root / "LICENSE"),
    ("LICENSES", root / "LICENSES"),
    ("_licenses", licenses),
    ("dependencies.json", dist / "dependencies.json"),
]
for arch in ("amd64", "arm64"):
    folder = dist / "packages" / "linux" / arch
    if folder.exists():
        shutil.rmtree(folder)
    folder.mkdir(parents=True)
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    binary = folder / "plugin"
    subprocess.run(["go", "build", "-trimpath", "-ldflags", ldflags, "-o", str(binary), "./cmd/fleeting-plugin-aliyun"], cwd=root, env=env, check=True)
    stage_oci_notices(folder, notice_entries, epoch)
    archive = dist / f"fleeting-plugin-aliyun_{version}_linux_{arch}.tar.gz"
    write_archive(archive, [("fleeting-plugin-aliyun", binary)] + notice_entries, epoch)
    checksums.append(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name)
    if sys.platform == "linux":
        deb = build_debian(root, dist, version, arch, epoch)
        checksums.append(hashlib.sha256(deb.read_bytes()).hexdigest() + "  " + deb.name)
(dist / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
