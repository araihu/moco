#!/usr/bin/env python3
"""Compare every deployed file to the verified local publication."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
from pathlib import Path
import subprocess
import time
from urllib.parse import quote, urlsplit

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--root", type=Path, default=Path("dist/site"))
parser.add_argument("--origin", default="https://moco.araihu.com")
parser.add_argument("--resolve", help="Public IPv4 address to bypass stale DNS")
parser.add_argument("--wait-seconds", type=int, default=300, help="Bounded wait for DNS, TLS and the deployed home page")
args = parser.parse_args()
origin = args.origin.rstrip("/")
curl = ["curl", "--silent", "--show-error", "--location", "--max-time", "30",
        "--connect-timeout", "10", "--retry", "2", "--retry-delay", "1",
        "--retry-all-errors"]
if args.resolve:
    curl += ["--resolve", f"{urlsplit(origin).hostname}:443:{args.resolve}"]


# New custom domains need time for DNS/TLS and deployment propagation. Wait once
# before starting the full inventory; never skip TLS or accept different bytes.
expected_home = hashlib.sha256((args.root / "index.html").read_bytes()).digest()
deadline = time.monotonic() + max(args.wait_seconds, 0)
while True:
    ready = subprocess.run(curl + ["--fail", origin + "/"], capture_output=True)
    if ready.returncode == 0 and hashlib.sha256(ready.stdout).digest() == expected_home:
        break
    if time.monotonic() >= deadline:
        detail = ready.stderr.decode(errors="replace").strip()[-500:]
        raise RuntimeError(f"Deployment not ready at {origin}: curl {ready.returncode}; {detail or 'home bytes differ'}")
    print("Waiting for DNS, TLS and the expected deployment...", flush=True)
    time.sleep(min(5, max(0, deadline - time.monotonic())))


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
