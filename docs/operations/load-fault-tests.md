# Load and fault acceptance

`make load-test` builds the actual server/maintenance executables and runs an
isolated Linux production-profile instance with TLS, per-principal authorization,
transactional audit and SQLite WAL/FULL durability. Every credential and database
is generated in a private temporary directory. The suite accepts no deployed
URL or production credentials. It requires Python 3 with its standard Linux
`resource.prlimit` support, OpenSSL and `/proc`.

```sh
make load-test LOAD_REPORT=/tmp/moco-load-fault.json
make check
```

The bounded CI profile is included in `make check`, including the Dagger check.
Use the JSON report to compare runs on the **same** resource/storage profile;
fixture measurements are not a production capacity guarantee.

## Workload and budgets

The default profile uses eight workers, twelve operations per worker per load
phase and 4 KiB secret values. Both phases use the server's production Go
settings (`GOMAXPROCS=2`, `GOMEMLIMIT=768MiB`).
The native harness does not impose Docker cgroup CPU/RAM limits; record the host
and container runner resources when interpreting results. The second phase runs concurrently
with a fully verified mirrored backup and root-key rotation. Traffic mixes
updates, plaintext value verification, metadata listing, tenant watches,
unauthenticated rejection and cross-tenant denial. An independent tenant's
one-second watch must remain unchanged. Each successful measured write must
have exactly one committed success audit event.

Acceptance budgets are explicit and configurable:

| Measurement | Default |
| --- | --- |
| Unexpected load request errors | zero |
| p95 request latency | at most 1,000 ms |
| p99 request latency | at most 2,000 ms |
| Throughput per measured load phase | at least 5 requests/second |
| Peak server RSS | at most 512 MiB |
| Peak live WAL | at most 128 MiB |
| Small-fixture mirrored recovery | at most 30 seconds |

Expected 401/403 requests are included in request timing/status statistics, and
expected fault responses are reported separately from normal-load errors. The
separate long-poll watch and maintenance operations do not distort normal request
percentiles. RSS and WAL are sampled during execution; peak measurements are
sampled maxima, not kernel high-water guarantees. The report records the profile,
budgets, status counts, successful mutations, p50/p95/p99, throughput, resource
peaks and fault outcomes. It contains no credential or secret values.

An example longer run for a staging host is:

```sh
make load-test LOAD_REPORT=/tmp/moco-load-staging.json \
  LOAD_ARGS='--workers 16 --requests-per-worker 200 --duration 30 --payload-bytes 32768 --p95-ms 1000 --p99-ms 2000 --min-rps 20'
```

`--duration` is a minimum duration for **each** load phase, in addition to the
minimum operation count. All budgets can be overridden through `LOAD_ARGS`;
retain the chosen report with the release evidence and document the host's CPU,
RAM and disk configuration. Increase concurrency/payloads in steps to locate
saturation. Agree application-specific throughput and latency objectives before
calling the measured deployment production-ready. The fixture's 30-second
recovery budget is distinct from the deployment's RPO 30 minutes/RTO 15 minutes
acceptance targets in [production-sqlite.md](production-sqlite.md).

## Fault invariants

- **Conditional write race:** every contender uses the same ETag; exactly one
  update succeeds, other contenders receive 412, and the version advances once.
- **Audit unavailable:** concurrent writes are rejected with 503; no secret,
  success audit or watch-revision change may escape. Readiness reports the
  incident and becomes ready after the fault is removed and the server restarts.
- **SQLite writer contention:** a separate connection holds `BEGIN IMMEDIATE`.
  The competing request must finish within its timeout. Its data and success
  audit must either both commit or both be absent; transient failure statuses are
  recorded rather than counted as normal-load successes.
- **WAL write quota:** `resource.prlimit` restricts file size only on the
  temporary server process, forcing real filesystem write errors. The suite
  restores the process limit, restarts and requires no partial data/audit commit.
  This tests a per-process file-size failure, not physical disk exhaustion.
- **Cancelled slow upload:** a valid principal sends an incomplete body and
  disconnects. No mutation may commit, and readiness must recover promptly.
- **SIGKILL during writes:** producers create uniquely identified secrets;
  acknowledged writes must survive restart. Every submitted operation, including
  ones with an uncertain client outcome, must have either both data and exactly
  one success audit or neither.
- **Mirrored recovery:** a final verified backup is restored after deleting the
  original SQLite directory. A known checkpoint's value/ETag and tenant isolation
  must survive, and the recovered service must become ready within the budget.

Fault injection, abrupt termination and directory removal are scoped to the
suite's own processes and temporary directories. Failure exits nonzero and
still writes the available JSON report. The suite complements unit tests that
inject audit insert/deferred-commit failures, and the existing operational drill
covering policy/startup audit rollback, historical keys and backup failures.

## Dagger bootstrap compatibility

CI pins CLI and engine to Dagger v0.21.11, with the engine image pinned by digest.
The engine supplies the built-in Go SDK and its code generator. All runner lanes
install the selected CLI for the job, and the job overrides runner-wide engine
endpoints. This prevents a newer CLI from silently loading an older engine's
SDK. The module keeps Go 1.27.3, matching the application floor; it does not lower
the module's Go version to accommodate the old generator.

`dagger develop` first regenerates bindings, and CI rejects any dependency
manifest changes. `dagger functions` then loads type definitions through the
real engine, before tool builds. Then
`dagger call build-tools entries` and `dagger call check --run-nonce=...` verify
the complete build/check path. Generated SDK bindings must be regenerated by
Dagger; repository OpenTelemetry compatibility/patched dependency policy stays
in force during bootstrap and module scans. The local SDK adapter delegates
bindings to the engine's Go generator, restores the reviewed `go.mod`/`go.sum`
after generation, and compiles with a digest-pinned Go 1.27.3 image and
`-mod=readonly`. This prevents the generator's unconditional legacy Logs
replacements from downgrading the compiled runtime. Do not accept the generated
legacy versions as the application dependency policy. Remove the adapter once
the upstream SDK preserves the patched dependencies itself, after repeating
these checks against a fresh checkout.

For subsequent Go upgrades, validate this early bootstrap against the selected
engine's bundled generator too;
setting `GOTOOLCHAIN` only on later build containers cannot fix an older bootstrap.
