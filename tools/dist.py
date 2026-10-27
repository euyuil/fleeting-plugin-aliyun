#!/usr/bin/env python3
"""Build native archives and the directory consumed by fleeting-artifact."""
import hashlib
import gzip
import json
import os
import pathlib
import shutil
import subprocess
import tarfile

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
for arch in ("amd64", "arm64"):
    folder = dist / "packages" / "linux" / arch
    folder.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    binary = folder / "plugin"
    subprocess.run(["go", "build", "-trimpath", "-ldflags", ldflags, "-o", str(binary), "./cmd/fleeting-plugin-aliyun"], cwd=root, env=env, check=True)
    shutil.copyfile(root / "LICENSE", folder / "LICENSE")
    shutil.copytree(root / "LICENSES", folder / "LICENSES", dirs_exist_ok=True)
    shutil.copytree(licenses, folder / "_licenses", dirs_exist_ok=True)
    shutil.copyfile(dist / "dependencies.json", folder / "dependencies.json")
    archive = dist / f"fleeting-plugin-aliyun_{version}_linux_{arch}.tar.gz"
    epoch = int(subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], text=True).strip())
    def normalized(info):
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mtime = epoch
        info.mode = 0o755 if info.isdir() or info.name == "fleeting-plugin-aliyun" else 0o644
        return info
    with archive.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed, tarfile.open(fileobj=compressed, mode="w") as out:
        out.add(binary, arcname="fleeting-plugin-aliyun", filter=normalized)
        for name in ("LICENSE", "LICENSES", "_licenses", "dependencies.json"):
            out.add(folder / name, arcname=name, filter=normalized)
    checksums.append(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name)
(dist / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
