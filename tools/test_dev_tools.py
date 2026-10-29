"""Reject corrupted or ambiguous downloaded development executables."""
import hashlib
import io
import tarfile
import unittest

from dev_tools import executable


class DevelopmentToolTests(unittest.TestCase):
    def archive(self, members):
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:gz") as archive:
            for name, kind in members:
                member = tarfile.TarInfo(name)
                member.type = kind
                if kind == tarfile.REGTYPE:
                    member.size = 4
                    archive.addfile(member, io.BytesIO(b"tool"))
                else:
                    archive.addfile(member)
        data = output.getvalue()
        return data, hashlib.sha256(data).hexdigest()

    def test_only_verified_bytes_are_returned_without_extraction(self):
        data, digest = self.archive([("nested/tool", tarfile.REGTYPE)])
        self.assertEqual(executable(data, digest, "tool"), b"tool")
        with self.assertRaisesRegex(ValueError, "checksum"):
            executable(data + b"tampered", digest, "tool")

    def test_missing_duplicate_and_linked_executables_are_rejected(self):
        for members in ([], [("tool", tarfile.REGTYPE), ("nested/tool", tarfile.REGTYPE)],
                        [("tool", tarfile.SYMTYPE)]):
            data, digest = self.archive(members)
            with self.subTest(members=members), self.assertRaisesRegex(ValueError, "exactly one"):
                executable(data, digest, "tool")
