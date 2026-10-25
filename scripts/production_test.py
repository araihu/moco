#!/usr/bin/env python3
"""Operational recovery drill against release executables; optional OCI smoke.

Uses only Python's standard library, OpenSSL and (for --image) Docker. It never
touches a configured deployment: all credentials, processes and data are private
fixtures. Image mode runs the binaries extracted from the actual image.
"""
import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import ssl
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

REPO = Path(__file__).resolve().parent.parent


def command(args, **kwargs):
    return subprocess.run([str(arg) for arg in args], check=True, capture_output=True, **kwargs)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def until(check, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            pass
        time.sleep(0.1)
    raise AssertionError("operational check timed out")


class Drill:
    def __init__(self, root, binaries):
        self.root, self.binaries = root, binaries
        self.credentials = root / "credentials"
        command([binaries / "moco-ops", "init", "--directory", self.credentials])
        self.admin = (self.credentials / "admin-token").read_text()
        self.reader = secrets.token_hex(32)
        access = json.loads((self.credentials / "access.json").read_text())
        access["principals"].append({"id": "reader", "tokenSha256": hashlib.sha256(self.reader.encode()).hexdigest()})
        (self.credentials / "access.json").write_text(json.dumps(access))
        command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=localhost",
                 "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", self.credentials / "tls.key", "-out", self.credentials / "tls.crt"])
        shutil.copyfile(self.credentials / "tls.crt", self.credentials / "ca.pem")
        for path in self.credentials.iterdir():
            path.chmod(0o600)
        for name in ("data", "backups", "mirror", "scratch"):
            (root / name).mkdir(mode=0o700)
        self.mirror_id = command([binaries / "moco-ops", "init-mirror", "--directory", root / "mirror"]).stdout.decode().strip()
        # Exercise the Kubernetes init path with the actual Secret projection
        # layout; the server still reads private regular copies, never links.
        projected = root / "projected"
        version = projected / "..version"
        version.mkdir(parents=True, mode=0o700)
        (projected / "..data").symlink_to("..version", target_is_directory=True)
        for name in ("access.json", "cursor-key", "keyring.json", "metrics-token", "tls.crt", "tls.key", "ca.pem"):
            shutil.copyfile(self.credentials / name, version / name)
            (projected / name).symlink_to("..data/" + name)
        prepared = root / "private-credentials"
        command([binaries / "moco-ops", "prepare-runtime", "--source", projected, "--credentials", prepared,
                 "--data", root / "data", "--backups", root / "backups", "--mirror", root / "mirror", "--mirror-id", self.mirror_id])
        self.credentials = prepared
        self.tls = ssl.create_default_context(cafile=str(self.credentials / "ca.pem"))
        self.public_port, self.admin_port = free_port(), free_port()
        self.base, self.admin_base = f"https://localhost:{self.public_port}", f"https://localhost:{self.admin_port}"
        self.processes = []
        self.server = None
        self.log = (root / "server.log").open("ab")
        self.environment = {key: value for key, value in os.environ.items() if not key.startswith("MOCO_")}
        self.environment["TMPDIR"] = str(root / "scratch")

    def api(self, route, *, admin=False, token=None, method="GET", body=None, headers=None, expected=200):
        header = dict(headers or {})
        if token:
            header["Authorization"] = "Bearer " + token
        payload = None if body is None else json.dumps(body).encode()
        if payload is not None:
            header["Content-Type"] = "application/json"
        request = urllib.request.Request((self.admin_base if admin else self.base) + route, payload, header, method=method)
        try:
            response = urllib.request.urlopen(request, context=self.tls, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            assert response.status == expected, f"{method} {route.split('?')[0]} returned {response.status}, expected {expected}"
            content = json.loads(raw) if raw and "json" in response.headers.get("Content-Type", "") else raw.decode()
            return content, response.headers

    def start(self, epoch=1):
        env = dict(self.environment, MOCO_PROFILE="production", MOCO_ADDR=f"127.0.0.1:{self.public_port}",
                   MOCO_ADMIN_ADDR=f"127.0.0.1:{self.admin_port}", MOCO_DB_PATH=str(self.root / "data/moco.db"),
                   MOCO_AUTH_CONFIG=str(self.credentials / "access.json"), MOCO_CURSOR_HMAC_KEY_FILE=str(self.credentials / "cursor-key"),
                   MOCO_ENCRYPTION_KEYS_FILE=str(self.credentials / "keyring.json"), MOCO_ENCRYPTION_KEY_EPOCH=str(epoch),
                   MOCO_METRICS_TOKEN_FILE=str(self.credentials / "metrics-token"), MOCO_BACKUP_STATUS_FILE=str(self.root / "backups/status.json"),
                   MOCO_TLS_CERT_FILE=str(self.credentials / "tls.crt"), MOCO_TLS_KEY_FILE=str(self.credentials / "tls.key"))
        self.server = subprocess.Popen([str(self.binaries / "moco-server")], env=env, stdout=subprocess.DEVNULL, stderr=self.log)
        self.processes.append(self.server)

        def ready():
            assert self.server.poll() is None, "server exited during startup: " + (self.root / "server.log").read_text()
            return self.api("/readyz")[0]
        until(ready)

    def stop(self, abrupt=False):
        self.server.send_signal(signal.SIGKILL if abrupt else signal.SIGTERM)
        code = self.server.wait(timeout=40)
        assert code == (-signal.SIGKILL if abrupt else 0), f"server exit status {code}"

    def ops(self, name, args, expected_success=True):
        result = subprocess.run([str(self.binaries / name), *map(str, args)], env=self.environment, capture_output=True)
        assert (result.returncode == 0) == expected_success, result.stderr.decode()
        return json.loads(result.stdout) if result.returncode == 0 and result.stdout else None

    def backup_args(self):
        return ["--database", self.root / "data/moco.db", "--directory", self.root / "backups", "--mirror-directory", self.root / "mirror",
                "--mirror-id", self.mirror_id, "--keyring", self.credentials / "keyring.json", "--keep", "8"]

    def backup(self):
        return self.ops("moco-ops", ["backup-once", *self.backup_args()])["archive"]

    def run(self):
        started = time.monotonic()
        self.start()
        self.api("/api/v1/tenants", expected=401)
        for route in ("/internal/v1/authorization", "/internal/v1/audit", "/metrics"):
            self.api(route, token=self.admin, expected=404)
        self.ops("moco-ops", ["probe", "--url", self.base + "/readyz", "--ca", self.credentials / "ca.pem"])
        tenants, paths, etags = [], [], []
        value = base64.b64encode(b"operational-recovery-secret").decode()
        for number in range(2):
            tenant, _ = self.api("/api/v1/tenants", token=self.admin, method="POST", body={"name": f"tenant-{number}"}, expected=201)
            tenants.append(tenant["id"])
            vault, _ = self.api(f"/api/v1/tenants/{tenant['id']}/vaults", token=self.admin, method="POST", body={"name": "database"}, expected=201)
            route = f"/api/v1/tenants/{tenant['id']}/vaults/{vault['id']}/secret?path=prod/password"
            paths.append(route)
            _, headers = self.api(route, token=self.admin, method="PUT", body={"value": value}, expected=201)
            etags.append(headers["ETag"])
        policies, headers = self.api("/internal/v1/authorization", admin=True, token=self.admin)
        policies["policies"].append({"subject": "reader", "domain": tenants[0], "path": "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", "method": "GET", "secretPathPrefix": "prod/"})
        self.api("/internal/v1/authorization", admin=True, token=self.admin, method="PUT", headers={"If-Match": headers["ETag"]},
                 body={"roleBindings": policies["roleBindings"], "policies": policies["policies"]})
        def reader_ready():
            try:
                return self.api(paths[0], token=self.reader)[0]
            except AssertionError:
                return False  # The persisted policy reload is asynchronous.
        until(reader_ready)
        self.api(paths[1], token=self.reader, expected=403)
        # Reject only the test mutation's audit insert, after its business SQL.
        # This must roll back the value, ETag and policy revision before HTTP 503.
        with sqlite3.connect(self.root / "data/moco.db") as database:
            database.execute("CREATE TRIGGER reject_request_audit BEFORE INSERT ON audit_events WHEN NEW.request_id='operational-audit-fault' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
        self.api(paths[0], token=self.admin, method="PUT", body={"value": base64.b64encode(b"must-never-commit").decode()},
                 headers={"X-Request-ID": "operational-audit-fault"}, expected=503)
        current, current_headers = self.api(paths[0], token=self.reader)
        assert current["value"] == value and current_headers["ETag"] == etags[0]
        policy_before, policy_headers = self.api("/internal/v1/authorization", admin=True, token=self.admin)
        self.api("/internal/v1/authorization", admin=True, token=self.admin, method="PUT",
                 headers={"X-Request-ID": "operational-audit-fault", "If-Match": policy_headers["ETag"]},
                 body={"roleBindings": policy_before["roleBindings"], "policies": policy_before["policies"]}, expected=503)
        policy_after, after_headers = self.api("/internal/v1/authorization", admin=True, token=self.admin)
        assert policy_after == policy_before and after_headers["ETag"] == policy_headers["ETag"]
        self.api("/readyz", expected=503)
        with sqlite3.connect(self.root / "data/moco.db") as database:
            assert database.execute("SELECT COUNT(*) FROM audit_events WHERE request_id='operational-audit-fault'").fetchone()[0] == 0
            database.execute("DROP TRIGGER reject_request_audit")
        self.stop()
        self.start()
        assert self.api(paths[0], token=self.reader)[1]["ETag"] == etags[0]
        worker = subprocess.Popen([str(self.binaries / "moco-ops"), "backup-loop", *map(str, self.backup_args()), "--interval", "3s", "--timeout", "2s"],
                                  env=self.environment, stdout=subprocess.DEVNULL, stderr=self.log)
        self.processes.append(worker)
        status_path = self.root / "backups/status.json"
        first = until(lambda: json.loads(status_path.read_text()) if status_path.exists() else None)
        assert first["lastAttemptOK"]
        until(lambda: json.loads(status_path.read_text())["lastSuccess"] != first["lastSuccess"], seconds=12)
        worker.terminate()
        assert worker.wait(timeout=5) == 0
        old_archive = self.backup()
        self.stop()
        keyring = json.loads((self.credentials / "keyring.json").read_text())
        keyring["keys"]["root-v2"] = base64.b64encode(secrets.token_bytes(32)).decode()
        keyring["activeKeyId"] = "root-v2"
        (self.credentials / "keyring.json").write_text(json.dumps(keyring))
        self.start(epoch=2)
        partial, _ = self.api("/internal/v1/encryption/rotation?limit=1", admin=True, token=self.admin, method="POST")
        assert partial["remainingOldKeys"] == 1
        self.backup()
        self.stop(abrupt=True)
        self.start(epoch=2)
        assert self.api(paths[0], token=self.reader)[0]["value"] == value
        complete, _ = self.api("/internal/v1/encryption/rotation?limit=200", admin=True, token=self.admin, method="POST")
        assert complete["complete"] and complete["remainingOldKeys"] == 0
        self.backup()
        self.api("/metrics", admin=True, expected=401)
        metrics, _ = self.api("/metrics", admin=True, token=(self.credentials / "metrics-token").read_text())
        assert "moco_ready 1" in metrics and "moco_backup_last_attempt_success 1" in metrics
        assert "operational-recovery-secret" not in metrics and "password" not in metrics
        self.ops("moco-ops", ["backup-once", *self.backup_args(), "--mirror-directory", self.root / "missing-mirror"], expected_success=False)
        self.ops("moco-ops", ["backup-health", "--status", status_path], expected_success=False)
        metrics, _ = self.api("/metrics", admin=True, token=(self.credentials / "metrics-token").read_text())
        assert "moco_backup_last_attempt_success 0" in metrics and "moco_backup_failures_total 1" in metrics
        self.stop()
        # Startup writes (epoch/policy bootstrap) must also be audited atomically.
        with sqlite3.connect(self.root / "data/moco.db") as database:
            database.execute("CREATE TRIGGER reject_startup_audit BEFORE INSERT ON audit_events WHEN NEW.method='STARTUP' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
        try:
            self.start(epoch=3)
            raise AssertionError("startup accepted an unaudited epoch change")
        except AssertionError as error:
            assert "server exited during startup" in str(error)
        with sqlite3.connect(self.root / "data/moco.db") as database:
            assert database.execute("SELECT epoch FROM encryption_key_state").fetchone()[0] == 2
            database.execute("DROP TRIGGER reject_startup_audit")
        # An incorrect historical root key must abort startup before serving.
        original = (self.credentials / "keyring.json").read_text()
        wrong = json.loads(original)
        wrong["keys"]["root-v2"] = base64.b64encode(secrets.token_bytes(32)).decode()
        (self.credentials / "keyring.json").write_text(json.dumps(wrong))
        try:
            self.start(epoch=2)
            raise AssertionError("server accepted incorrect persisted root-key bytes")
        except AssertionError as error:
            assert "server exited during startup" in str(error)
        (self.credentials / "keyring.json").write_text(original)
        # Lose the original SQLite directory, including WAL and shared-memory files.
        shutil.rmtree(self.root / "data")
        (self.root / "data").mkdir(mode=0o700)
        recovery_started = time.monotonic()
        current_only = self.credentials / "current-only.json"
        current_only.write_text(json.dumps({"activeKeyId": "root-v2", "keys": {"root-v2": keyring["keys"]["root-v2"]}}))
        current_only.chmod(0o600)
        restore_args = ["restore", "--backup", self.root / "mirror" / old_archive, "--database", self.root / "data/moco.db", "--keyring"]
        self.ops("moco-backup", [*restore_args, current_only], expected_success=False)
        assert not (self.root / "data/moco.db").exists()
        manifest = self.ops("moco-backup", [*restore_args, self.credentials / "keyring.json"])
        self.start(epoch=2)
        recovered, headers = self.api(paths[0], token=self.reader)
        assert recovered["value"] == value and headers["ETag"] == etags[0]
        self.api(paths[1], token=self.reader, expected=403)
        self.api(paths[0], expected=401)
        self.api(paths[0], token=self.admin, method="PUT", body={"value": base64.b64encode(b"continued-after-recovery").decode()}, expected=200)
        self.backup()
        self.stop()
        assert "operational-recovery-secret" not in (self.root / "server.log").read_text()
        print(json.dumps({"operational": "passed", "transactionalAuditFaultRollback": True, "installRestartCrashRecoveryRotation": True, "snapshotBoundary": manifest["createdAt"],
                          "recoverySeconds": round(time.monotonic()-recovery_started, 3), "totalSeconds": round(time.monotonic()-started, 3)}), flush=True)

    def close(self):
        for process in self.processes:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
        self.log.close()


def tar_credentials(directory):
    payload = io.BytesIO()
    with tarfile.open(fileobj=payload, mode="w") as archive:
        for path in directory.iterdir():
            data = path.read_bytes()
            info = tarfile.TarInfo(path.name)
            info.size, info.mode, info.uid, info.gid = len(data), 0o600, 65532, 65532
            archive.addfile(info, io.BytesIO(data))
    return payload.getvalue()


def oci_smoke(image, drill):
    """Run the image under the actual Compose restrictions; works with SSH Docker contexts."""
    prefix = "moco-test-" + secrets.token_hex(5)
    volumes, containers, tunnel = [], [], None
    try:
        for name in ("data", "backups", "credentials", "mirror", "recovery"):
            volume = prefix + "-" + name
            command(["docker", "volume", "create", volume])
            volumes.append(volume)
        helper, server = prefix + "-copy", prefix + "-server"
        command(["docker", "create", "--name", helper, "-v", volumes[2]+":/run/moco", image])
        containers.append(helper)
        command(["docker", "cp", "-a", "-", helper+":/run/moco"], input=tar_credentials(drill.credentials))
        environment = dict(drill.environment, MOCO_IMAGE=image, MOCO_DATA_DIR="/fixture/data", MOCO_BACKUP_DIR="/fixture/backups", MOCO_MIRROR_DIR="/fixture/mirror", MOCO_MIRROR_ID=drill.mirror_id, MOCO_CREDENTIALS_DIR="/fixture/credentials")
        profile = json.loads(command(["docker", "compose", "-f", REPO / "deploy/production/compose.yaml", "config", "--format", "json"], env=environment).stdout)
        service = profile["services"]["server"]
        assert service["read_only"] and service["user"] == "65532:65532" and service["cap_drop"] == ["ALL"]
        env = dict(service["environment"], MOCO_ENCRYPTION_KEY_EPOCH="2")
        args = ["docker", "create", "--name", server, "--read-only", "--user", service["user"], "--cap-drop=ALL", "--security-opt=no-new-privileges:true",
                "--pids-limit", str(service["pids_limit"]), "--memory", str(service["mem_limit"]), "--cpus", str(service["cpus"]),
                "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "-p", "127.0.0.1::8443", "-p", "127.0.0.1::9443",
                "-v", volumes[0]+":/data", "-v", volumes[1]+":/backups:ro", "-v", volumes[2]+":/run/moco:ro"]
        for key, value in env.items():
            args.extend(["-e", key+"="+str(value)])
        command([*args, image])
        containers.append(server)
        command(["docker", "start", server])
        endpoint = os.environ.get("DOCKER_HOST") or json.loads(command(["docker", "context", "inspect"]).stdout)[0]["Endpoints"]["docker"]["Host"]
        def connect(container):
            nonlocal tunnel
            inspect = json.loads(command(["docker", "inspect", container]).stdout)[0]
            ports = inspect["NetworkSettings"]["Ports"]
            public_port, admin_port = int(ports["8443/tcp"][0]["HostPort"]), int(ports["9443/tcp"][0]["HostPort"])
            if endpoint.startswith("ssh://"):
                if tunnel:
                    tunnel.terminate()
                    tunnel.wait(timeout=10)
                remote_public, remote_admin = public_port, admin_port
                public_port, admin_port = free_port(), free_port()
                remote = urllib.parse.urlsplit(endpoint)
                host = (urllib.parse.unquote(remote.username)+"@" if remote.username else "") + remote.hostname
                ssh_port = ["-p", str(remote.port)] if remote.port else []
                tunnel = subprocess.Popen(["ssh", *ssh_port, "-N", "-o", "ExitOnForwardFailure=yes", "-L", f"127.0.0.1:{public_port}:127.0.0.1:{remote_public}",
                                           "-L", f"127.0.0.1:{admin_port}:127.0.0.1:{remote_admin}", host], stdout=subprocess.DEVNULL, stderr=drill.log)
            drill.base, drill.admin_base = f"https://localhost:{public_port}", f"https://localhost:{admin_port}"
        connect(server)
        until(lambda: drill.api("/readyz")[0], seconds=60)
        drill.api("/internal/v1/audit", token=drill.admin, expected=404)
        drill.api("/metrics", token=drill.admin, expected=404)
        drill.api("/metrics", admin=True, expected=401)
        metrics, _ = drill.api("/metrics", admin=True, token=(drill.credentials / "metrics-token").read_text())
        assert "moco_ready 1" in metrics
        tenant, _ = drill.api("/api/v1/tenants", token=drill.admin, method="POST", body={"name": "oci-persistence"}, expected=201)
        vault, _ = drill.api(f"/api/v1/tenants/{tenant['id']}/vaults", token=drill.admin, method="POST", body={"name": "database"}, expected=201)
        secret_path = f"/api/v1/tenants/{tenant['id']}/vaults/{vault['id']}/secret?path=prod/password"
        secret_value = base64.b64encode(b"oci-recovery-value").decode()
        _, secret_headers = drill.api(secret_path, token=drill.admin, method="PUT", body={"value": secret_value}, expected=201)
        mirror_id = command(["docker", "run", "--rm", "--read-only", "--network", "none", "-v", volumes[3]+":/mirror", "--entrypoint", "/moco-ops", image, "init-mirror"]).stdout.decode().strip()
        maintenance = ["docker", "run", "--rm", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--network", "none",
                       "-e", "TMPDIR=/backups", "-v", volumes[1]+":/backups", "-v", volumes[2]+":/run/moco:ro", "-v", volumes[3]+":/mirror"]
        status = json.loads(command([*maintenance, "-v", volumes[0]+":/data:ro", "--entrypoint", "/moco-ops", image, "backup-once", "--database", "/data/moco.db", "--mirror-id", mirror_id, "--keep", "2"]).stdout)
        assert status["lastAttemptOK"]
        metrics, _ = drill.api("/metrics", admin=True, token=(drill.credentials / "metrics-token").read_text())
        assert "moco_backup_last_attempt_success 1" in metrics
        unwritable = subprocess.run(["docker", "exec", server, "/moco-ops", "init", "--directory", "/unwritable"], capture_output=True, check=False)
        assert unwritable.returncode != 0, "container root filesystem was writable"
        command(["docker", "exec", server, "/moco-ops", "probe"])
        command(["docker", "restart", "--timeout", "40", server])
        connect(server)  # Ephemeral published ports can change on restart.
        until(lambda: drill.api("/readyz")[0])
        drill.api("/api/v1/tenants/"+tenant["id"], token=drill.admin)
        assert drill.api(secret_path, token=drill.admin)[0]["value"] == secret_value
        command(["docker", "stop", "--timeout", "40", server])
        command([*maintenance, "-v", volumes[4]+":/data", "--entrypoint", "/moco-backup", image, "restore", "--backup", "/mirror/"+status["archive"], "--database", "/data/moco.db", "--keyring", "/run/moco/keyring.json"])
        # Replace the stopped source container with a separate restored volume.
        command(["docker", "rm", server])
        restored = prefix + "-restored"
        replacements = {server: restored, volumes[0]+":/data": volumes[4]+":/data"}
        command([*[replacements.get(str(value), str(value)) for value in args], image])
        containers.append(restored)
        command(["docker", "start", restored])
        connect(restored)
        until(lambda: drill.api("/readyz")[0])
        recovered, headers = drill.api(secret_path, token=drill.admin)
        assert recovered["value"] == secret_value and headers["ETag"] == secret_headers["ETag"]
        command(["docker", "stop", "--timeout", "40", restored])
        print(json.dumps({"ociRuntime": "passed", "nonRootReadOnlyTLSAdminIsolationRestart": True, "verifiedReadOnlySourceBackupAndMirroredRestore": True}), flush=True)
    except Exception:
        for name in containers:
            diagnostics = subprocess.run(["docker", "logs", "--tail", "25", name], capture_output=True, check=False)
            sys.stderr.write(diagnostics.stderr.decode(errors="replace"))
        raise
    finally:
        if tunnel:
            tunnel.terminate()
            tunnel.wait(timeout=10)
        for name in reversed(containers):
            subprocess.run(["docker", "rm", "-f", name], capture_output=True, check=False)
        for name in volumes:
            subprocess.run(["docker", "volume", "rm", name], capture_output=True, check=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path)
    parser.add_argument("--image")
    args = parser.parse_args()
    assert bool(args.bin_dir) != bool(args.image), "supply either --bin-dir or --image"
    with tempfile.TemporaryDirectory(prefix="moco-production-") as temporary:
        root = Path(temporary)
        binaries = args.bin_dir
        if args.image:
            binaries = root / "bin"
            binaries.mkdir()
            container = command(["docker", "create", args.image]).stdout.decode().strip()
            try:
                for name in ("moco-server", "moco-ops", "moco-backup", "moco-audit-export"):
                    command(["docker", "cp", container+":/"+name, binaries / name])
            finally:
                command(["docker", "rm", container])
        drill = Drill(root, binaries.resolve())
        try:
            drill.run()
            if args.image:
                oci_smoke(args.image, drill)
        finally:
            drill.close()


if __name__ == "__main__":
    main()
