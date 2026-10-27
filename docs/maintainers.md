# Maintainers

This page covers changing Moco, running its acceptance suites and publishing
releases or documentation. To install or operate a deployment, use the
[user guides](README.md).

## Development

The repository contains separate Go modules for the server, CLI, controller,
Dagger and pinned development tools. Use Go 1.27.3 or newer. Python 3 and OpenSSL
are required for operational drills; Docker and Helm are required for image
and chart checks. Run commands from the repository root unless noted otherwise.

```sh
make run                   # development server; supply the development environment first
make cli-build             # writes bin/moco
make controller-check      # controller generation, tests, lint, scans and vet
make check                 # complete application gate, including CLI and operational drills
```

[Architecture and development details](development.md) describe ports/adapters,
module boundaries, the repository layout and tool isolation. Do not edit generated
Go or CLI runtime files directly.

## Contracts and CLI generation

API source is in `openapi/public.yaml` and `openapi/internal.yaml`. CLI command
metadata belongs in separate `openapi/overlays/cli.*.yaml` files. Vacuum applies
the overlays before the generic `oapi-cli-gen` generator consumes the bundle.
The CLI is owned by Moco's `cli/` module; the generator remains a separate project.

```sh
make spec-lint
make api-generate
make cli-generate
make cli-check-generated
make sqlc-generate
make check
```

Review generated changes with the contract change. The generator's manifest
rejects overwriting locally edited generated files. Upgrade its pin in
`cli/tools/go.mod`, tidy that module, regenerate and run `make cli-check`.
See [OpenAPI source and overlay instructions](../openapi/README.md) for exact
bundle locations, lint rules and internal-contract generation.

## Acceptance tests

| Check | Purpose |
| --- | --- |
| `make operational-test` | Real executables, TLS, permissions, restart, backups, key rotation and recovery |
| `make load-test LOAD_REPORT=/tmp/moco-load.json` | Bounded concurrency, latency/resource budgets and fault injection |
| `make chart-check` | Both charts' helm-unittest suites, schemas, rendering and package checks |
| `make image-check IMAGE=moco:REVIEWED_COMMIT` | Production OCI runtime and recovery checks |
| `make monitoring-check` | Prometheus rules and alarm behavior |
| `make vind-test` | Published images/charts, three CRDs and ESO in a disposable real cluster |
| `make vind-source-test` | Controller source image and source charts in the same cluster harness |

The [load and fault suite](operations/load-fault-tests.md) documents workload
budgets and the Dagger/SDK compatibility adapter. The
[vind/ESO acceptance procedure](operations/kubernetes-eso-tests.md) documents
component pins, cluster prerequisites, all scenarios and retained reports.
These fixtures do not validate an operator's storage driver, off-host key custody
or alert delivery.

Chart suites live in `charts/*/tests/`. The wrapper downloads and verifies a
pinned helm-unittest plugin without changing global Helm plugins. `make chart-check`
then uses the real selected Helm executable; CI exercises Helm 3 and 4 and
validates rendered Kubernetes objects. Use named document selectors and explicit
assertions. Chart tests are excluded from packaged charts. JUnit reports are
written under `bin/`; set `CHART_TEST_REPORT` to choose another server-chart path.

Transactional-audit tests inject audit-insert and final-commit failures and
verify that business state, idempotency and watch revisions roll back together.
For changes to this boundary, run the database, HTTP and server packages with
`go test -race`, then the operational and image checks. Keep the guarantee about
HTTP changes separate from privileged offline SQL, migrations and restore.

## CI and dependency maintenance

`make check` is the local application gate. CI runs it through Dagger, rejects
generated drift, and runs image/monitoring and chart gates separately. Keep the
CLI and engine pinned together. Validate `dagger functions` before tool builds
when upgrading Go or Dagger; a later build-container toolchain cannot repair
an incompatible SDK bootstrap.

The local SDK adapter preserves reviewed module manifests after binding
generation. The OpenTelemetry compatibility patch and its removal conditions
are documented in [the third-party adapter notes](../dagger/third_party/otel-go/README.moco.md).
Update isolated tools modules as well as runtime modules when addressing a
vulnerability, and scan the compiled tools with the selected Go version.

## Publishing a release

Use an immutable stable SemVer tag only after the required checks pass. The
release workflow publishes multi-platform server and controller images,
digest-pinned Helm charts, `release.json` and checksums. Retain artifact and
acceptance evidence; do not move a published tag to correct documentation.

Follow [the complete release procedure](operations/releases.md) for package
visibility, signing identities, verification and recovery from partial publication.
The software release, OpenAPI `info.version` and the `/v1/` route namespace are
independent. An API namespace change is not implied by a patch release.

## Release evidence

[Release 0.0.3 evidence](releases/0.0.3.md) records the published identities and
21-scenario vind/ESO acceptance. Keep historical results tied to the artifacts
that were tested. Put CI run links, scenario counts, test-host limits and SDK
bootstrap history here or in the linked records, rather than in user walkthroughs.

## Website maintenance

The website publishes user instructions plus this single maintainer entry.
`scripts/build_site.py` maps canonical Markdown sources to routes. Keep the home,
concepts, quick start, task guides and deployment reference focused on operating
Moco. Internal design plans are not published. Legacy maintainer routes redirect
to sections on this page and do not appear in search or navigation.

```sh
make site-check
make site-preview
```

The [site maintenance guide](../site/README.md) covers Margo/Manja pins, brand
provenance, Cloudflare credentials, deployment checks and rollback. For a docs
correction on the current release, commit and push the reviewed source, then run:

```sh
gh workflow run site.yml --repo araihu/moco --ref main -f release_tag=v0.0.3
```

Wait for build, deploy and live verification to pass. Check mobile navigation,
light/dark mode, search, links and operational command examples before publishing.
