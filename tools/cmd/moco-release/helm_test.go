//go:build helmtest

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePackagesWithRealHelmAndLeavesSourceUntouched(t *testing.T) {
	source := filepath.Join("..", "..", "..", "charts", "moco")
	original := map[string][]byte{}
	for _, name := range []string{"Chart.yaml", "values.yaml", "values.schema.json"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = data
	}
	binary := os.Getenv("HELM")
	if binary == "" {
		binary = "helm"
	}
	run := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, binary, args...).CombinedOutput()
	}
	for _, tag := range []string{"v1.2.3", "v1.2.3-rc.1", "v1.2.3+build.007"} {
		version := strings.TrimPrefix(tag, "v")
		p, err := releasePlan(tag, testCommit, "araihu/moco", strings.Contains(strings.Split(version, "+")[0], "-"))
		if err != nil {
			t.Fatal(err)
		}
		path, err := packageChart(t.Context(), run, p, testDigest, source, t.TempDir())
		if err != nil {
			t.Fatal(tag, err)
		}
		if err := verifyChart(path, p, testDigest); err != nil {
			t.Fatal(tag, err)
		}
	}
	for name, expected := range original {
		actual, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("release modified source chart", name, err)
		}
	}
}
