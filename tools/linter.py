"""Run a pinned release binary, downloading it into the user cache on demand."""

import argparse
import hashlib
import io
import json
import os
import platform
from pathlib import Path
import shutil
import subprocess
import tarfile
from tempfile import TemporaryDirectory
from urllib.request import urlopen
import zipfile


def binary_matches(binary, expected):
    if binary.is_symlink() or not binary.is_file() or not os.access(binary, os.X_OK):
        return False
    digest = hashlib.sha256()
    with binary.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest() == expected


def install(name, platforms):
    system = platform.system().lower()
    machine = platform.machine().lower()
    arch = {"aarch64": "arm64", "x86_64": "amd64"}.get(machine, machine)
    release = platforms.get(f"{system}/{arch}")
    if release is None:
        raise ValueError(f"no precompiled {name} for {system}/{arch}")

    cache_home = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache").resolve()
    target_dir = cache_home / "moco" / "linters" / release["sha256"]
    binary = target_dir / Path(release["member"]).name
    if binary_matches(binary, release["binary_sha256"]):
        return binary

    target_dir.mkdir(parents=True, exist_ok=True)
    with TemporaryDirectory(dir=target_dir) as staging:
        with urlopen(release["url"], timeout=60) as response:
            data = response.read()
        if hashlib.sha256(data).hexdigest() != release["sha256"]:
            raise ValueError(f"SHA-256 mismatch for {release['url']}")
        pending = Path(staging) / binary.name
        with pending.open("wb") as output:
            if release["url"].endswith(".zip"):
                with zipfile.ZipFile(io.BytesIO(data)) as archive, archive.open(release["member"]) as executable:
                    shutil.copyfileobj(executable, output)
            else:
                with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
                    member = archive.getmember(release["member"])
                    if not member.isfile():
                        raise ValueError(f"{release['member']} is not a regular file")
                    with archive.extractfile(member) as executable:
                        shutil.copyfileobj(executable, output)
        pending.chmod(0o755)
        if not binary_matches(pending, release["binary_sha256"]):
            raise ValueError(f"SHA-256 mismatch for {release['member']}")
        try:
            pending.replace(binary)
        except PermissionError:
            # Windows may lock a cached binary installed and launched by another invocation.
            if not binary_matches(binary, release["binary_sha256"]):
                raise
    return binary


def main():
    releases = json.loads(Path(__file__).with_name("linters.json").read_text(encoding="utf-8"))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--path", action="store_true", help="print the installed path")
    parser.add_argument("name", choices=releases)
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        binary = install(args.name, releases[args.name])
    except (OSError, ValueError, KeyError, tarfile.TarError, zipfile.BadZipFile) as error:
        parser.exit(1, f"{args.name}: {error}\n")
    if args.path:
        print(binary)
        return 0
    if os.name == "nt":
        return subprocess.call([str(binary), *args.args])
    os.execv(binary, [str(binary), *args.args])


if __name__ == "__main__":
    raise SystemExit(main())
