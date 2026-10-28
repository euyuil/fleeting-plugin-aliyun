"""Protect full license retention and the flat OCI installer contract."""
import pathlib
import tarfile
import tempfile
import unittest

from package_layout import stage_oci_notices


class PackageLayoutTest(unittest.TestCase):
    def test_nested_notices_are_preserved_in_a_reproducible_flat_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / "source"
            payload = root / "payload"
            payload.mkdir()
            contents = {
                "LICENSE": b"project license\n",
                "LICENSES/retained.txt": b"retained notice\n",
                "_licenses/dependency/LICENSE": b"dependency license\n",
                "_licenses/Go/PATENTS": b"Go patents notice\n",
                "dependencies.json": b'[{"licenses":["_licenses/dependency/LICENSE"]}]\n',
            }
            for name, data in contents.items():
                path = source / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
            (payload / "plugin").write_bytes(b"synthetic executable")
            entries = [(name, source / name) for name in ("LICENSE", "LICENSES", "_licenses", "dependencies.json")]
            stage_oci_notices(payload, entries, 1234567890)
            self.assertEqual({p.name for p in payload.iterdir()}, {"plugin", "LICENSE", "NOTICE", "licenses.tar.gz"})
            self.assertTrue(all(p.is_file() for p in payload.iterdir()))
            archive = payload / "licenses.tar.gz"
            before = archive.read_bytes()
            with tarfile.open(archive) as bundle:
                for name, data in contents.items():
                    self.assertEqual(bundle.extractfile(name).read(), data)
                for member in bundle.getmembers():
                    self.assertEqual((member.uid, member.gid, member.mtime), (0, 0, 1234567890))
            stage_oci_notices(payload, entries, 1234567890)
            self.assertEqual(archive.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
