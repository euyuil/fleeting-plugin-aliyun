"""Build Debian packages from the exact native/OCI binaries with dpkg-deb."""
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


def debian_version(version):
    # Debian '~' sorts release candidates before the final release.
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("invalid package version")
    return version.replace("-", "~", 1)


def build(root, dist, version, arch, epoch):
    if arch not in ("amd64", "arm64"):
        raise ValueError("unsupported Debian architecture")
    if shutil.which("dpkg-deb") is None:
        raise RuntimeError("dpkg-deb is required to build Debian packages (install dpkg-dev)")
    package_version = debian_version(version)
    output = dist / f"fleeting-plugin-aliyun_{package_version}_{arch}.deb"
    with tempfile.TemporaryDirectory(prefix="fleeting-deb-") as temporary:
        stage = Path(temporary)
        metadata = stage / "DEBIAN"
        metadata.mkdir()
        (metadata / "control").write_text(
            "Package: fleeting-plugin-aliyun\n"
            f"Version: {package_version}\nArchitecture: {arch}\n"
            "Maintainer: Liu Yue <euyuil@gmail.com>\n"
            "Section: admin\nPriority: optional\n"
            "Depends: adduser, passwd, systemd, util-linux, ca-certificates\n"
            "Suggests: gitlab-runner\n"
            "Homepage: https://github.com/euyuil/fleeting-plugin-aliyun\n"
            "Description: Alibaba Cloud Fleeting plugin and local Runner setup\n"
            " Configure a dedicated GitLab Runner service with the bundled plugin.\n"
            " Install GitLab Runner separately; setup never starts jobs automatically.\n"
        )
        for name in ("preinst", "postinst", "prerm", "postrm"):
            shutil.copyfile(root / "packaging/debian" / name, metadata / name)
            (metadata / name).chmod(0o755)
        binary = stage / "usr/bin/fleeting-plugin-aliyun"
        binary.parent.mkdir(parents=True)
        shutil.copyfile(dist / "packages/linux" / arch / "plugin", binary)
        binary.chmod(0o755)
        unit = stage / "usr/lib/systemd/system/gitlab-runner-aliyun.service"
        unit.parent.mkdir(parents=True)
        shutil.copyfile(root / "packaging/debian/gitlab-runner-aliyun.service", unit)
        docs = stage / "usr/share/doc/fleeting-plugin-aliyun"
        docs.mkdir(parents=True)
        for name in ("LICENSE", "LICENSES"):
            source = root / name
            if source.is_dir():
                shutil.copytree(source, docs / name)
            else:
                shutil.copyfile(source, docs / name)
        shutil.copytree(dist / "_licenses", docs / "_licenses")
        shutil.copyfile(dist / "dependencies.json", docs / "dependencies.json")
        for name in ("debian-installation.md", "operations.md"):
            shutil.copyfile(root / "docs" / name, docs / name)
        for mode in ("instance", "docker-autoscaler"):
            example = docs / "examples" / mode / "config.toml"
            example.parent.mkdir(parents=True)
            shutil.copyfile(root / "examples" / mode / "config.toml", example)
        # Normalize metadata for reproducible packages and root-owned payloads.
        for path in sorted(stage.rglob("*")):
            if path.is_dir():
                path.chmod(0o755)
            elif path != binary and path.parent != metadata:
                path.chmod(0o644)
            os.utime(path, (epoch, epoch))
        stage.chmod(0o755)
        os.utime(stage, (epoch, epoch))
        subprocess.run(["dpkg-deb", "--root-owner-group", "-Zgzip", "-z9", "--build", str(stage), str(output)],
                       check=True, env=dict(os.environ, SOURCE_DATE_EPOCH=str(epoch)))
    return output
