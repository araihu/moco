# Moco CLI

This directory is the independent Go module `github.com/araihu/moco/cli`.
The application, generated client/runtime, module dependencies and acceptance
tests belong to Moco. The generic generator is
[oapi-cli-gen](https://github.com/araihu/oapi-cli-gen), pinned in `tools/go.mod`.
It is used only during development and CI; the built CLI runs independently.

From the Moco repository root:

```sh
make cli-build                      # writes bin/moco
make -C cli install                 # installs moco into GOBIN (or GOPATH/bin)
make cli-generate                   # principal spec + overlay -> bundle -> CLI
make cli-check-generated            # read-only generated-source comparison
make cli-check                      # generation, tests, race, vet, scans, real server
```

Build or install the checked-in module directly with Go:

```sh
go -C cli build -o ../bin/moco ./cmd/moco
# Install the released source from this multi-module repository:
git clone --branch v0.0.3 --depth 1 https://github.com/araihu/moco.git
GOWORK=off go -C moco/cli install ./cmd/moco
```

Use the public API through the generated Cobra commands:

```sh
export MOCO_SERVER=https://moco.example.com:8443
# Provide MOCO_TOKEN through your credential mechanism.
bin/moco service info
bin/moco tenants list                  # table is the default
bin/moco tenants list --output json    # full response, including pagination
bin/moco tenants list --output csv     # one record per item, with headers
bin/moco secrets get --tenant-id TENANT --vault-id VAULT --path prod/database
bin/moco secrets put --help
bin/moco completion bash
```

`--body-file request.json` and `--body-stdin` supply JSON mutation bodies.
`--response-header ETag`, `--if-match` and `--if-none-match` support conditional
operations. TLS and authentication settings follow the generic runtime's
contract; no credentials are stored by the CLI.

Edit naming metadata in `../openapi/overlays/cli.public.yaml`, then run
`make cli-generate` from the repository root and review the resulting changes.
The manifest tracks generator-owned files and refuses to overwrite local edits.
Keep application tests and documentation outside generated files. Upgrading the
generator means updating its version in `tools/go.mod`, tidying that tools
module, regenerating the CLI and running `make cli-check`.

The CLI tests start a temporary local server with private credentials and SQLite
data. They cover resource lifecycles, secret round trips, output formats,
conditional writes, authentication and transactional audit. Internal-contract
generation/compilation acceptance also lives in this repository under `tools/`.
Root `make check` runs these checks, and Dagger rejects generated CLI drift.
