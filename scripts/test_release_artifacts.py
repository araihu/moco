import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from release_artifacts import validate_chart, validate_manifest


class ReleaseArtifactTests(unittest.TestCase):
    def setUp(self):
        self.lock = {"mocoVersion": "0.0.3", "mocoImage": "ghcr.io/araihu/moco@sha256:"+"a"*64,
                     "mocoControllerImage": "ghcr.io/araihu/moco-controller@sha256:"+"b"*64,
                     "mocoRelease": {"commit": "f"*40}, "mocoCharts": {}}
        self.manifest = {"version": "0.0.3", "tag": "v0.0.3", "commit": "f"*40, "components": []}
        for name, image in zip(["moco", "moco-controller"], [self.lock["mocoImage"], self.lock["mocoControllerImage"]]):
            self.lock["mocoCharts"][name] = {"repository": "oci://ghcr.io/araihu/charts/"+name,
                                          "digest": "sha256:"+"c"*64, "sha256": "d"*64}
            repo, digest = image.split("@")
            self.manifest["components"].append({"name": name, "imageRepository": repo, "imageDigest": digest,
                "chartRepository": self.lock["mocoCharts"][name]["repository"], "chartDigest": "sha256:"+"c"*64,
                "chartSHA256": "d"*64})

    def encoded_manifest(self):
        data = json.dumps(self.manifest).encode()
        self.lock["mocoRelease"]["sha256"] = hashlib.sha256(data).hexdigest()
        return data

    def test_release_identity_and_each_component_pin_are_required(self):
        validate_manifest(self.lock, self.encoded_manifest())
        for field, value in [("version", "0.0.2"), ("commit", "e"*40)]:
            previous = self.manifest[field]
            self.manifest[field] = value
            with self.assertRaises(RuntimeError):
                validate_manifest(self.lock, self.encoded_manifest())
            self.manifest[field] = previous
        self.manifest["components"][1]["imageDigest"] = "sha256:"+"a"*64
        with self.assertRaises(RuntimeError):
            validate_manifest(self.lock, self.encoded_manifest())

    def test_manifest_bytes_must_match_the_reviewed_checksum(self):
        data = self.encoded_manifest()
        with self.assertRaises(RuntimeError):
            validate_manifest(self.lock, data+b" ")

    def chart(self, path, image, crds=True):
        repo, digest = image.split("@")
        entries = {"moco-controller/values.yaml": json.dumps({"image": {"repository": repo, "digest": digest}}).encode()}
        if crds:
            for name in ["mocotenants", "mocovaults", "mocoauthorizations"]:
                entries["moco-controller/crds/moco.araihu.io_"+name+".yaml"] = b"fixture"
        with tarfile.open(path, "w:gz") as archive:
            for name, data in entries.items():
                header = tarfile.TarInfo(name)
                header.size = len(data)
                archive.addfile(header, io.BytesIO(data))
        self.lock["mocoCharts"]["moco-controller"]["sha256"] = hashlib.sha256(path.read_bytes()).hexdigest()

    def test_default_image_and_all_three_crds_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"controller.tgz"
            self.chart(path, self.lock["mocoControllerImage"])
            validate_chart(path, "moco-controller", self.lock)
            self.chart(path, self.lock["mocoImage"])
            with self.assertRaises(RuntimeError):
                validate_chart(path, "moco-controller", self.lock)
            self.chart(path, self.lock["mocoControllerImage"], crds=False)
            with self.assertRaises(RuntimeError):
                validate_chart(path, "moco-controller", self.lock)
            path.write_bytes(path.read_bytes()+b"tampered")
            with self.assertRaises(RuntimeError):
                validate_chart(path, "moco-controller", self.lock)


if __name__ == "__main__":
    unittest.main()
