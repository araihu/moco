import hashlib
import io
import os
from pathlib import Path
import tarfile
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from linter import install


class ReleaseCacheTest(unittest.TestCase):
    def test_warm_cache_verifies_and_repairs_binary(self):
        with TemporaryDirectory() as root:
            archive = Path(root, "test.tar.gz")
            contents = b"pinned binary"
            with tarfile.open(archive, "w:gz") as output:
                member = tarfile.TarInfo("vacuum")
                member.size = len(contents)
                output.addfile(member, io.BytesIO(contents))
            release = {
                "url": archive.as_uri(),
                "sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
                "member": "vacuum",
                "binary_sha256": hashlib.sha256(contents).hexdigest(),
            }
            with (patch.dict(os.environ, {"XDG_CACHE_HOME": root}),
                  patch("linter.platform.system", return_value="Linux"),
                  patch("linter.platform.machine", return_value="aarch64")):
                platforms = {"linux/arm64": release}
                binary = install("vacuum", platforms)
                for corrupted in (b"", b"x" * len(contents)):
                    binary.write_bytes(corrupted)
                    self.assertEqual(install("vacuum", platforms).read_bytes(), contents)
                archive.unlink()
                self.assertEqual(install("vacuum", platforms), binary)
