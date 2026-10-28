"""Create reproducible archives and a flat payload for the pinned OCI installer."""
import gzip
import pathlib
import shutil
import tarfile


def write_archive(destination, entries, epoch):
    def normalized(info):
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mtime = epoch
        info.mode = 0o755 if info.isdir() or info.name == "fleeting-plugin-aliyun" else 0o644
        return info

    with pathlib.Path(destination).open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                for name, source in entries:
                    archive.add(source, arcname=name, filter=normalized)


def stage_oci_notices(folder, entries, epoch):
    # The pinned installer skips directory entries and cannot create nested files.
    write_archive(folder / "licenses.tar.gz", entries, epoch)
    shutil.copyfile(dict(entries)["LICENSE"], folder / "LICENSE")
    (folder / "NOTICE").write_text(
        "License texts, retained notices, and the dependency source inventory\n"
        "are included in licenses.tar.gz. Extract them with:\n\n"
        "  tar -xzf licenses.tar.gz\n\n"
        "Dependency source archive URLs are recorded in dependencies.json.\n"
    )
