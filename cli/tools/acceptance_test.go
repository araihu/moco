package clitools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/araihu/oapi-cli-gen/generator"
)

// Keep the administrative contract's generation acceptance at the API owner.
func TestInternalContractGeneratesCompilableCommands(t *testing.T) {
	spec, err := filepath.Abs("../../openapi/bundled/cli/internal.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(t.Context(), generator.Options{
		Spec: spec, Name: "moco-admin", Module: "github.com/araihu/moco/cli/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "admin")
	if err := result.Write(directory, false); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "moco-admin")
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "-mod=readonly", "-o", binary, "./cmd/moco-admin"}} {
		command := exec.CommandContext(t.Context(), "go", args...)
		command.Dir = directory
		command.Env = append(os.Environ(), "GOWORK=off")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, output)
		}
	}
	for _, parts := range [][]string{{"health", "live"}, {"health", "ready"}, {"authorization", "get"}, {"authorization", "set"}, {"audit", "list"}, {"audit", "purge"}, {"encryption", "rotate"}, {"encryption", "status"}} {
		command := exec.CommandContext(t.Context(), binary, append(parts, "--help")...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("internal command %v: %v\n%s", parts, err, output)
		}
	}
}
