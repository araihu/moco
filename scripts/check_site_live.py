#!/usr/bin/env python3
"""Compare every deployed file to the verified local publication."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
from pathlib import Path
import subprocess
from urllib.parse import quote, urlsplit

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--root", type=Path, default=Path("dist/site"))
parser.add_argument("--origin", default="https://moco.araihu.com")
parser.add_argument("--resolve", help="Public IPv4 address to bypass stale DNS")
args = parser.parse_args()
origin = args.origin.rstrip("/")
curl = ["curl", "--silent", "--show-error", "--location", "--max-time", "30",
        "--connect-timeout", "10", "--retry", "2", "--retry-delay", "1",
        "--retry-all-errors"]
if args.resolve:
    curl += ["--resolve", f"{urlsplit(origin).hostname}:443:{args.resolve}"]


def verify(path):
    relative = path.relative_to(args.root).as_posix()
    result = subprocess.run(curl + ["--fail", origin + "/" + quote(relative)], capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{relative}: curl exit {result.returncode}")
    if hashlib.sha256(result.stdout).digest() != hashlib.sha256(path.read_bytes()).digest():
        raise RuntimeError(f"{relative}: deployed bytes differ")


files = sorted(path for path in args.root.rglob("*") if path.is_file() and path.name != "_headers")
with ThreadPoolExecutor(max_workers=8) as pool:
    for count, _ in enumerate(pool.map(verify, files), 1):
        if count % 100 == 0:
            print(f"Checked {count}/{len(files)} deployed files", flush=True)

for route in ("/does-not-exist", "/openapi/does-not-exist.json"):
    result = subprocess.run(curl + ["--output", "/dev/null", "--write-out", "%{http_code}", origin + route],
                            capture_output=True, text=True, check=True)
    if result.stdout != "404":
        raise RuntimeError(f"{route}: expected 404, got {result.stdout}")
for route, expected in (
    ("/site/assets/social.png", "image/png"),
    ("/openapi/sw.js", "text/javascript"),
    ("/media/architecture.svg", "image/svg+xml"),
):
    result = subprocess.run(curl + ["--fail", "--output", "/dev/null", "--write-out",
                                    "%{content_type}", origin + route],
                            capture_output=True, text=True, check=True)
    if result.stdout.split(";")[0] != expected:
        raise RuntimeError(f"{route}: incorrect MIME type {result.stdout}")
print(f"Verified {len(files)} deployed files by SHA-256, MIME types, and real 404s.")
