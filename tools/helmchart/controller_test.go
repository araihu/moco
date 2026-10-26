//go:build helmtest

package helmchart

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerChartSchemaAndCRDPackaging(t *testing.T) {
	chart := "charts/moco-controller"
	values := chart + "/ci/test-values.yaml"
	data, err := helm(t, "template", "management", chart, "--namespace", "platform", "--include-crds", "-f", values)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, data)
	}
	if strings.Count(string(data), "kind: CustomResourceDefinition") != 3 || strings.Contains(string(data), "kind: ClusterRole") || strings.Contains(string(data), "kind: Secret\n") {
		t.Fatal("chart must install three CRDs with namespace-scoped access and no credentials")
	}
	if !strings.Contains(string(data), "--namespace=platform") {
		t.Fatal("controller not restricted to release namespace")
	}
	for _, setting := range []string{"adminURL=http://insecure:9443", "image.digest=latest", "credentialsSecret="} {
		if data, err := helm(t, "template", "management", chart, "-f", values, "--set", setting); err == nil {
			t.Fatalf("unsafe values %s accepted: %s", setting, data)
		}
	}
	if data, err := helm(t, "lint", "--strict", chart, "-f", values); err != nil {
		t.Fatalf("lint: %v\n%s", err, data)
	}
	destination := t.TempDir()
	if data, err := helm(t, "package", chart, "--destination", destination); err != nil {
		t.Fatalf("package: %v\n%s", err, data)
	}
	archives, err := filepath.Glob(filepath.Join(destination, "moco-controller-*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one controller package: %v %v", archives, err)
	}
	file, err := os.Open(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	crds := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(header.Name, "moco-controller/crds/") {
			crds++
		}
		if strings.HasPrefix(header.Name, "moco-controller/tests/") || strings.HasPrefix(header.Name, "moco-controller/ci/") {
			t.Fatalf("test fixture packaged: %s", header.Name)
		}
	}
	if crds != 3 {
		t.Fatalf("package contains %d CRDs, expected three", crds)
	}
}

func TestControllerAdminIsolationWithSkippedSchema(t *testing.T) {
	for _, settings := range []string{"controllerAccess.enabled=true", "controllerAccess.enabled=true,networkPolicy.enabled=false"} {
		if data, err := helm(t, "template", "test", "charts/moco", "-f", "charts/moco/ci/test-values.yaml", "--skip-schema-validation", "--set", settings); err == nil {
			t.Fatalf("unsafe controller access accepted: %s", data)
		}
	}
}
