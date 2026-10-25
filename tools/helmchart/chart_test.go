//go:build helmtest

package helmchart

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func helm(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	binary := os.Getenv("HELM")
	if binary == "" {
		binary = "helm"
	}
	command := exec.CommandContext(t.Context(), binary, args...)
	command.Dir = filepath.Join("..", "..")
	return command.CombinedOutput()
}

func render(t *testing.T, extra ...string) []map[string]any {
	t.Helper()
	args := append([]string{"template", "test", "charts/moco", "--namespace", "moco", "--kube-version", "1.32.0", "-f", "charts/moco/ci/test-values.yaml"}, extra...)
	data, err := helm(t, args...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, data)
	}
	var objects []map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var object map[string]any
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(object) > 0 {
			objects = append(objects, object)
		}
	}
	return objects
}

func at(value map[string]any, keys ...string) any {
	var result any = value
	for _, key := range keys {
		result = result.(map[string]any)[key]
	}
	return result
}

func find(t *testing.T, objects []map[string]any, kind string) map[string]any {
	t.Helper()
	for _, object := range objects {
		if object["kind"] == kind {
			return object
		}
	}
	t.Fatalf("missing %s", kind)
	return nil
}

func TestSecureSingleWriterChart(t *testing.T) {
	objects := render(t)
	state := find(t, objects, "StatefulSet")
	if at(state, "spec", "replicas") != 1 || at(state, "spec", "persistentVolumeClaimRetentionPolicy", "whenDeleted") != "Retain" {
		t.Fatal("single writer or claim retention missing")
	}
	pod := at(state, "spec", "template", "spec").(map[string]any)
	if pod["automountServiceAccountToken"] != false || at(pod, "securityContext", "runAsUser") != 65532 || at(pod, "securityContext", "runAsNonRoot") != true {
		t.Fatal("pod security context is unsafe")
	}
	containers := append(pod["initContainers"].([]any), pod["containers"].([]any)...)
	for _, item := range containers {
		container := item.(map[string]any)
		if !strings.Contains(container["image"].(string), "@sha256:") || at(container, "securityContext", "readOnlyRootFilesystem") != true || at(container, "securityContext", "allowPrivilegeEscalation") != false {
			t.Fatal("unsafe container security or image")
		}
	}
	server := pod["containers"].([]any)[0].(map[string]any)
	for _, item := range server["env"].([]any) {
		env := item.(map[string]any)
		if env["name"] == "MOCO_ADMIN_ADDR" && env["value"] != "127.0.0.1:9443" {
			t.Fatal("default admin listener must stay on loopback")
		}
	}
	backup := pod["containers"].([]any)[1].(map[string]any)
	if !strings.Contains(strings.Join(toStrings(backup["args"].([]any)), " "), "--wait-ready") || at(backup, "env") == nil {
		t.Fatal("backup must wait for SQLite to become available")
	}
	for _, item := range backup["volumeMounts"].([]any) {
		mount := item.(map[string]any)
		if mount["name"] == "data" && mount["readOnly"] != true {
			t.Fatal("backup data must be read-only")
		}
	}
	policy := find(t, objects, "NetworkPolicy")
	if len(at(policy, "spec", "egress").([]any)) != 0 || len(at(policy, "spec", "ingress").([]any)) != 1 {
		t.Fatal("default policy must isolate admin and deny egress")
	}
	for _, object := range objects {
		if object["kind"] == "Secret" || object["kind"] == "HorizontalPodAutoscaler" || object["kind"] == "Deployment" {
			t.Fatal("chart must not create credentials or a scalable writer")
		}
	}
}

func toStrings(items []any) []string {
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.(string)
	}
	return result
}

