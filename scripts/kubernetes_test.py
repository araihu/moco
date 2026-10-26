#!/usr/bin/env python3
"""Run Moco/ESO acceptance against a disposable, private-node vind cluster."""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "deploy/testing"
LOCK = json.loads((FIXTURES / "versions.json").read_text())


def command(args, *, data=None, timeout=240, check=True):
    result = subprocess.run(list(map(str, args)), input=data, capture_output=True, timeout=timeout)
    if check and result.returncode:
        # Never echo command input or responses: they may contain credentials.
        if Path(str(args[0])).name == "helm":
            print(result.stderr.decode(errors="replace"), flush=True)
        raise RuntimeError(f"{Path(str(args[0])).name} {args[1]} failed with exit {result.returncode}")
    return result


def wait(check, message, seconds=240):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, urllib.error.URLError, json.JSONDecodeError, RuntimeError):
            pass
        time.sleep(2)
    raise AssertionError(message)


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class Acceptance:
    def __init__(self, options, directory):
        self.options, self.directory = options, directory
        self.ns = options.cluster_name
        self.app, self.obs, self.denied = self.ns + "-app", self.ns + "-obs", self.ns + "-denied"
        self.eso = self.ns + "-eso"
        self.namespaces = [self.ns, self.app, self.obs, self.denied, self.eso]
        self.claims = ["data", "backups", "mirror", "restored"]
        self.run_id = str(uuid.uuid4())
        self.mirror_id = str(uuid.uuid4())
        self.results, self.forward = [], None
        self.credentials = directory / "credentials"
        self.values_path = directory / "values.json"
        self.reader = secrets.token_hex(32)
        self.password = secrets.token_bytes(32)
        self.key = "apps/demo/password+space &value"

    def kubectl(self, *args, data=None, check=True, timeout=240):
        return command([self.options.kubectl, "--kubeconfig", self.options.kubeconfig,
                        "--request-timeout=30s", *args], data=data, check=check, timeout=timeout)

    def get(self, kind, name, namespace=None):
        args = ["get", kind, name, "-o", "json"]
        if namespace:
            args.extend(["-n", namespace])
        return json.loads(self.kubectl(*args).stdout)

    def apply(self, objects):
        if not isinstance(objects, list):
            objects = [objects]
        payload = {"apiVersion": "v1", "kind": "List", "items": objects}
        self.kubectl("apply", "--server-side", "--field-manager=moco-acceptance", "-f", "-",
                     data=json.dumps(payload).encode())

    def resource(self, kind, name, spec, namespace=None, api="v1", **extra):
        metadata = {"name": name, "labels": {"moco.araihu.io/test-run": self.run_id}}
        if namespace:
            metadata["namespace"] = namespace
        return {"apiVersion": api, "kind": kind, "metadata": metadata, "spec": spec, **extra}

    def secret(self, namespace, name, values, webhook=False):
        obj = self.resource("Secret", name, {}, namespace)
        del obj["spec"]
        obj.update(type="Opaque", data={k: base64.b64encode(v).decode() for k, v in values.items()})
        if webhook:
            obj["metadata"]["labels"]["external-secrets.io/type"] = "webhook"
        self.apply(obj)

    def ready(self, kind, name, namespace, condition="Ready", value="True"):
        obj = self.get(kind, name, namespace)
        return any(c["type"] == condition and c["status"] == value
                   for c in obj.get("status", {}).get("conditions", []))

    def checkpoint(self, name, action):
        print(json.dumps({"check": name, "status": "running"}), flush=True)
        started = time.monotonic()
        try:
            action()
        except Exception:
            self.results.append({"check": name, "status": "failed", "seconds": round(time.monotonic()-started, 2)})
            raise
        self.results.append({"check": name, "status": "passed", "seconds": round(time.monotonic()-started, 2)})
        print(json.dumps(self.results[-1]), flush=True)

    def download(self, key, filename):
        pin = LOCK[key]
        with urllib.request.urlopen(pin["url"], timeout=90) as response:
            data = response.read()
        assert hashlib.sha256(data).hexdigest() == pin["sha256"], "upstream archive checksum differs"
        path = self.directory / filename
        path.write_bytes(data)
        return path

    def infrastructure(self):
        nodes = json.loads(self.kubectl("get", "nodes", "-o", "json").stdout)["items"]
        assert nodes and all(n["metadata"]["name"].startswith(self.options.cluster_name) for n in nodes), "use the dedicated vind test cluster"
        existing = json.loads(self.kubectl("get", "namespaces", "-o", "json").stdout)["items"]
        assert not set(self.namespaces) & {n["metadata"]["name"] for n in existing}, "test namespaces must be new"
        calico = self.download("calico", "calico.yaml")
        self.kubectl("apply", "--server-side", "--field-manager=moco-acceptance", "-f", calico)
        self.kubectl("rollout", "status", "daemonset/calico-node", "-n", "kube-system", "--timeout=240s", timeout=270)
        self.kubectl("wait", "node", "--all", "--for=condition=Ready", "--timeout=120s")
        self.kubectl("apply", "--server-side", "-f", self.download("serviceMonitorCRD", "servicemonitor.yaml"))
        for ns in self.namespaces:
            self.apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns,
                        "labels": {"moco.araihu.io/test-run": self.run_id}}})
        self.eso_chart = self.download("esoChart", "external-secrets.tgz")
        command([self.options.helm, "upgrade", "--install", "external-secrets", self.eso_chart,
                 "--namespace", self.eso, "--kubeconfig", self.options.kubeconfig,
                 "--wait", "--timeout", "240s"], timeout=270)

    def pod(self, name, namespace, *, volumes=None, mounts=None, code="import time; time.sleep(3600)", labels=None):
        obj = self.resource("Pod", name, {"restartPolicy": "Never", "containers": [{"name": "python",
                            "image": LOCK["pythonImage"], "command": ["python", "-c", code],
                            "volumeMounts": mounts or [], "resources": {"requests": {"cpu": "10m", "memory": "32Mi"},
                            "limits": {"memory": "128Mi"}}}], "volumes": volumes or []}, namespace)
        obj["metadata"]["labels"].update(labels or {})
        self.apply(obj)

    def python(self, pod, namespace, code, payload=None, check=True):
        return self.kubectl("exec", "-i", pod, "-n", namespace, "--", "python", "-c", code,
                            data=json.dumps(payload).encode() if payload is not None else None, check=check)

    def storage(self):
        self.node = json.loads(self.kubectl("get", "nodes", "-o", "json").stdout)["items"][0]["metadata"]["name"]
        resources = []
        for claim in self.claims:
            pv = self.ns + "-" + claim
            resources.append(self.resource("PersistentVolume", pv, {
                "capacity": {"storage": "256Mi"}, "accessModes": ["ReadWriteOnce"], "storageClassName": "",
                "persistentVolumeReclaimPolicy": "Retain", "volumeMode": "Filesystem",
                "claimRef": {"namespace": self.ns, "name": claim},
                "nodeAffinity": {"required": {"nodeSelectorTerms": [{"matchExpressions": [{"key": "kubernetes.io/hostname",
                                  "operator": "In", "values": [self.node]}]}]}},
                "hostPath": {"path": "/var/lib/moco-acceptance/" + self.run_id + "/" + claim, "type": "DirectoryOrCreate"}}))
            resources.append(self.resource("PersistentVolumeClaim", claim, {"accessModes": ["ReadWriteOnce"],
                "storageClassName": "", "volumeName": pv, "resources": {"requests": {"storage": "256Mi"}}}, self.ns))
        self.apply(resources)
        self.volumes = [{"name": c, "persistentVolumeClaim": {"claimName": c}} for c in self.claims]
        self.mounts = [{"name": c, "mountPath": "/storage/"+c} for c in self.claims]
        self.pod("workbench", self.ns, volumes=self.volumes, mounts=self.mounts)
        self.kubectl("wait", "pod/workbench", "-n", self.ns, "--for=condition=Ready", "--timeout=180s")
        self.python("workbench", self.ns, "import os; " + "; ".join(
            f"os.chown('/storage/{c}',65532,65532); os.chmod('/storage/{c}',0o700)" for c in self.claims) +
            "; os.mkdir('/storage/mirror/moco',0o700); os.chown('/storage/mirror/moco',65532,65532)")
        obj = self.resource("Pod", "prepare-mirror", {"restartPolicy": "Never", "securityContext": {
            "runAsUser": 65532, "runAsGroup": 65532, "runAsNonRoot": True}, "containers": [{"name": "ops",
            "image": LOCK["mocoImage"], "command": ["/moco-ops", "init-mirror", "--directory", "/mirror/moco", "--id", self.mirror_id],
            "volumeMounts": [{"name": "mirror", "mountPath": "/mirror"}]}], "volumes": [self.volumes[2]]}, self.ns)
        self.apply(obj)
        wait(lambda: self.get("pod", "prepare-mirror", self.ns)["status"].get("phase") == "Succeeded", "mirror initialization failed")

    def certificate(self):
        c = self.credentials
        if not (c / "ca.key").exists():
            command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
                     "-subj", "/CN=Moco acceptance CA", "-addext", "basicConstraints=critical,CA:TRUE",
                     "-keyout", c/"ca.key", "-out", c/"ca.pem"])
        command(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=moco",
                 "-keyout", c/"tls.key", "-out", c/"server.csr"])
        (c/"san.txt").write_text("subjectAltName=DNS:localhost,DNS:moco,DNS:moco."+self.ns+".svc,DNS:moco."+self.ns+".svc.cluster.local,DNS:moco-metrics."+self.ns+".svc\nextendedKeyUsage=serverAuth\n")
        command(["openssl", "x509", "-req", "-in", c/"server.csr", "-CA", c/"ca.pem", "-CAkey", c/"ca.key",
                 "-CAcreateserial", "-days", "2", "-extfile", c/"san.txt", "-out", c/"tls.crt"])
        for path in c.iterdir():
            path.chmod(0o600)

    def credentials_secret(self):
        names = ["access.json", "cursor-key", "keyring.json", "metrics-token", "tls.crt", "tls.key", "ca.pem"]
        self.secret(self.ns, "moco-credentials", {n: (self.credentials/n).read_bytes() for n in names})

    def install(self):
        command([self.options.ops, "init", "--directory", self.credentials])
        self.admin = (self.credentials/"admin-token").read_text()
        self.access = json.loads((self.credentials/"access.json").read_text())
        self.access["principals"].append({"id": "eso-reader", "tokenSha256": hashlib.sha256(self.reader.encode()).hexdigest()})
        (self.credentials/"access.json").write_text(json.dumps(self.access))
        self.certificate()
        self.credentials_secret()
        self.secret(self.ns, "metrics-only", {"metrics-token": (self.credentials/"metrics-token").read_bytes(), "ca.pem": (self.credentials/"ca.pem").read_bytes()})
        self.values = {"fullnameOverride": "moco", "image": {"repository": LOCK["mocoImage"].split("@")[0], "digest": LOCK["mocoImage"].split("@")[1]},
            "credentials": {"existingSecret": "moco-credentials", "revision": "1"},
            "persistence": {"data": {"existingClaim": "data", "accessMode": "ReadWriteOnce"},
                            "backups": {"existingClaim": "backups", "accessMode": "ReadWriteOnce"}},
            "backups": {"mirrorClaim": "mirror", "mirrorID": self.mirror_id, "keep": 2,
                        "intervalSeconds": 30, "timeoutSeconds": 10, "maxAgeSeconds": 120},
            "monitoring": {"enabled": True, "existingSecret": "metrics-only", "namespaceSelector": {
                "matchLabels": {"kubernetes.io/metadata.name": self.obs}}, "podSelector": {"matchLabels": {"app": "prometheus"}}},
            "networkPolicy": {"publicPeers": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": n}}} for n in [self.app, self.eso]]}}
        self.upgrade()
        self.start_forward()

    def stop_forward(self):
        if self.forward:
            self.forward.terminate()
            self.forward.wait(timeout=10)
            self.forward = None
            self.forward_log.close()

    def start_forward(self):
        self.stop_forward()
        self.public_port, self.admin_port = free_port(), free_port()
        self.tls = ssl.create_default_context(cafile=str(self.credentials/"ca.pem"))
        self.forward_log = (self.directory/"port-forward.log").open("ab")
        self.forward = subprocess.Popen([str(self.options.kubectl), "--kubeconfig", str(self.options.kubeconfig),
            "-n", self.ns, "port-forward", "pod/moco-0", f"{self.public_port}:8443", f"{self.admin_port}:9443", "--address=127.0.0.1"],
            stdout=self.forward_log, stderr=self.forward_log)
        wait(lambda: self.api("/readyz", token=None, raw=True)[0] == 200, "Moco TLS readiness failed", seconds=60)

    def upgrade(self):
        self.values_path.write_text(json.dumps(self.values))
        command([self.options.helm, "upgrade", "--install", "moco", ROOT/"charts/moco", "--namespace", self.ns,
                 "--kubeconfig", self.options.kubeconfig, "-f", self.values_path, "--wait", "--timeout", "240s"], timeout=270)

    def api(self, path, *, token="admin", method="GET", body=None, headers=None, internal=False, raw=False):
        port = self.admin_port if internal else self.public_port
        headers = dict(headers or {})
        if token:
            headers["Authorization"] = "Bearer " + (self.admin if token == "admin" else token)
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        request = urllib.request.Request(f"https://localhost:{port}"+path, data=data, headers=headers, method=method)
        try:
            response = urllib.request.urlopen(request, context=self.tls, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            content_type = response.headers.get_content_type()
            if payload and not raw and (content_type == "application/json" or content_type.endswith("+json")):
                payload = json.loads(payload)
            return response.status, payload, response.headers

    def status(self):
        return json.loads(self.python("workbench", self.ns,
                          "from pathlib import Path; print(Path('/storage/backups/moco/status.json').read_text())").stdout)

    def seed(self):
        tenant = {"name": "eso-acceptance", "externalId": "moco-acceptance-tenant"}
        headers = {"Idempotency-Key": "moco-acceptance-tenant"}
        status, first, _ = self.api("/api/v1/tenants", method="POST", body=tenant, headers=headers)
        assert status == 201
        assert self.api("/api/v1/tenants", method="POST", body=tenant, headers=headers)[1]["id"] == first["id"]
        self.tenant = first["id"]
        self.vault = self.api(f"/api/v1/tenants/{self.tenant}/vaults", method="POST",
                             body={"name": "demo-app", "externalId": "moco-acceptance-vault"})[1]["id"]
        self.route = f"/api/v1/tenants/{self.tenant}/vaults/{self.vault}/secret"
        self.secret_route = self.route + "?" + urllib.parse.urlencode({"path": self.key})
        status, snapshot, headers = self.api("/internal/v1/authorization", internal=True)
        assert status == 200
        self.policy = {"roleBindings": snapshot["roleBindings"], "policies": snapshot["policies"] + [
            {"subject": "eso-reader", "domain": self.tenant, "path": self.route, "method": "GET"}]}
        assert self.api("/internal/v1/authorization", internal=True, method="PUT", body=self.policy,
                        headers={"If-Match": headers["ETag"]})[0] == 200
        assert self.api(self.secret_route, method="PUT", body={"value": base64.b64encode(self.password).decode()})[0] == 201
        assert self.api(self.secret_route, token=self.reader)[0] == 200

    def isolation(self):
        tenant = self.api("/api/v1/tenants", method="POST", body={"name": "other"})[1]["id"]
        vault = self.api(f"/api/v1/tenants/{tenant}/vaults", method="POST", body={"name": "other"})[1]["id"]
        assert self.api(f"/api/v1/tenants/{tenant}/vaults/{vault}/secret?path=hidden", token=self.reader)[0] == 403
        vault = self.api(f"/api/v1/tenants/{self.tenant}/vaults", method="POST", body={"name": "other"})[1]["id"]
        assert self.api(f"/api/v1/tenants/{self.tenant}/vaults/{vault}/secret?path=hidden", token=self.reader)[0] == 403
        assert self.api(self.secret_route, token=self.reader, method="PUT", body={"value": "eA=="})[0] == 403
        assert self.api(self.secret_route, token=None)[0] == 401
        assert self.api("/internal/v1/authorization")[0] == 404

    def secret_matches(self):
        item = self.get("secret", "demo-app", self.app)
        return base64.b64decode(item["data"]["password"]) == self.password

    def force_sync(self):
        self.kubectl("annotate", "externalsecret/demo-app", "-n", self.app,
                     "force-sync="+str(time.time_ns()), "--overwrite")

    def reader_secret(self, token=None):
        self.secret(self.app, "moco-reader", {"token": (token or self.reader).encode(),
                    "ca.pem": (self.credentials/"ca.pem").read_bytes()}, webhook=True)

    def eso_sync(self):
        self.reader_secret()
        manifest = (FIXTURES/"eso.yaml").read_text()
        for name, value in {"APP_NAMESPACE": self.app, "MOCO_NAMESPACE": self.ns,
                            "TENANT_ID": self.tenant, "VAULT_ID": self.vault}.items():
            manifest = manifest.replace("@@"+name+"@@", value)
        self.eso_manifest = manifest.encode()
        self.kubectl("apply", "-f", "-", data=self.eso_manifest)
        wait(self.secret_matches, "ESO did not decode the Moco secret correctly")
        wait(lambda: self.ready("externalsecret", "demo-app", self.app), "ESO did not report a successful reconciliation")
        before = self.get("secret", "demo-app", self.app)["metadata"]["uid"]
        self.kubectl("apply", "-f", "-", data=self.eso_manifest)
        assert self.get("secret", "demo-app", self.app)["metadata"]["uid"] == before
        self.pod("consumer", self.app, volumes=[{"name": "secret", "secret": {"secretName": "demo-app"}}],
                 mounts=[{"name": "secret", "mountPath": "/secret", "readOnly": True}])
        self.kubectl("wait", "pod/consumer", "-n", self.app, "--for=condition=Ready", "--timeout=180s")
        assert self.consumer_matches(), "consumer did not read the ESO-managed Secret"

    def consumer_matches(self):
        result = self.python("consumer", self.app,
            "import sys,json,base64; from pathlib import Path; assert Path('/secret/password').read_bytes()==base64.b64decode(json.load(sys.stdin))",
            base64.b64encode(self.password).decode(), check=False)
        return result.returncode == 0

    def updates(self):
        self.password = secrets.token_bytes(48)
        etag = self.api(self.secret_route)[2]["ETag"]
        assert self.api(self.secret_route, method="PUT", body={"value": base64.b64encode(self.password).decode()},
                        headers={"If-Match": etag, "X-Request-ID": "moco-acceptance-secret-update"})[0] == 200
        assert self.api(self.secret_route, method="PUT", body={"value": "eA=="}, headers={"If-Match": etag})[0] == 412
        self.force_sync()
        wait(self.secret_matches, "ESO did not refresh the changed value")
        wait(self.consumer_matches, "consumer did not receive the mounted Secret update")
        status, audit, _ = self.api("/internal/v1/audit?limit=100", internal=True)
        assert status == 200 and any(e["requestId"] == "moco-acceptance-secret-update" for e in audit["items"])

    def auth_failure(self):
        self.reader_secret("invalid-"+secrets.token_hex(16))
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app, value="False"), "ESO accepted an invalid credential")
        assert self.secret_matches(), "ESO must retain the last valid Secret on authentication failure"
        self.reader_secret()
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app), "ESO did not recover after credential repair")

    def tls_failure(self):
        self.secret(self.app, "moco-reader", {"token": self.reader.encode(), "ca.pem": b"invalid-ca"}, webhook=True)
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app, value="False"), "ESO accepted invalid CA material")
        assert self.secret_matches()
        self.reader_secret()
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app), "ESO TLS trust did not recover")

    def certificate_rotation(self):
        old = hashlib.sha256((self.credentials/"tls.crt").read_bytes()).hexdigest()
        self.certificate()
        assert hashlib.sha256((self.credentials/"tls.crt").read_bytes()).hexdigest() != old
        self.credentials_secret()
        self.values["credentials"]["revision"] = "2"
        self.upgrade()
        self.start_forward()
        with socket.create_connection(("127.0.0.1", self.public_port)) as connection:
            with self.tls.wrap_socket(connection, server_hostname="localhost") as connection:
                expected = ssl.PEM_cert_to_DER_cert((self.credentials/"tls.crt").read_text())
                assert connection.getpeercert(binary_form=True) == expected
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app), "ESO failed after certificate renewal")

    def token_rotation(self):
        previous, self.reader = self.reader, secrets.token_hex(32)
        self.access["principals"][1]["tokenSha256"] = hashlib.sha256(self.reader.encode()).hexdigest()
        (self.credentials/"access.json").write_text(json.dumps(self.access))
        self.credentials_secret()
        self.values["credentials"]["revision"] = "3"
        self.upgrade()
        self.start_forward()
        assert self.api(self.secret_route, token=previous)[0] == 401
        assert self.api(self.secret_route, token=self.reader)[0] == 200
        self.reader_secret()
        self.force_sync()
        wait(lambda: self.ready("externalsecret", "demo-app", self.app), "ESO failed after token rotation")

    def network(self):
        for namespace in [self.app, self.obs, self.denied]:
            self.pod("probe", namespace, labels={"app": "prometheus"} if namespace != self.app else {})
            self.kubectl("wait", "pod/probe", "-n", namespace, "--for=condition=Ready", "--timeout=180s")
        probe = """import json,sys,ssl,urllib.request,urllib.error,socket
p=json.load(sys.stdin)
try:
 r=urllib.request.urlopen(urllib.request.Request(p['url'],headers={'Authorization':'Bearer '+p['token']}),context=ssl.create_default_context(cadata=p['ca']),timeout=3)
 assert p['expected']=='http' and r.status==200
 if p.get('metrics'): assert b'moco_' in r.read()
except urllib.error.HTTPError:
 raise AssertionError('unexpected HTTP response')
except (urllib.error.URLError,TimeoutError,socket.timeout):
 assert p['expected']=='blocked'
"""
        def check(namespace, port, path, token, blocked=False, metrics=False):
            self.python("probe", namespace, probe, {"url": f"https://moco.{self.ns}.svc:{port}"+path,
                "token": token, "ca": (self.credentials/"ca.pem").read_text(),
                "expected": "blocked" if blocked else "http", "metrics": metrics})
        check(self.app, 8443, self.secret_route, self.reader)
        check(self.denied, 8443, self.secret_route, self.reader, blocked=True)
        metrics = (self.credentials/"metrics-token").read_text()
        assert self.api("/metrics", token=None, internal=True, raw=True)[0] == 401
        assert self.api("/internal/v1/authorization", token=metrics, internal=True)[0] == 401
        admin_probe = probe
        for namespace, blocked in [(self.obs, False), (self.app, True), (self.denied, True)]:
            self.python("probe", namespace, admin_probe, {"url": f"https://moco-metrics.{self.ns}.svc:9443/metrics",
                "token": metrics, "ca": (self.credentials/"ca.pem").read_text(), "expected": "blocked" if blocked else "http", "metrics": True})
        assert self.get("servicemonitor", "moco", self.ns)["spec"]["endpoints"][0]["authorization"]["credentials"]["name"] == "metrics-only"

    def snapshot(self):
        before = self.status()["lastSuccess"]
        wait(lambda: self.status()["lastSuccess"] != before, "automatic verified backup did not repeat", seconds=90)
        status = self.status()
        assert status["lastAttemptOK"]
        self.python("workbench", self.ns,
            "import sys,json,shutil,os; from pathlib import Path; p=json.load(sys.stdin); "
            "a=Path('/storage/backups/moco')/p; b=Path('/storage/mirror/moco')/p; "
            "assert a.read_bytes()==b.read_bytes(); shutil.copyfile(b,'/storage/mirror/recovery.tar'); "
            "os.chown('/storage/mirror/recovery.tar',65532,65532); os.chmod('/storage/mirror/recovery.tar',0o600)", status["archive"])

    def root_rotation(self):
        keyring = json.loads((self.credentials/"keyring.json").read_text())
        keyring["keys"]["root-v2"] = base64.b64encode(secrets.token_bytes(32)).decode()
        keyring["activeKeyId"] = "root-v2"
        (self.credentials/"keyring.json").write_text(json.dumps(keyring))
        self.credentials_secret()
        self.values["credentials"]["revision"] = "4"
        self.values["encryption"] = {"epoch": 2}
        self.upgrade()
        self.start_forward()
        assert self.api(self.secret_route, token=self.reader)[0] == 200
        status, result, _ = self.api("/internal/v1/encryption/rotation?limit=200", method="POST", internal=True)
        assert status == 200 and result["complete"] and result["remainingOldKeys"] == 0
        assert self.api(self.secret_route, token=self.reader)[1]["value"] == base64.b64encode(self.password).decode()

    def mirror_failure(self):
        self.python("workbench", self.ns, "from pathlib import Path; Path('/storage/mirror/moco/.moco-destination').rename('/storage/mirror/moco/.saved-marker')")
        try:
            wait(lambda: not self.status()["lastAttemptOK"], "missing mirror marker did not fail the backup", seconds=90)
            wait(lambda: not self.ready("pod", "moco-0", self.ns), "failed mirrored backup did not remove readiness", seconds=60)
            assert self.secret_matches(), "existing application Secret was lost during Moco unavailability"
        finally:
            self.python("workbench", self.ns, "from pathlib import Path; Path('/storage/mirror/moco/.saved-marker').rename('/storage/mirror/moco/.moco-destination')")
        wait(lambda: self.status()["lastAttemptOK"] and self.ready("pod", "moco-0", self.ns), "mirror recovery did not restore readiness", seconds=120)

    def pod_restart(self):
        uid = self.get("pod", "moco-0", self.ns)["metadata"]["uid"]
        self.kubectl("delete", "pod/moco-0", "-n", self.ns, "--wait=false")
        wait(lambda: self.get("pod", "moco-0", self.ns)["metadata"]["uid"] != uid and self.ready("pod", "moco-0", self.ns), "Moco did not recover after pod replacement")
        self.start_forward()
        assert self.api(self.secret_route, token=self.reader)[1]["value"] == base64.b64encode(self.password).decode()

    def restore(self):
        self.stop_forward()
        self.kubectl("scale", "statefulset/moco", "-n", self.ns, "--replicas=0")
        self.kubectl("wait", "--for=delete", "pod/moco-0", "-n", self.ns, "--timeout=120s")
        self.kubectl("delete", "pod/workbench", "-n", self.ns, "--wait=true")
        volumes = [{"name": "data", "persistentVolumeClaim": {"claimName": "restored"}},
                   {"name": "backups", "emptyDir": {}}, self.volumes[2],
                   {"name": "projected", "secret": {"secretName": "moco-credentials", "defaultMode": 288}},
                   {"name": "credentials", "emptyDir": {"medium": "Memory"}}]
        prepare = {"name": "prepare", "image": LOCK["mocoImage"], "command": ["/moco-ops", "prepare-runtime", "--mirror-id", self.mirror_id],
                   "volumeMounts": [{"name": n, "mountPath": p} for n,p in [("data","/storage/data"), ("backups","/storage/backups"),
                       ("mirror","/storage/mirror"), ("projected","/projected"), ("credentials","/runtime")]]}
        restore = {"name": "restore", "image": LOCK["mocoImage"], "command": ["/moco-backup", "restore", "--backup", "/mirror/recovery.tar",
            "--database", "/data/moco/moco.db", "--keyring", "/run/moco/keyring.json"], "volumeMounts": [
                {"name": "data", "mountPath": "/data"}, {"name": "mirror", "mountPath": "/mirror", "readOnly": True},
                {"name": "credentials", "mountPath": "/run/moco", "subPath": "credentials", "readOnly": True}]}
        self.apply(self.resource("Pod", "restore", {"restartPolicy": "Never", "securityContext": {
            "runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532},
            "initContainers": [prepare], "containers": [restore], "volumes": volumes}, self.ns))
        wait(lambda: self.get("pod", "restore", self.ns)["status"].get("phase") == "Succeeded", "offline mirrored restore failed")
        self.kubectl("delete", "pod/restore", "-n", self.ns, "--wait=true")
        self.values["persistence"]["data"]["existingClaim"] = "restored"
        self.upgrade()
        self.start_forward()
        assert self.api(self.secret_route, token=self.reader)[1]["value"] == base64.b64encode(self.password).decode()
        # Force ESO to fetch again rather than accepting an old Kubernetes Secret.
        self.kubectl("delete", "secret/demo-app", "-n", self.app)
        self.force_sync()
        wait(self.secret_matches, "ESO did not recreate its Secret from the restored Moco database")

    def run(self):
        for name, action in [
            ("vind-calico-eso-install", self.infrastructure), ("separate-persistent-storage-and-mirror", self.storage),
            ("production-chart-tls-and-verified-backup-readiness", self.install),
            ("idempotent-api-bootstrap-and-scoped-policy", self.seed), ("tenant-vault-write-and-admin-isolation", self.isolation),
            ("eso-declarative-sync-url-encoding-binary-value-and-consumer", self.eso_sync),
            ("conditional-write-and-live-consumer-update", self.updates),
            ("eso-invalid-token-retention-and-recovery", self.auth_failure), ("eso-tls-trust-failure-and-recovery", self.tls_failure),
            ("certificate-renewal-private-copy-rollout", self.certificate_rotation),
            ("reader-token-rotation-and-revocation", self.token_rotation), ("networkpolicy-and-protected-metrics", self.network),
            ("automatic-verified-mirrored-snapshot", self.snapshot), ("root-key-rotation-and-rewrap", self.root_rotation),
            ("mirror-loss-readiness-and-recovery", self.mirror_failure), ("pod-replacement-with-persistent-data", self.pod_restart),
            ("offline-mirror-restore-with-historical-root-key-and-eso-resync", self.restore)]:
            self.checkpoint(name, action)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--cluster-name", default="moco-eso-e2e")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--ops", type=Path, default=ROOT/"bin/moco-ops")
    parser.add_argument("--report", type=Path, default=ROOT/"bin/kubernetes-report.json")
    options = parser.parse_args()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="moco-acceptance-") as directory:
        drill = Acceptance(options, Path(directory))
        report = {"passed": False, "driver": "vind", "mocoVersion": LOCK["mocoVersion"],
                  "versions": LOCK, "startedAt": datetime.now(timezone.utc).isoformat(), "checks": drill.results,
                  "limitations": ["Static local RWO volumes in a disposable single-node cluster; not CSI/RWOP certification.",
                                  "Mirror uses a distinct volume on the same Docker host; not proof of off-host durability."]}
        try:
            drill.run()
            report["passed"] = True
        finally:
            drill.stop_forward()
            options.report.parent.mkdir(parents=True, exist_ok=True)
            options.report.write_text(json.dumps(report, indent=2)+"\n")
        print(json.dumps({"kubernetesAcceptance": "passed", "checks": len(drill.results), "report": str(options.report)}), flush=True)


if __name__ == "__main__":
    main()
