#!/usr/bin/env python3
"""Create a disposable vind cluster, execute Moco/ESO acceptance, and clean up."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import subprocess
import sys
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "deploy/testing"
LOCK = json.loads((FIXTURES / "versions.json").read_text())


def tool(name):
    system = platform.system().lower()
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    key = f"{system}-{arch}"
    digest = LOCK[name+"SHA256"].get(key)
    if not digest:
        raise RuntimeError("supported hosts are Linux/macOS amd64/arm64")
    version = LOCK[name]
    path = ROOT / "tools/bin/kubernetes" / f"{name}-{version}-{key}"
    if not path.exists():
        url = (f"https://github.com/loft-sh/vcluster/releases/download/v{version}/vcluster-{key}" if name == "vcluster"
               else f"https://dl.k8s.io/release/v{version}/bin/{system}/{arch}/kubectl")
        print(f"Downloading pinned {name} {version} ({key})", flush=True)
        with urllib.request.urlopen(url, timeout=120) as response:
            data = response.read()
        if hashlib.sha256(data).hexdigest() != digest:
            raise RuntimeError(f"{name} download checksum differs")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        path.chmod(0o755)
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise RuntimeError(f"cached {name} checksum differs")
    return str(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--ops", default=str(ROOT/"bin/moco-ops"))
    parser.add_argument("--keep", action="store_true", help="retain cluster and private workspace for debugging")
    parser.add_argument("--report", type=Path, default=ROOT/"bin/kubernetes-report.json")
    options = parser.parse_args()
    os.umask(0o077)
    name = "moco-eso-e2e-" + secrets.token_hex(3)
    # Docker bind sources are local paths. A remote daemon needs this runner to
    # execute on that daemon's machine, with the same private workspace paths.
    endpoint = subprocess.check_output(["docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"], text=True).strip()
    if endpoint.startswith("ssh://") and not os.environ.get("DOCKER_HOST"):
        raise RuntimeError("run vind-test on the Docker host, or use the vind-eso GitHub Actions workflow; SSH Docker contexts cannot mount the local workspace")
    vcluster, kubectl = tool("vcluster"), tool("kubectl")
    directory = Path(tempfile.mkdtemp(prefix="moco-vind-eso-"))
    config, kubeconfig = directory/"config.json", directory/"kubeconfig"
    environment = dict(os.environ, KUBECONFIG=str(kubeconfig), VCLUSTER_TELEMETRY_DISABLED="true")
    base = [vcluster, "--config", str(config)]
    log = (directory/"bootstrap.log").open("wb")
    success = False
    created = False
    try:
        print("Creating isolated vind cluster "+name, flush=True)
        # An isolated CLI config and --connect=false preserve global contexts.
        created = True
        subprocess.run([*base, "create", name, "--driver", "docker", "--connect=false", "--values", str(FIXTURES/"vind.yaml")],
                       env=environment, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=600)
        with kubeconfig.open("wb") as target:
            subprocess.run([*base, "connect", name, "--driver", "docker", "--print"],
                           env=environment, stdout=target, stderr=log, check=True, timeout=120)
        kubeconfig.chmod(0o600)
        subprocess.run([sys.executable, str(ROOT/"scripts/kubernetes_test.py"), "--kubeconfig", str(kubeconfig),
                        "--cluster-name", name, "--kubectl", kubectl, "--helm", options.helm,
                        "--ops", str(Path(options.ops).resolve()), "--report", str(options.report.resolve())],
                       env=environment, check=True, timeout=1800)
        success = True
    finally:
        log.close()
        if not success:
            diagnostics = ROOT/"bin/vind-diagnostics"
            diagnostics.mkdir(parents=True, exist_ok=True)
            # Report only status, not kubeconfigs, Secret data, or application responses.
            result = subprocess.run(["docker", "ps", "-a", "--filter", "name=vcluster.cp."+name,
                                     "--format", "{{.Names}} {{.Status}}"], capture_output=True)
            (diagnostics/"docker-status.txt").write_bytes(result.stdout)
            if kubeconfig.exists():
                result = subprocess.run([kubectl, "--kubeconfig", str(kubeconfig), "get", "pods", "-A", "-o", "wide",
                                         "--request-timeout=15s"], capture_output=True, timeout=30)
                (diagnostics/"pods.txt").write_bytes(result.stdout)
                print(result.stdout.decode(), flush=True)
                # Moco operation containers log metadata/errors, never secret responses.
                with (diagnostics/"moco-operations.log").open("wb") as output:
                    for pod, container in [("moco-0", "prepare-runtime"), ("moco-0", "backup"),
                                           ("prepare-mirror", "ops"), ("restore", "prepare"), ("restore", "restore")]:
                        result = subprocess.run([kubectl, "--kubeconfig", str(kubeconfig), "-n", name,
                            "logs", pod, "-c", container, "--tail=30", "--request-timeout=10s"],
                            capture_output=True, timeout=20)
                        output.write((pod+"/"+container+"\n").encode()+result.stdout)
        if options.keep:
            print("Private vind workspace retained at "+str(directory), flush=True)
        else:
            if created:
                subprocess.run([*base, "delete", name, "--driver", "docker"], env=environment, check=False, timeout=120)
            import shutil
            shutil.rmtree(directory)


if __name__ == "__main__":
    main()
