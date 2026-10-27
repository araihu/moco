package ci_test

import (
	"encoding/json"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestWorkflowUsesPinnedDaggerAndIsolatedRunners(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/check.yml")

	required := []string{
		"hostinger-vps-pr",
		"hostinger-vps-trusted",
		"dagger/dagger-for-github@27b130bf0f79a7f6fbbbe0fbca6760dc9bb40a77",
		"version: '0.21.11'",
		"dagger --silent call build-tools entries",
		"dagger functions",
		"dagger develop",
		"git diff --exit-code -- dagger.json dagger/go.mod dagger/go.sum",
		"docker-image://registry.dagger.io/engine:v0.21.11@sha256:6b557cc8b5fdfee0e2e8c82d451ac922097a1255482143e1b96c0be3ff2a303c",
		"dagger --silent call check --run-nonce=\"$RUN_NONCE\"",
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Fatalf("workflow is missing %q", value)
		}
	}
	if strings.Contains(workflow, "actions/setup-go@") {
		t.Fatal("the host runner must not install Go outside Dagger")
	}
	if strings.Contains(workflow, "if: runner.environment == 'github-hosted'") {
		t.Fatal("self-hosted runners must install the same pinned CLI as hosted runners")
	}
	if strings.Index(workflow, "dagger --silent call build-tools entries") > strings.Index(workflow, "dagger --silent call check") {
		t.Fatal("the explicit Go tool build must precede the required checks")
	}
	if strings.Index(workflow, "dagger functions") > strings.Index(workflow, "dagger --silent call build-tools entries") {
		t.Fatal("current-Go bootstrap must pass before tools are built")
	}
}

func TestReleasePublicationRequiresValidatedSource(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	if !strings.Contains(workflow, "tags: ['v*', '[0-9]*']") || strings.Contains(workflow, "types: [published]") || strings.Contains(workflow, "workflow_dispatch:") {
		t.Fatal("publication must originate from a version tag before the release becomes immutable")
	}
	if !strings.Contains(workflow, "needs: [prepare, validate]") || !strings.Contains(workflow, "uses: ./.github/workflows/check.yml") || !strings.Contains(readRepoFile(t, ".github/workflows/check.yml"), "workflow_call:") {
		t.Fatal("publication must reuse the complete check workflow after source validation")
	}
	publish := strings.Index(workflow, "\n  publish:")
	if publish < 0 || strings.Contains(workflow[:publish], "contents: write") || strings.Contains(workflow[:publish], "packages: write") {
		t.Fatal("write permissions must be confined to publication")
	}
	if !strings.Contains(workflow[publish:], "ref: ${{ needs.prepare.outputs.commit }}") || !strings.Contains(workflow, "platforms: linux/amd64,linux/arm64") {
		t.Fatal("release images must use the validated commit and both Linux architectures")
	}
	for _, required := range []string{"controller_image_repository", "--component controller", "context: controller", "scope=moco-controller-release", "--controller-image-digest", "--controller-chart charts/moco-controller", "publish-release"} {
		if !strings.Contains(workflow[publish:], required) {
			t.Fatalf("controller artifacts missing from release publication: %s", required)
		}
	}
	if strings.Count(workflow[publish:], "platforms: linux/amd64,linux/arm64") != 2 {
		t.Fatal("both release images must contain the two supported architectures")
	}
	for _, match := range regexp.MustCompile(`(?m)^\s+(?:-\s+)?uses:\s+(\S+)`).FindAllStringSubmatch(workflow, -1) {
		if strings.HasPrefix(match[1], "./") {
			continue
		}
		if !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(match[1]) {
			t.Fatalf("release action is not commit-pinned: %s", match[1])
		}
	}
}

func TestSDKPreservesDependencyPolicyBeforeRuntimeCompilation(t *testing.T) {
	var config struct {
		SDK struct {
			Source string `json:"source"`
		} `json:"sdk"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "dagger.json")), &config); err != nil {
		t.Fatal(err)
	}
	if config.SDK.Source != "./dagger/sdk" {
		t.Fatal("Moco must use the SDK adapter that preserves reviewed manifests")
	}
	adapter := readRepoFile(t, "dagger/sdk/main.dang")
	if !strings.Contains(adapter, "golang:"+strings.TrimPrefix(repoGoVersion(t, "go.mod"), "go")+"-bookworm@sha256:") {
		t.Fatal("SDK compiler image must match the application's Go floor and use a digest")
	}
	for _, value := range []string{
		`modSource.withSDK("go").generatedContextChangeset.after`,
		`.withFile(path + "/go.mod", original.file(path + "/go.mod"))`,
		`.withFile(path + "/go.sum", original.file(path + "/go.sum"))`,
		`.withEnvVariable("GOTOOLCHAIN", "local")`,
		`.withEnvVariable("GOFLAGS", "-mod=readonly")`,
	} {
		if !strings.Contains(adapter, value) {
			t.Fatalf("SDK adapter is missing policy guard %q", value)
		}
	}
}

func TestSharedDaggerDependenciesUseImmutablePins(t *testing.T) {
	var config struct {
		EngineVersion string `json:"engineVersion"`
		Dependencies  []struct {
			Source string `json:"source"`
			Pin    string `json:"pin"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "dagger.json")), &config); err != nil {
		t.Fatal(err)
	}
	if config.EngineVersion != "v0.21.11" {
		t.Fatalf("engine version = %q, want v0.21.11", config.EngineVersion)
	}
	if len(config.Dependencies) != 3 {
		t.Fatalf("shared dependency count = %d, want 3", len(config.Dependencies))
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, dependency := range config.Dependencies {
		if !commit.MatchString(dependency.Pin) {
			t.Fatalf("dependency pin %q is not a full commit", dependency.Pin)
		}
		if !strings.HasPrefix(dependency.Source, "github.com/araihu/dagger/modules/") ||
			!strings.HasSuffix(dependency.Source, "@"+dependency.Pin) {
			t.Fatalf("dependency source %q does not match its immutable pin", dependency.Source)
		}
	}
}