func TestExistingClaimsIngressAndMonitoring(t *testing.T) {
	objects := render(t, "--set", "persistence.data.existingClaim=data-existing,persistence.backups.existingClaim=backups-existing,ingress.enabled=true,ingress.className=private,ingress.hostname=moco.example.com,ingress.tlsSecret=frontend-tls,monitoring.enabled=true,monitoring.existingSecret=metrics-only,monitoring.namespaceSelector.matchLabels.monitoring=enabled,monitoring.podSelector.matchLabels.app=prometheus", "--api-versions", "monitoring.coreos.com/v1/ServiceMonitor")
	state := find(t, objects, "StatefulSet")
	if at(state, "spec").(map[string]any)["volumeClaimTemplates"] != nil {
		t.Fatal("existing claims must not create new claims")
	}
	ingress := find(t, objects, "Ingress")
	rule := at(ingress, "spec", "rules").([]any)[0].(map[string]any)
	path := at(rule, "http", "paths").([]any)[0].(map[string]any)
	if at(path, "backend", "service", "port", "name") != "https" {
		t.Fatal("Ingress must route only to public HTTPS")
	}
	monitor := find(t, objects, "ServiceMonitor")
	endpoint := at(monitor, "spec", "endpoints").([]any)[0].(map[string]any)
	if at(endpoint, "authorization", "credentials", "name") != "metrics-only" || at(endpoint, "tlsConfig", "ca", "secret", "name") != "metrics-only" || endpoint["scheme"] != "https" {
		t.Fatal("monitor must use TLS and a separate token/CA Secret")
	}
	policy := find(t, objects, "NetworkPolicy")
	if len(at(policy, "spec", "ingress").([]any)) != 2 {
		t.Fatal("metrics listener needs an explicit protected ingress rule")
	}
}

func TestSchemaAndSafetyGuardsRejectInvalidValues(t *testing.T) {
	for _, settings := range []string{
		"replicaCount=2", "replicaCount=0", "unknownKey=true", "image.digest=latest", "credentials.existingSecret=",
		"backups.keep=1", "backups.mirrorID=00000000-0000-0000-0000-000000000000", "backups.timeoutSeconds=900",
		"backups.maxAgeSeconds=1500", "persistence.data.accessMode=ReadWriteMany", "persistence.data.size=0Gi",
		"persistence.data.existingClaim=moco-offhost-mirror", "resources.server.limits.memory=0Mi",
		"persistence.backups.existingClaim=data-moco-test-0", "backups.mirrorClaim=backups-moco-test-0",
		"ingress.enabled=true", "monitoring.enabled=true", "monitoring.enabled=true,networkPolicy.enabled=false",
		"monitoring.enabled=true,monitoring.existingSecret=metrics-only,monitoring.namespaceSelector.matchLabels.monitoring=enabled,monitoring.podSelector.matchLabels.app=prometheus",
	} {
		t.Run(settings, func(t *testing.T) {
			data, err := helm(t, "template", "test", "charts/moco", "-f", "charts/moco/ci/test-values.yaml", "--kube-version", "1.32.0", "--set", settings)
			if err == nil {
				t.Fatalf("unsafe values accepted: %s\n%s", settings, data)
			}
		})
	}
	if data, err := helm(t, "template", "test", "charts/moco"); err == nil {
		t.Fatalf("missing required production inputs accepted: %s", data)
	}
}

func TestLintAndPackage(t *testing.T) {
	if data, err := helm(t, "lint", "--strict", "charts/moco", "-f", "charts/moco/ci/test-values.yaml"); err != nil {
		t.Fatalf("lint: %v\n%s", err, data)
	}
	if data, err := helm(t, "package", "charts/moco", "--destination", t.TempDir()); err != nil {
		t.Fatalf("package: %v\n%s", err, data)
	}
}

func TestAdminIsolationSurvivesSkippedSchemaValidation(t *testing.T) {
	for _, settings := range []string{
		"monitoring.enabled=true,monitoring.existingSecret=metrics-only",
		"monitoring.enabled=true,monitoring.existingSecret=moco-credentials,monitoring.namespaceSelector.matchLabels.monitoring=enabled,monitoring.podSelector.matchLabels.app=prometheus",
	} {
		data, err := helm(t, "template", "test", "charts/moco", "-f", "charts/moco/ci/test-values.yaml", "--kube-version", "1.32.0", "--skip-schema-validation", "--api-versions", "monitoring.coreos.com/v1/ServiceMonitor", "--set", settings)
		if err == nil {
			t.Fatalf("unsafe admin listener accepted: %s\n%s", settings, data)
		}
	}
}
