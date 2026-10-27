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
	binary := os.Getenv("HELM")
	if binary == "" {
		binary = "helm"
	}
	run := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, binary, args...).CombinedOutput()
	}
	for _, name := range []string{"moco", "moco-controller"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join("..", "..", "..", "charts", name)
			original := map[string][]byte{}
			for _, file := range []string{"Chart.yaml", "values.yaml", "values.schema.json"} {
				data, err := os.ReadFile(filepath.Join(source, file))
				if err != nil {
					t.Fatal(err)
				}
				original[file] = data
			}
			for _, tag := range []string{"v1.2.3", "v1.2.3-rc.1", "v1.2.3+build.007"} {
				version := strings.TrimPrefix(tag, "v")
				p, err := releasePlan(tag, testCommit, "araihu/moco", strings.Contains(strings.Split(version, "+")[0], "-"))
				if err != nil {
					t.Fatal(err)
				}
				c := server(p)
				if name == "moco-controller" {
					c = controller(p)
				}
				path, err := packageChart(t.Context(), run, p, c, testDigest, source, t.TempDir())
				if err != nil {
					t.Fatal(tag, err)
				}
				if err := verifyChart(path, p, c, testDigest); err != nil {
					t.Fatal(tag, err)
				}
			}
			for file, expected := range original {
				actual, err := os.ReadFile(filepath.Join(source, file))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("release modified source chart", file, err)
				}
			}
		})
	}
}
