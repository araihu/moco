"""Fetch immutable release identity and checksum-verified OCI charts for acceptance."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import urllib.request


def validate_manifest(lock, data):
    pin = lock["mocoRelease"]
    if hashlib.sha256(data).hexdigest() != pin["sha256"]:
        raise RuntimeError("release manifest checksum differs")
    manifest = json.loads(data)
    if (manifest["version"] != lock["mocoVersion"] or manifest["commit"] != pin["commit"]
            or manifest["tag"] != "v"+lock["mocoVersion"]):
        raise RuntimeError("release manifest identity differs")
    components = manifest["components"]
    if [c["name"] for c in components] != ["moco", "moco-controller"]:
        raise RuntimeError("release must contain server and controller components")
    for component, image in zip(components, [lock["mocoImage"], lock["mocoControllerImage"]]):
        pin = lock["mocoCharts"][component["name"]]
        if (component["imageRepository"]+"@"+component["imageDigest"] != image
                or component["chartRepository"] != pin["repository"]
                or component["chartDigest"] != pin["digest"]
                or component["chartSHA256"] != pin["sha256"]):
            raise RuntimeError("release component differs from the reviewed pins")
    return manifest


def validate_chart(path, name, lock):
    if hashlib.sha256(path.read_bytes()).hexdigest() != lock["mocoCharts"][name]["sha256"]:
        raise RuntimeError("published chart archive checksum differs")
    image = lock["mocoImage"] if name == "moco" else lock["mocoControllerImage"]
    with tarfile.open(path) as archive:
        # The release helper writes values.yaml as JSON (a YAML subset).
        # Read the pinned archive without extracting files or parsing arbitrary YAML.
        values = json.load(archive.extractfile(name+"/values.yaml"))
        if values["image"]["repository"]+"@"+values["image"]["digest"] != image:
            raise RuntimeError("published chart image default differs from the released image")
        if name == "moco-controller":
            for resource in ["mocotenants", "mocovaults", "mocoauthorizations"]:
                try:
                    member = archive.getmember(name+"/crds/moco.araihu.io_"+resource+".yaml")
                except KeyError as error:
                    raise RuntimeError("controller chart is missing a required CRD") from error
                if not member.isfile():
                    raise RuntimeError("controller chart is missing a required CRD")


def published_charts(lock, directory, helm):
    with urllib.request.urlopen(lock["mocoRelease"]["url"], timeout=90) as response:
        validate_manifest(lock, response.read())
    result = {}
    for name, pin in lock["mocoCharts"].items():
        output = directory/name
        output.mkdir()
        process = subprocess.run([helm, "pull", pin["repository"], "--version", pin["version"],
                                  "--destination", str(output)], capture_output=True, timeout=120)
        if process.returncode:
            raise RuntimeError("published OCI chart download failed: "+process.stderr.decode(errors="replace"))
        digest = re.search(r"(?m)^Digest: (sha256:[a-f0-9]{64})\s*$", (process.stdout+process.stderr).decode())
        if digest is None or digest[1] != pin["digest"]:
            raise RuntimeError("published OCI chart digest differs")
        path = output/(name+"-"+pin["version"]+".tgz")
        validate_chart(path, name, lock)
        result[name] = Path(path)
        print(json.dumps({"publishedChart": name, "version": pin["version"], "digest": pin["digest"],
                          "sha256": pin["sha256"], "verified": True}), flush=True)
    return result
