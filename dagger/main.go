// Package main exposes Moco's portable build and validation pipeline.
package main

import (
	"context"

	"dagger/moco/internal/dagger"
)

const toolsBinPath = "/opt/moco-tools/bin"

// Moco provides the repository CI contract.
type Moco struct {
	Source *dagger.Directory
}

func New(
	// Repository source. VCS metadata and local build caches are excluded.
	// +defaultPath="."
	// +ignore=[".git", ".dagger", "dagger/internal/dagger", "dagger/dagger.gen.go", "tools/bin", "bin"]
	source *dagger.Directory,
) *Moco {
	return &Moco{Source: source}
}

// BuildTools compiles all Go-managed tool binaries required by CI.
func (m *Moco) BuildTools() *dagger.Directory {
	toolsSource := dag.Directory().
		WithDirectory("tools", m.Source.Directory("tools")).
		WithDirectory("cli/tools", m.Source.Directory("cli/tools"))

	return dag.GoCi(dagger.GoCiOpts{Source: toolsSource}).
		Base().
		WithWorkdir("/src/tools").
		WithEnvVariable("GOWORK", "off").
		WithEnvVariable("GOTOOLCHAIN", "auto").
		WithEnvVariable("GOFLAGS", "-mod=readonly").
		WithEnvVariable("GOBIN", "/out").
		WithEnvVariable("GOMAXPROCS", "1").
		WithEnvVariable("GOMEMLIMIT", "2GiB").
		WithEnvVariable("GOGC", "50").
		WithDirectory("/out", dag.Directory()).
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithWorkdir("/src/tools/actionlint").
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithWorkdir("/src/tools/golangci-lint").
		WithEnvVariable("GOMEMLIMIT", "4GiB").
		WithEnvVariable("GOGC", "100").
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithWorkdir("/src/cli/tools").
		WithEnvVariable("GOBIN", "/out/cli").
		WithEnvVariable("GOMEMLIMIT", "2GiB").
		WithEnvVariable("GOGC", "50").
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithExec([]string{
			"sh", "-euc",
			"for tool in actionlint vacuum oapi-codegen sqlc migrate-sqlite govulncheck golangci-lint; do test -x \"/out/$tool\"; done",
		}).
		WithExec([]string{"sh", "-euc", "test -x /out/cli/oapi-cli-gen && test -x /out/cli/govulncheck"}).
		Directory("/out")
}

// Check runs the complete required CI contract and returns ok after rejecting generated drift.
// +cache="never"
func (m *Moco) Check(
	ctx context.Context,
	// A per-run value forces the checks to execute while preserving tool-build caching.
	// +optional
	runNonce string,
) (string, error) {
	if runNonce == "" {
		runNonce = "local"
	}

	ciTools := m.BuildTools()

	checked := dag.GoCi(dagger.GoCiOpts{Source: m.Source}).
		Base().
		WithWorkdir("/src").
		WithEnvVariable("GOWORK", "off").
		WithEnvVariable("GOTOOLCHAIN", "auto").
		WithEnvVariable("GOFLAGS", "-mod=readonly").
		WithDirectory(toolsBinPath, ciTools).
		WithEnvVariable("MOCO_RUN_NONCE", runNonce).
		WithExec([]string{
			"make", "check",
			"TOOL_BIN_DIR=" + toolsBinPath,
			"GOLANGCI_LINT=" + toolsBinPath + "/golangci-lint",
		})

	if _, err := checked.Sync(ctx); err != nil {
		return "", err
	}

	moduleSource := dag.CurrentModule().Source().
		WithFile("go.mod", m.Source.File("dagger/go.mod")).
		WithFile("go.sum", m.Source.File("dagger/go.sum")).
		WithDirectory("third_party/otel-go", m.Source.Directory("dagger/third_party/otel-go"))

	moduleChecked := dag.GoCi(dagger.GoCiOpts{Source: moduleSource}).
		Base().
		WithWorkdir("/src").
		WithEnvVariable("GOWORK", "off").
		WithEnvVariable("GOTOOLCHAIN", "auto").
		WithEnvVariable("GOFLAGS", "-mod=readonly").
		WithDirectory(toolsBinPath, ciTools).
		WithFile("/config/.golangci.yml", m.Source.File(".golangci.yml")).
		WithEnvVariable("MOCO_RUN_NONCE", runNonce).
		WithExec([]string{"go", "test", "./...", "-count=1"}).
		WithExec([]string{toolsBinPath + "/golangci-lint", "run", "--config", "/config/.golangci.yml", "./..."}).
		WithExec([]string{toolsBinPath + "/govulncheck", "-test", "./..."}).
		WithExec([]string{"go", "vet", "./..."})

	if _, err := moduleChecked.Sync(ctx); err != nil {
		return "", err
	}

	if err := dag.Generated().AssertClean(
		ctx,
		generatedSnapshot(m.Source),
		generatedSnapshot(checked.Directory("/src")),
	); err != nil {
		return "", err
	}

	return "ok", nil
}

func generatedSnapshot(source *dagger.Directory) *dagger.Directory {
	return dag.Directory().
		WithDirectory("openapi/bundled", source.Directory("openapi/bundled")).
		WithDirectory("cli", source.Directory("cli")).
		WithDirectory("controller/api", source.Directory("controller/api")).
		WithDirectory("charts/moco-controller/crds", source.Directory("charts/moco-controller/crds")).
		WithFile("internal/adapters/http/moco.gen.go", source.File("internal/adapters/http/moco.gen.go")).
		WithDirectory("internal/adapters/http/internalapi", source.Directory("internal/adapters/http/internalapi")).
		WithDirectory("internal/adapters/db/sqlc", source.Directory("internal/adapters/db/sqlc"))
}
