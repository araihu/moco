#!/usr/bin/env python3
"""Bounded load and fault acceptance suite against an isolated production server.

All traffic targets temporary local fixtures. No deployed URL or credentials are
accepted. Reports contain timings and counts, never tokens or secret values.
"""
import argparse
import base64
import concurrent.futures
import datetime
import http.client
import json
import math
import os
from pathlib import Path
import resource
import secrets
import shutil
import socket
import sqlite3
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

sys.dont_write_bytecode = True
from production_test import Drill, until  # noqa: E402 -- repository-local fixture, no runtime dependencies.


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)]


class LoadSuite(Drill):
    def __init__(self, root, binaries, options):
        super().__init__(root, binaries)
        self.options = options
        self.environment.update({"GOMAXPROCS": "2", "GOMEMLIMIT": "768MiB"})
        self.epoch = 1
        self.report = {"schema": "moco.load-fault/v1", "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                       "configuration": {"workers": options.workers, "requestsPerWorker": options.requests_per_worker,
                                         "minimumDurationSeconds": options.duration, "payloadBytes": options.payload_bytes,
                                         "goMaxProcs": 2, "goMemoryLimit": "768MiB"},
                       "budgets": {"p95Milliseconds": options.p95_ms, "p99Milliseconds": options.p99_ms, "minimumRequestsPerSecond": options.min_rps,
                                   "serverRSSMiB": options.max_rss_mib, "WALMiB": options.max_wal_mib, "recoverySeconds": options.recovery_seconds},
                       "phases": [], "faults": {}, "resources": {"peakRSSBytes": 0, "peakWALBytes": 0}}
        self.sampling_stop = threading.Event()

    def sql(self, query, parameters=()):
        connection = sqlite3.connect(self.root / "data/moco.db", timeout=5)
        try:
            with connection:
                return connection.execute(query, parameters).fetchall()
        finally:
            connection.close()

    def request(self, route, *, method="GET", body=None, token=None, admin=False, request_id=None, headers=None,
                expected=None, transport_failure=False, timeout=5):
        header = dict(headers or {})
        if token:
            header["Authorization"] = "Bearer " + token
        if request_id:
            header["X-Request-ID"] = request_id
        payload = None if body is None else json.dumps(body).encode()
        if payload is not None:
            header["Content-Type"] = "application/json"
        request = urllib.request.Request((self.admin_base if admin else self.base)+route, payload, header, method=method)
        started = time.monotonic()
        try:
            try:
                response = urllib.request.urlopen(request, context=self.tls, timeout=timeout)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read()
                result = {"status": response.status, "seconds": time.monotonic()-started,
                          "headers": dict(response.headers), "body": json.loads(raw) if raw and "json" in response.headers.get("Content-Type", "") else raw.decode()}
        except (OSError, urllib.error.URLError, http.client.HTTPException, json.JSONDecodeError):
            if not transport_failure:
                raise
            return {"status": None, "seconds": time.monotonic()-started, "headers": {}, "body": None}
        if expected is not None:
            assert result["status"] in expected, f"unexpected HTTP {result['status']} for {method} {route.split('?')[0]}"
        return result

    def seed(self):
        self.start()
        self.tenants, self.vaults = [], []
        for index in range(3):
            tenant, _ = self.api("/api/v1/tenants", token=self.admin, method="POST", body={"name": f"load-tenant-{index}"}, expected=201)
            self.tenants.append(tenant["id"])
            vault, _ = self.api(f"/api/v1/tenants/{tenant['id']}/vaults", token=self.admin, method="POST", body={"name": "load-vault"}, expected=201)
            self.vaults.append(vault["id"])
        self.scope = f"/api/v1/tenants/{self.tenants[0]}/vaults/{self.vaults[0]}"
        self.foreign = f"/api/v1/tenants/{self.tenants[1]}/vaults/{self.vaults[1]}/secret?path=prod/foreign"
        self.value = base64.b64encode(b"l" * self.options.payload_bytes).decode()
        self.paths = [self.scope+f"/secret?path=prod/worker-{index}" for index in range(self.options.workers)]
        for path in [*self.paths, self.foreign]:
            self.api(path, token=self.admin, method="PUT", body={"value": self.value}, expected=201)
        policies, headers = self.api("/internal/v1/authorization", token=self.admin, admin=True)
        policies["policies"].append({"subject": "reader", "domain": self.tenants[0], "path": "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret",
                                     "method": "GET", "secretPathPrefix": "prod/"})
        self.api("/internal/v1/authorization", token=self.admin, admin=True, method="PUT", headers={"If-Match": headers["ETag"]},
                 body={"roleBindings": policies["roleBindings"], "policies": policies["policies"]})
        until(lambda: self.request(self.paths[0], token=self.reader)["status"] == 200)
        self.watch = f"/api/v1/tenants/{self.tenants[0]}/watch"
        self.quiet_watch = f"/api/v1/tenants/{self.tenants[2]}/watch"
        self.baseline_revision = self.api(self.watch, token=self.admin)[0]["resourceVersion"]

    def sample(self):
        while not self.sampling_stop.wait(0.05):
            try:
                status = Path(f"/proc/{self.server.pid}/status").read_text()
                rss = next(int(line.split()[1])*1024 for line in status.splitlines() if line.startswith("VmRSS:"))
                self.report["resources"]["peakRSSBytes"] = max(self.report["resources"]["peakRSSBytes"], rss)
                wal = self.root / "data/moco.db-wal"
                if wal.exists():
                    self.report["resources"]["peakWALBytes"] = max(self.report["resources"]["peakWALBytes"], wal.stat().st_size)
            except (OSError, StopIteration):
                pass  # Expected while the fault scenarios restart the fixture.

    def assert_pair(self, path, request_id, acknowledged=False):
        rows = self.sql("SELECT COUNT(*) FROM secrets WHERE path=?", (path,))[0][0]
        events = self.sql("SELECT COUNT(*) FROM audit_events WHERE request_id=? AND outcome='success' AND status_code BETWEEN 200 AND 299", (request_id,))[0][0]
        assert rows in (0, 1) and events == rows, "mutation/audit atomicity violated"
        if acknowledged:
            assert rows == 1, "acknowledged mutation was lost after recovery"
        return bool(rows)

    def run_load(self, name, maintenance=False):
        start_gate = threading.Event()
        maintenance_spans = {}
        quiet_revision = self.api(self.quiet_watch, token=self.admin)[0]["resourceVersion"]

        def worker(index):
            start_gate.wait()
            started, iteration, samples = time.monotonic(), 0, []
            expected_value = self.value
            while iteration < self.options.requests_per_worker or time.monotonic()-started < self.options.duration:
                request_id = f"load-{name}-{index}-{iteration}"
                operation = iteration % 6
                if operation == 0:
                    marker = (iteration//6 + (0 if name == "baseline" else 127)) % 256
                    expected_value = base64.b64encode(bytes([marker])*self.options.payload_bytes).decode()
                    result = self.request(self.paths[index], method="PUT", token=self.admin, body={"value": expected_value}, request_id=request_id, expected=(200,))
                elif operation == 1:
                    result = self.request(self.paths[index], token=self.reader, expected=(200,))
                    assert result["body"]["value"] == expected_value, "concurrent secret read was corrupted"
                elif operation == 2:
                    result = self.request(self.scope+"/secrets?prefix=prod/&limit=200", token=self.admin, expected=(200,))
                elif operation == 3:
                    result = self.request(self.watch+"?resourceVersion="+self.baseline_revision, token=self.admin, expected=(200,))
                    assert result["body"]["changed"], "watch lost a committed mutation"
                elif operation == 4:
                    result = self.request(self.paths[index], expected=(401,))
                else:
                    result = self.request(self.foreign, token=self.reader, expected=(403,))
                samples.append((result["seconds"], result["status"], operation == 0))
                iteration += 1
            return samples

        def quiet_watch():
            start_gate.wait()
            result = self.request(self.quiet_watch+"?resourceVersion="+quiet_revision+"&timeoutSeconds=1", token=self.admin, expected=(200,))
            assert not result["body"]["changed"], "cross-tenant write leaked through watch"

        def rotate():
            start_gate.wait()
            started = time.monotonic()
            result = self.request("/internal/v1/encryption/rotation?limit=200", admin=True, token=self.admin, method="POST", expected=(200,))
            assert result["body"]["complete"] and result["body"]["rewrapped"] > 0, "rotation did not move old keys under load"
            maintenance_spans["rotation"] = (started, time.monotonic())

        def backup():
            start_gate.wait()
            started = time.monotonic()
            archive = self.backup()
            maintenance_spans["backup"] = (started, time.monotonic())
            return archive

        with concurrent.futures.ThreadPoolExecutor(max_workers=self.options.workers+3) as executor:
            futures = [executor.submit(worker, index) for index in range(self.options.workers)]
            watchers = [executor.submit(quiet_watch)]
            if maintenance:
                watchers.extend([executor.submit(rotate), executor.submit(backup)])
            started = time.monotonic()
            start_gate.set()
            samples = [sample for future in futures for sample in future.result()]
            load_elapsed = time.monotonic()-started
            for future in watchers:
                future.result()
        if maintenance:
            assert set(maintenance_spans) == {"backup", "rotation"}
            assert all(begin < started+load_elapsed and end > started for begin, end in maintenance_spans.values()), "maintenance did not overlap live load"
        latencies = [sample[0]*1000 for sample in samples]
        writes = sum(sample[2] for sample in samples)
        status_counts = {str(status): sum(sample[1] == status for sample in samples) for status in (200, 401, 403)}
        count, unique = self.sql("SELECT COUNT(*),COUNT(DISTINCT request_id) FROM audit_events WHERE request_id LIKE ? AND outcome='success'", ("load-"+name+"-%",))[0]
        assert count == unique == writes, "missing or duplicated transactional audit under load"
        self.api("/readyz")
        metrics = {"name": name, "requests": len(samples), "successfulMutations": writes, "unexpectedErrors": 0,
                   "statusCounts": status_counts, "seconds": round(load_elapsed, 3), "requestsPerSecond": round(len(samples)/load_elapsed, 2),
                   "p50Milliseconds": round(percentile(latencies, .50), 3), "p95Milliseconds": round(percentile(latencies, .95), 3),
                   "p99Milliseconds": round(percentile(latencies, .99), 3), "maintenanceConcurrent": maintenance}
        self.report["phases"].append(metrics)
        assert metrics["p95Milliseconds"] <= self.options.p95_ms, "p95 latency budget exceeded"
        assert metrics["p99Milliseconds"] <= self.options.p99_ms, "p99 latency budget exceeded"
        assert metrics["requestsPerSecond"] >= self.options.min_rps, "minimum throughput budget missed"

    def cas(self):
        before, headers = self.api(self.paths[0], token=self.admin)
        barrier = threading.Barrier(self.options.workers)

        def contender(index):
            barrier.wait(timeout=10)
            value = base64.b64encode(f"cas-winner-{index}".encode()).decode()
            result = self.request(self.paths[0], method="PUT", token=self.admin, body={"value": value}, headers={"If-Match": headers["ETag"]},
                                  request_id=f"fault-cas-{index}", expected=(200, 412))
            return result["status"], value
        with concurrent.futures.ThreadPoolExecutor(max_workers=self.options.workers) as executor:
            results = list(executor.map(contender, range(self.options.workers)))
        winners = [value for status, value in results if status == 200]
        assert len(winners) == 1, "conditional update had more than one winner"
        events = self.sql("SELECT COUNT(*) FROM audit_events WHERE request_id LIKE 'fault-cas-%' AND outcome='success'")[0][0]
        assert events == 1, "conditional race did not commit exactly one success audit"
        after, after_headers = self.api(self.paths[0], token=self.admin)
        assert after["value"] == winners[0] and after["version"] == before["version"]+1 and after_headers["ETag"] != headers["ETag"]
        self.report["faults"]["conditionalWriteRace"] = {"winners": 1, "preconditionFailures": self.options.workers-1}

    def audit_fault(self):
        before = self.api(self.watch, token=self.admin)[0]["resourceVersion"]
        self.sql("CREATE TRIGGER load_reject_audit BEFORE INSERT ON audit_events WHEN NEW.request_id LIKE 'fault-audit-%' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
        try:
            def worker(index):
                request_id = f"fault-audit-{index}"
                self.request(self.scope+"/secret?path=prod/"+request_id, method="PUT", token=self.admin, body={"value": self.value}, request_id=request_id, expected=(503,))
                assert not self.assert_pair("prod/"+request_id, request_id)
            with concurrent.futures.ThreadPoolExecutor(max_workers=self.options.workers) as executor:
                list(executor.map(worker, range(self.options.workers)))
            assert self.api(self.watch, token=self.admin)[0]["resourceVersion"] == before
            self.api("/readyz", expected=503)
        finally:
            self.sql("DROP TRIGGER load_reject_audit")
        self.stop()
        self.start(epoch=self.epoch)
        self.report["faults"]["auditUnavailable"] = {"rejectedMutations": self.options.workers, "partialCommits": 0, "readyAfterRestart": True}

    def busy_fault(self):
        request_id = "fault-busy-"+secrets.token_hex(4)
        connection = sqlite3.connect(self.root / "data/moco.db", timeout=5)
        try:
            connection.execute("BEGIN IMMEDIATE")
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
                pending = executor.submit(self.request, self.scope+"/secret?path=prod/"+request_id, method="PUT", token=self.admin,
                                          body={"value": self.value}, request_id=request_id, expected=(201, 500, 503), timeout=10)
                time.sleep(.2)
                connection.rollback()
                result = pending.result(timeout=12)
        finally:
            connection.close()
        committed = self.assert_pair("prod/"+request_id, request_id, acknowledged=result["status"] == 201)
        assert committed == (result["status"] == 201)
        self.stop()
        self.start(epoch=self.epoch)
        self.report["faults"]["databaseWriterContention"] = {"httpStatus": result["status"], "atomicOutcome": True}

    def quota_fault(self):
        request_id = "fault-quota-"+secrets.token_hex(4)
        pid = self.server.pid
        original = resource.prlimit(pid, resource.RLIMIT_FSIZE)
        try:
            # A per-child kernel quota exercises real WAL write errors without
            # filling the developer/runner filesystem or affecting other jobs.
            resource.prlimit(pid, resource.RLIMIT_FSIZE, (1024, original[1]))
            result = self.request(self.scope+"/secret?path=prod/"+request_id, method="PUT", token=self.admin,
                                  body={"value": base64.b64encode(b"q"*(256<<10)).decode()}, request_id=request_id,
                                  expected=(500, 503), transport_failure=True, timeout=10)
        finally:
            if self.server.poll() is None:
                resource.prlimit(pid, resource.RLIMIT_FSIZE, original)
                self.stop()
        self.start(epoch=self.epoch)
        assert not self.assert_pair("prod/"+request_id, request_id)
        self.report["faults"]["walFileSizeQuota"] = {"httpStatus": result["status"], "partialCommits": 0, "readyAfterRestart": True}

    def slow_client(self):
        request_id = "fault-slow-"+secrets.token_hex(4)
        route = self.scope+"/secret?path=prod/"+request_id
        with socket.create_connection(("127.0.0.1", self.public_port), timeout=5) as transport:
            with self.tls.wrap_socket(transport, server_hostname="localhost") as connection:
                header = f"PUT {route} HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer {self.admin}\r\nX-Request-ID: {request_id}\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n"
                connection.sendall(header.encode()+b"{")
                time.sleep(.1)
        until(lambda: self.request("/readyz")["status"] == 200, seconds=5)
        assert not self.assert_pair("prod/"+request_id, request_id)
        self.report["faults"]["cancelledSlowUpload"] = {"partialCommits": 0, "readyAfterDisconnect": True}

    def crash(self):
        submitted, acknowledged, lock = {}, set(), threading.Lock()
        stop, enough = threading.Event(), threading.Event()
        prefix = "fault-crash-"+secrets.token_hex(3)+"-"

        def worker(index):
            sequence = 0
            while not stop.is_set():
                request_id = f"{prefix}{index}-{sequence}"
                path = "prod/"+request_id
                with lock:
                    submitted[request_id] = path
                result = self.request(self.scope+"/secret?path="+path, method="PUT", token=self.admin,
                                      body={"value": self.value}, request_id=request_id, expected=(201,), transport_failure=True)
                if result["status"] == 201:
                    with lock:
                        acknowledged.add(request_id)
                        if len(acknowledged) >= self.options.workers:
                            enough.set()
                sequence += 1
        with concurrent.futures.ThreadPoolExecutor(max_workers=self.options.workers) as executor:
            futures = [executor.submit(worker, index) for index in range(self.options.workers)]
            try:
                assert enough.wait(15), "crash workload did not reach acknowledged writes"
                stop.set()
                self.stop(abrupt=True)
            finally:
                stop.set()
            for future in futures:
                future.result()
        self.start(epoch=self.epoch)
        for request_id, path in submitted.items():
            self.assert_pair(path, request_id, acknowledged=request_id in acknowledged)
            if request_id in acknowledged:
                restored, _ = self.api(self.scope+"/secret?path="+path, token=self.reader)
                assert restored["value"] == self.value, "acknowledged value changed after crash recovery"
        assert acknowledged
        self.report["faults"]["sigkillDuringWrites"] = {"submitted": len(submitted), "acknowledged": len(acknowledged),
                                                      "acknowledgedWritesLost": 0, "unpairedMutations": 0}

    def recover(self):
        checkpoint_path = self.scope+"/secret?path=prod/recovery-checkpoint"
        checkpoint_value = base64.b64encode(b"verified-load-recovery-checkpoint").decode()
        self.api(checkpoint_path, method="PUT", token=self.admin, body={"value": checkpoint_value}, expected=201)
        before, headers = self.api(checkpoint_path, token=self.reader)
        archive = self.backup()
        self.stop()
        shutil.rmtree(self.root / "data")
        (self.root / "data").mkdir(mode=0o700)
        started = time.monotonic()
        self.ops("moco-backup", ["restore", "--backup", self.root / "mirror" / archive, "--database", self.root / "data/moco.db", "--keyring", self.credentials / "keyring.json"])
        self.start(epoch=self.epoch)
        after, after_headers = self.api(checkpoint_path, token=self.reader)
        assert after == before and after_headers["ETag"] == headers["ETag"], "load recovery changed committed data"
        self.api(self.foreign, token=self.reader, expected=403)
        elapsed = time.monotonic()-started
        assert elapsed <= self.options.recovery_seconds, "fixture recovery time budget exceeded"
        self.report["faults"]["mirroredRecovery"] = {"seconds": round(elapsed, 3), "etagPreserved": True, "tenantIsolationPreserved": True}

    def run(self):
        started = time.monotonic()
        self.seed()
        sampler = threading.Thread(target=self.sample, daemon=True)
        sampler.start()
        try:
            self.run_load("baseline")
            self.stop()
            keyring = json.loads((self.credentials / "keyring.json").read_text())
            keyring["activeKeyId"] = "root-v2"
            keyring["keys"]["root-v2"] = base64.b64encode(secrets.token_bytes(32)).decode()
            (self.credentials / "keyring.json").write_text(json.dumps(keyring))
            self.epoch = 2
            self.start(epoch=2)
            self.run_load("backup-rotation", maintenance=True)
            self.cas()
            self.audit_fault()
            self.busy_fault()
            self.quota_fault()
            self.slow_client()
            self.crash()
            self.recover()
            assert self.report["resources"]["peakRSSBytes"] > 0, "RSS sampling did not run"
            assert self.report["resources"]["peakRSSBytes"] <= self.options.max_rss_mib*(1<<20), "server RSS budget exceeded"
            assert self.report["resources"]["peakWALBytes"] <= self.options.max_wal_mib*(1<<20), "WAL growth budget exceeded"
            self.report.update({"status": "passed", "totalSeconds": round(time.monotonic()-started, 3)})
        finally:
            self.sampling_stop.set()
            sampler.join(timeout=5)
        self.stop()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--requests-per-worker", type=int, default=12)
    parser.add_argument("--duration", type=float, default=0, help="minimum seconds per load phase")
    parser.add_argument("--payload-bytes", type=int, default=4096)
    parser.add_argument("--p95-ms", type=float, default=1000)
    parser.add_argument("--p99-ms", type=float, default=2000)
    parser.add_argument("--min-rps", type=float, default=5)
    parser.add_argument("--max-rss-mib", type=float, default=512)
    parser.add_argument("--max-wal-mib", type=float, default=128)
    parser.add_argument("--recovery-seconds", type=float, default=30)
    parser.add_argument("--report", type=Path)
    options = parser.parse_args()
    if not sys.platform.startswith("linux") or not hasattr(resource, "prlimit"):
        parser.error("this isolated production suite requires Linux /proc and per-process resource.prlimit")
    if not 2 <= options.workers <= 128 or not 6 <= options.requests_per_worker <= 100000 or not 1 <= options.payload_bytes <= 1<<20 or not 0 <= options.duration <= 3600:
        parser.error("require workers 2..128, requests 6..100000, payload 1..1MiB and duration 0..3600")
    if any(value <= 0 for value in (options.p95_ms, options.p99_ms, options.min_rps, options.max_rss_mib, options.max_wal_mib, options.recovery_seconds)):
        parser.error("latency, throughput, memory, WAL and recovery budgets must be positive")
    with tempfile.TemporaryDirectory(prefix="moco-load-fault-") as temporary:
        suite = LoadSuite(Path(temporary), options.bin_dir.resolve(), options)
        try:
            suite.run()
        except Exception:
            suite.report["status"] = "failed"
            raise
        finally:
            suite.close()
            encoded = json.dumps(suite.report, sort_keys=True)
            if options.report:
                options.report.parent.mkdir(parents=True, exist_ok=True)
                with tempfile.NamedTemporaryFile(mode="w", dir=options.report.parent, delete=False) as report:
                    report.write(encoded+"\n")
                    report.flush()
                    os.fsync(report.fileno())
                os.replace(report.name, options.report)
            print(encoded, flush=True)


if __name__ == "__main__":
    main()