func TestGolangCILintUsesIsolatedModule(t *testing.T) {
	toolsModule := readRepoFile(t, "tools/go.mod")
	if strings.Contains(toolsModule, "github.com/golangci/golangci-lint") {
		t.Fatal("golangci-lint must keep its dependency graph separate from Vacuum")
	}
	linterModule := readRepoFile(t, "tools/golangci-lint/go.mod")
	if !strings.Contains(linterModule, "tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint") {
		t.Fatal("golangci-lint must be pinned as a Go-managed tool")
	}
}

func TestDaggerTelemetryUsesPatchedLogs(t *testing.T) {
	modules := []string{"go.opentelemetry.io/otel/log", "go.opentelemetry.io/otel/sdk/log",
		"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc", "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"}
	policy := readRepoFile(t, "dagger/go.mod")
	directory := t.TempDir()
	modfile := filepath.Join(directory, "go.mod")
	if err := os.WriteFile(modfile, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "go.sum"), []byte(readRepoFile(t, "dagger/go.sum")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository")
	}
	for _, module := range modules {
		command := exec.CommandContext(t.Context(), "go", "list", "-mod=readonly", "-modfile="+modfile, "-m", "-json", module) // #nosec G204 -- fixed module allowlist and a test-owned private modfile; no shell or remote input.
		command.Dir = filepath.Join(filepath.Dir(current), "..", "..", "dagger")
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("resolve telemetry policy: %v: %s", err, output)
		}
		var resolved struct {
			Version string
			Replace struct{ Version string }
		}
		if err := json.Unmarshal(output, &resolved); err != nil {
			t.Fatal(err)
		}
		if resolved.Version != "v0.21.0" || (resolved.Replace.Version != "" && resolved.Replace.Version != "v0.21.0") {
			t.Fatalf("SDK downgraded %s: %s", module, output)
		}
	}
}

func TestToolchainFloorsMatchApplication(t *testing.T) {
	application := repoGoVersion(t, "go.mod")
	for _, module := range []string{
		"tools/go.mod", "tools/actionlint/go.mod", "tools/golangci-lint/go.mod",
		"dagger/go.mod", "dagger/third_party/otel-go/go.mod",
	} {
		if toolchain := repoGoVersion(t, module); version.Compare(toolchain, application) < 0 {
			t.Errorf("%s permits %s, below application floor %s", module, toolchain, application)
		}
	}
}

func repoGoVersion(t *testing.T, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(readRepoFile(t, name), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" && version.IsValid("go"+fields[1]) {
			return "go" + fields[1]
		}
	}
	t.Fatalf("%s is missing a valid Go version directive", name)
	return ""
}

func TestDaggerModuleScanUsesRepositoryDependencyPolicy(t *testing.T) {
	daggerModule := readRepoFile(t, "dagger/main.go")
	for _, value := range []string{
		`WithFile("go.mod", m.Source.File("dagger/go.mod"))`,
		`WithFile("go.sum", m.Source.File("dagger/go.sum"))`,
	} {
		if !strings.Contains(daggerModule, value) {
			t.Fatalf("Dagger module scan is missing dependency overlay %q", value)
		}
	}
}

func TestDaggerCheckReturnsScalarOnlyAfterGeneratedDriftGate(t *testing.T) {
	daggerModule := readRepoFile(t, "dagger/main.go")
	drift := strings.Index(daggerModule, "dag.Generated().AssertClean(")
	success := strings.Index(daggerModule, `return "ok", nil`)
	if drift == -1 || success == -1 || success < drift {
		t.Fatal("Dagger check must return its success scalar only after generated drift validation")
	}
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	root, err := os.OpenRoot(filepath.Join(filepath.Dir(current), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close repository root: %v", closeErr)
		}
	})
	data, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
