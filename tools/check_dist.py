"""Verify every declared distribution checksum without executing target binaries."""
import argparse
import hashlib
from pathlib import Path
import re


def check(directory):
    seen = set()
    for line in (directory / "SHA256SUMS").read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+\.(?:tar\.gz|deb))", line)
        if match is None or match[2] in seen:
            raise ValueError("invalid or duplicate distribution checksum entry")
        seen.add(match[2])
        path = directory / match[2]
        if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != match[1]:
            raise ValueError("distribution checksum mismatch: " + match[2])
    if not seen:
        raise ValueError("distribution checksum inventory is empty")
    actual = {p.name for p in directory.iterdir() if p.name.endswith((".tar.gz", ".deb"))}
    if actual != seen:
        raise ValueError("distribution contains unlisted archives or packages")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    check(parser.parse_args().directory)
    print("PASS: complete distribution checksum inventory")
