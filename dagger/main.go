// Package main exposes Moco's portable build and validation pipeline.
package main

import (
	"context"

	"dagger/moco/internal/dagger"
)

const (
	golangCILintVersion           = "2.13.2"
	golangCILintInstallerURL      = "https://raw.githubusercontent.com/golangci/golangci-lint/27774aaf853a4fd21f1dd5e69439459dc1b26e68/install.sh"
	golangCILintInstallerChecksum = "1022ddb4d87ed252350ed03fc9677e250a4ae95cc6bcd4658c2a20a8a23d390f"
	toolsBinPath                  = "/opt/moco-tools/bin"
)

// Moco provides the repository CI contract.
type Moco struct {
	Source *dagger.Directory
}

func New(
	// Repository source. VCS metadata and local build caches are excluded.
	// +defaultPath="."
	// +ignore=[".git", ".dagger", "dagger/internal/dagger", "dagger/dagger.gen.go", "tools/bin"]
	source *dagger.Directory,
) *Moco {
	return &Moco{Source: source}
}

// BuildTools compiles all Go-managed tool binaries required by CI.
func (m *Moco) BuildTools() *dagger.Directory {
	toolsSource := dag.Directory().
		WithDirectory("tools", m.Source.Directory("tools"))

	return dag.GoCi(dagger.GoCiOpts{Source: toolsSource}).
		Base().
		WithWorkdir("/src/tools").
		WithEnvVariable("GOWORK", "off").
		WithEnvVariable("GOFLAGS", "-mod=readonly").
		WithEnvVariable("GOBIN", "/out").
		WithEnvVariable("GOMAXPROCS", "1").
		WithEnvVariable("GOMEMLIMIT", "2GiB").
		WithEnvVariable("GOGC", "50").
		WithDirectory("/out", dag.Directory()).
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithWorkdir("/src/tools/actionlint").
		WithExec([]string{"go", "install", "-p=1", "tool"}).
		WithExec([]string{
			"sh", "-euc",
			"for tool in actionlint vacuum oapi-codegen sqlc migrate-sqlite govulncheck; do test -x \"/out/$tool\"; done",
		}).
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

	ciTools := m.ciTools()

	checked := dag.GoCi(dagger.GoCiOpts{Source: m.Source}).
		Base().
		WithWorkdir("/src").
		WithEnvVariable("GOWORK", "off").
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
		WithFile("go.sum", m.Source.File("dagger/go.sum"))

	moduleChecked := dag.GoCi(dagger.GoCiOpts{Source: moduleSource}).
		Base().
		WithWorkdir("/src").
		WithEnvVariable("GOWORK", "off").
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

func (m *Moco) ciTools() *dagger.Directory {
	installer := dag.VerifiedDownload().Fetch(
		golangCILintInstallerURL,
		golangCILintInstallerChecksum,
		dagger.VerifiedDownloadFetchOpts{Name: "golangci-lint-install.sh"},
	)

	return dag.GoCi(dagger.GoCiOpts{Source: dag.Directory()}).
		Base().
		WithDirectory(toolsBinPath, m.BuildTools()).
		WithFile("/tmp/golangci-lint-install.sh", installer).
		WithExec([]string{
			"sh", "/tmp/golangci-lint-install.sh",
			"-b", toolsBinPath,
			"v" + golangCILintVersion,
		}).
		Directory(toolsBinPath)
}

func generatedSnapshot(source *dagger.Directory) *dagger.Directory {
	return dag.Directory().
		WithDirectory("openapi/bundled", source.Directory("openapi/bundled")).
		WithFile("internal/adapters/http/moco.gen.go", source.File("internal/adapters/http/moco.gen.go")).
		WithDirectory("internal/adapters/http/internalapi", source.Directory("internal/adapters/http/internalapi")).
		WithDirectory("internal/adapters/db/sqlc", source.Directory("internal/adapters/db/sqlc"))
}
