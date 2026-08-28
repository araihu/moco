package ci_test

import (
	"encoding/json"
	"os"
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
		"runner.environment == 'github-hosted'",
		"dagger/dagger-for-github@27b130bf0f79a7f6fbbbe0fbca6760dc9bb40a77",
		"version: '0.21.8'",
		"dagger call build-tools entries",
		"dagger call check --run-nonce=\"$RUN_NONCE\"",
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Fatalf("workflow is missing %q", value)
		}
	}
	if strings.Contains(workflow, "actions/setup-go@") {
		t.Fatal("the host runner must not install Go outside Dagger")
	}
	if strings.Index(workflow, "dagger call build-tools entries") > strings.Index(workflow, "dagger call check") {
		t.Fatal("the explicit Go tool build must precede the required checks")
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
	if config.EngineVersion != "v0.21.8" {
		t.Fatalf("engine version = %q, want v0.21.8", config.EngineVersion)
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

func TestGolangCILintRemainsPrecompiled(t *testing.T) {
	toolsModule := readRepoFile(t, "tools/go.mod")
	if strings.Contains(toolsModule, "github.com/golangci/golangci-lint") {
		t.Fatal("golangci-lint must not be compiled through the tools Go module")
	}

	daggerModule := readRepoFile(t, "dagger/main.go")
	for _, value := range []string{
		"golangCILintVersion",
		"golangCILintInstallerChecksum",
		"VerifiedDownload().Fetch",
	} {
		if !strings.Contains(daggerModule, value) {
			t.Fatalf("Dagger module is missing precompiled golangci-lint contract %q", value)
		}
	}
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
