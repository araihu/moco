package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"
const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixturePlan(t *testing.T) plan {
	t.Helper()
	p, err := releasePlan("v1.2.3", testCommit, "araihu/moco", false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSemVerAndOCIIdentity(t *testing.T) {
	for _, tag := range []string{"1.2.3", "v1.2.3", "v0.0.0", "v1.2.3-rc.1", "v1.2.3+build.007", "v1.2.3-rc.1+build.007"} {
		version := strings.TrimPrefix(tag, "v")
		pre := strings.Contains(strings.Split(version, "+")[0], "-")
		p, err := releasePlan(tag, testCommit, "Araihu/Moco", pre)
		if err != nil || p.Version != version || p.OCITag != strings.ReplaceAll(version, "+", "_") || p.ImageRepository != "ghcr.io/araihu/moco" || p.ControllerImageRepository != "ghcr.io/araihu/moco-controller" || p.ChartRegistry != "oci://ghcr.io/araihu/charts" {
			t.Fatalf("tag %s: %+v %v", tag, p, err)
		}
	}
	for _, tag := range []string{"latest", "v1", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+", "v1.2.3_foo", "v1.2.3\nprivate", "$(id)", "v1.2.3+" + strings.Repeat("a", 130)} {
		pre := strings.Contains(strings.Split(strings.TrimPrefix(tag, "v"), "+")[0], "-")
		if _, err := releasePlan(tag, testCommit, "araihu/moco", pre); err == nil {
			t.Fatalf("invalid release accepted: %q", tag)
		}
	}
	for _, tc := range []struct {
		tag, commit, repo string
		pre               bool
	}{
		{"v1.2.3-rc.1", testCommit, "araihu/moco", false}, {"v1.2.3", testCommit, "araihu/moco", true},
		{"v1.2.3", "HEAD", "araihu/moco", false}, {"v1.2.3", testCommit, "araihu/moco/other", false},
	} {
		if _, err := releasePlan(tc.tag, tc.commit, tc.repo, tc.pre); err == nil {
			t.Fatalf("invalid identity accepted: %+v", tc)
		}
	}
}

func TestGitIdentityRequiresTagCheckoutAndMainAncestor(t *testing.T) {
	directory := t.TempDir()
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed local git commands against a test-owned repository; no shell.
		cmd.Dir = directory
		return cmd.CombinedOutput()
	}
	git := func(args ...string) string {
		t.Helper()
		output, err := run(t.Context(), "git", append([]string{"-c", "user.name=Release Test", "-c", "user.email=release@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--initial-branch=main")
	git("commit", "--allow-empty", "-m", "initial")
	initial := git("rev-parse", "HEAD")
	git("update-ref", "refs/remotes/origin/main", initial)
	git("tag", "-a", "v1.2.3", "-m", "annotated release")
	p, err := releasePlan("v1.2.3", initial, "araihu/moco", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyGit(t.Context(), run, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "untracked.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyGit(t.Context(), run, p); err == nil {
		t.Fatal("dirty source checkout accepted")
	}
	if err := os.Remove(filepath.Join(directory, "untracked.go")); err != nil {
		t.Fatal(err)
	}
	git("checkout", "-b", "not-on-main")
	git("commit", "--allow-empty", "-m", "unmerged")
	other := git("rev-parse", "HEAD")
	git("tag", "v2.0.0")
	if err := verifyGit(t.Context(), run, p); err == nil {
		t.Fatal("mismatched checkout accepted")
	}
	p.Commit = other
	if err := verifyGit(t.Context(), run, p); err == nil {
		t.Fatal("moved tag identity accepted")
	}
	p.Tag, p.Version = "v2.0.0", "2.0.0"
	if err := verifyGit(t.Context(), run, p); err == nil {
		t.Fatal("unmerged release accepted")
	}
}

func imageFixture(p plan) descriptor {
	d := descriptor{Digest: testDigest, Annotations: map[string]string{"org.opencontainers.image.revision": p.Commit, "org.opencontainers.image.version": p.Version, "org.opencontainers.image.source": "https://github.com/" + p.Repository}}
	_ = json.Unmarshal([]byte(`[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]`), &d.Manifests)
	return d
}

func TestExistingImagesAreValidatedAndFailuresDoNotBecomeMissing(t *testing.T) {
	p := fixturePlan(t)
	for _, failure := range []string{"", "revision", "platform"} {
		d := imageFixture(p)
		if failure == "revision" {
			d.Annotations["org.opencontainers.image.revision"] = strings.Repeat("f", 40)
		}
		if failure == "platform" {
			d.Manifests = d.Manifests[:1]
		}
		run := func(context.Context, string, ...string) ([]byte, error) { return json.Marshal(d) }
		_, exists, err := imageStatus(t.Context(), run, p, server(p))
		if failure == "" && (err != nil || !exists) {
			t.Fatal("existing image not reusable", err)
		}
		if failure != "" && err == nil {
			t.Fatal("conflicting image accepted", failure)
		}
	}
	for _, tc := range []struct {
		message string
		missing bool
	}{
		{"ERROR: ghcr.io/araihu/moco:1.2.3: not found", true}, {"MANIFEST_UNKNOWN: requested manifest unknown", true},
		{"unexpected status: 404 Not Found", true}, {"unauthorized: authentication required", false},
		{"denied: requested access to the resource is denied", false}, {"exec: docker: executable file not found in $PATH", false}, {"TLS handshake timeout", false},
	} {
		run := func(context.Context, string, ...string) ([]byte, error) {
			return []byte(tc.message), errors.New("registry failure")
		}
		_, exists, err := imageStatus(t.Context(), run, p, server(p))
		if exists || (err == nil) != tc.missing {
			t.Fatalf("registry failure %q misclassified: %v", tc.message, err)
		}
	}
}

func chartFixture(t *testing.T, path string, p plan, c component, digest string, schema, includeCRDs bool) {
	t.Helper()
	file, err := os.Create(path) // #nosec G304 -- test-owned temporary archive fixture.
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	metadata, err := json.Marshal(map[string]any{"name": c.Name, "version": p.Version, "appVersion": p.Version, "annotations": map[string]string{"org.opencontainers.image.revision": p.Commit, "org.opencontainers.image.source": "https://github.com/" + p.Repository}})
	if err != nil {
		t.Fatal(err)
	}
	values, err := json.Marshal(map[string]any{"image": map[string]string{"repository": c.ImageRepository, "digest": digest}})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{c.Name + "/Chart.yaml": metadata, c.Name + "/values.yaml": values}
	if schema {
		entries[c.Name+"/values.schema.json"] = []byte(`{"type":"object"}`)
	}
	if c.Name == "moco-controller" && includeCRDs {
		for _, kind := range []string{"mocotenants", "mocovaults", "mocoauthorizations"} {
			entries[c.Name+"/crds/moco.araihu.io_"+kind+".yaml"] = []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: " + kind + ".moco.araihu.io\n")
		}
	}

	for name, data := range entries {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(archive.Close(), compressed.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestChartMustCarryMatchingSourceVersionDigestAndSchema(t *testing.T) {
	p := fixturePlan(t)
	for _, failure := range []string{"", "version", "commit", "digest", "schema"} {
		actual := p
		digest := testDigest
		switch failure {
		case "version":
			actual.Version = "9.0.0"
		case "commit":
			actual.Commit = strings.Repeat("f", 40)
		case "digest":
			digest = "sha256:" + strings.Repeat("f", 64)
		}
		path := filepath.Join(t.TempDir(), "moco.tgz")
		chartFixture(t, path, actual, server(actual), digest, failure != "schema", true)
		err := verifyChart(path, p, server(p), testDigest)
		if (err == nil) != (failure == "") {
			t.Fatalf("chart mismatch %s: %v", failure, err)
		}
	}
}

func TestResumeReusesChartAndDoesNotRepublish(t *testing.T) {
	p := fixturePlan(t)
	out := t.TempDir()
	called := []string{}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		called = append(called, name+" "+strings.Join(args, " "))
		if name == "docker" {
			return json.Marshal(descriptor{Digest: testDigest})
		}
		if name == "helm" && args[0] == "pull" {
			chartFixture(t, filepath.Join(out, "moco-"+p.Version+".tgz"), p, server(p), testDigest, true, true)
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}
	path, _, err := publishChart(t.Context(), run, p, server(p), testDigest, "does-not-exist", out)
	if err != nil || path == "" {
		t.Fatal("resume failed", err)
	}
	if len(called) != 2 || !strings.HasPrefix(called[1], "helm pull") {
		t.Fatal("existing chart must only be pulled and verified", called)
	}
}

func TestReleaseAssetsAreIdempotentAndNeverClobbered(t *testing.T) {
	p := fixturePlan(t)
	out := t.TempDir()
	chartPath := filepath.Join(out, "moco-"+p.Version+".tgz")
	chartFixture(t, chartPath, p, server(p), testDigest, true, true)
	controllerPath := filepath.Join(out, "moco-controller-"+p.Version+".tgz")
	chartFixture(t, controllerPath, p, controller(p), testDigest, true, true)
	artifacts := []publishedComponent{{Component: server(p), Path: chartPath, ImageDigest: testDigest, ChartDigest: testDigest}, {Component: controller(p), Path: controllerPath, ImageDigest: testDigest, ChartDigest: testDigest}}

	assets := map[string][]byte{}
	uploads := 0
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, errors.New("unexpected command")
		}
		switch args[1] {
		case "view":
			var list []map[string]string
			for name := range assets {
				list = append(list, map[string]string{"name": name})
			}
			return json.Marshal(map[string]any{"assets": list})
		case "upload":
			path := args[3]
			data, err := readLocal(path)
			if err != nil {
				return nil, err
			}
			assets[filepath.Base(path)] = data
			uploads++
			return nil, nil
		case "download":
			return nil, os.WriteFile(filepath.Join(args[8], args[6]), assets[args[6]], 0o600)
		}
		return nil, errors.New("unexpected release operation")
	}
	for range 2 {
		if err := attachAssets(t.Context(), run, p, out, artifacts); err != nil {
			t.Fatal(err)
		}
	}
	if uploads != 4 {
		t.Fatalf("assets uploaded %d times; expected four", uploads)
	}
	assets["SHA256SUMS"] = []byte("conflicting asset")
	if err := attachAssets(t.Context(), run, p, out, artifacts); err == nil {
		t.Fatal("conflicting assets were silently replaced")
	}
	if uploads != 4 {
		t.Fatal("existing asset overwritten")
	}
}

func TestPublicationUploadsToDraftBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		tag          string
		failUploadAt int
	}{{"v1.2.3", 0}, {"v1.2.3-rc.1", 0}, {"v1.2.3", 1}, {"v1.2.3", 2}, {"v1.2.3", 3}, {"v1.2.3", 4}} {
		t.Run(tc.tag, func(t *testing.T) {
			tag := tc.tag
			pre := strings.Contains(tag, "-rc")
			p, err := releasePlan(tag, testCommit, "araihu/moco", pre)
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			planPath := filepath.Join(out, "plan.json")
			if err := writeJSON(planPath, p); err != nil {
				t.Fatal(err)
			}
			created, published, uploads := false, false, 0
			execute := func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch name {
				case "git":
					if args[0] == "rev-parse" {
						return []byte(p.Commit), nil
					}
					return nil, nil
				case "docker":
					return json.Marshal(imageFixture(p))
				case "helm":
					if args[0] != "pull" || !created {
						t.Fatal("chart must be pulled after release preparation", args)
					}
					c := server(p)
					if strings.HasSuffix(args[1], "/moco-controller") {
						c = controller(p)
					}
					chartFixture(t, filepath.Join(out, c.Name+"-"+p.Version+".tgz"), p, c, testDigest, true, true)
					return nil, nil
				case "gh":
					if args[0] == "api" {
						return []byte("gh: Not Found (HTTP 404)"), errors.New("not found")
					}
					switch args[1] {
					case "create":
						joined := " " + strings.Join(args, " ") + " "
						if !strings.Contains(joined, " --draft ") || strings.Contains(joined, " --prerelease ") != pre {
							t.Fatal("release must start as a matching draft", args)
						}
						created = true
					case "view":
						return []byte(`{"assets":[]}`), nil
					case "upload":
						if !created || published {
							t.Fatal("assets must be uploaded while draft")
						}
						uploads++
						if uploads == tc.failUploadAt {
							return nil, errors.New("upload interrupted")
						}
					case "edit":
						if uploads != 4 || !strings.Contains(strings.Join(args, " "), "--draft=false --latest="+map[bool]string{true: "false", false: "true"}[pre]) {
							t.Fatal("publish only after all uploads, with matching latest policy", args)
						}
						published = true
					}
					return nil, nil
				}
				return nil, errors.New("unexpected tool")
			}
			err = run(t.Context(), []string{"publish-release", "--plan", planPath, "--image-digest", testDigest, "--controller-image-digest", testDigest, "--out", out}, execute)
			if tc.failUploadAt > 0 {
				if err == nil || published || !created {
					t.Fatal("failed uploads must leave the release as a draft", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !published {
				t.Fatal("completed release was not published")
			}
		})
	}
}

func TestTagPushDerivesPrereleaseStatusFromSemVer(t *testing.T) {
	for _, tc := range []struct {
		tag     string
		flags   []string
		failure bool
	}{
		{"v1.2.3", nil, false},
		{"v1.2.3-rc.1", nil, false},
		{"v1.2.3+build-with-hyphens", nil, false},
		{"v1.2.3-rc.1", []string{"--prerelease=false"}, true},
	} {
		execute := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "git" {
				t.Fatal("unexpected tool", name)
			}
			if args[0] == "rev-parse" {
				return []byte(testCommit), nil
			}
			return nil, nil
		}
		args := []string{"plan", "--tag", tc.tag, "--commit", testCommit, "--repository", "araihu/moco", "--plan", filepath.Join(t.TempDir(), "plan.json")}
		if err := run(t.Context(), append(args, tc.flags...), execute); (err != nil) != tc.failure {
			t.Fatalf("tag %s: %v", tc.tag, err)
		}
	}
}

func TestReleasePreparationFailsClosedAndSupportsRetries(t *testing.T) {
	p := fixturePlan(t)
	for _, tc := range []struct {
		response string
		failure  bool
		draft    bool
	}{
		{`{"tag_name":"v1.2.3","draft":true}`, false, true},
		{`{"tag_name":"v1.2.3","assets":[{"name":"moco-1.2.3.tgz"},{"name":"moco-controller-1.2.3.tgz"},{"name":"release.json"},{"name":"SHA256SUMS"}]}`, false, false},
		{`{"tag_name":"v1.2.3","immutable":true,"assets":[]}`, true, false},
		{`{"tag_name":"v1.2.3","assets":[{"name":"moco-1.2.3.tgz"},{"name":"release.json"},{"name":"SHA256SUMS"}]}`, true, false},
		{`{"tag_name":"v1.2.3","draft":true,"prerelease":true}`, true, false},
		{`{"tag_name":"v9.9.9","draft":true}`, true, false},
		{`invalid JSON`, true, false},
	} {
		execute := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "gh" || args[0] != "api" {
				t.Fatal("existing release must not be recreated")
			}
			return []byte(tc.response), nil
		}
		draft, err := prepareRelease(t.Context(), execute, p)
		if (err != nil) != tc.failure || draft != tc.draft {
			t.Fatalf("release %s: draft=%v err=%v", tc.response, draft, err)
		}
	}
	for _, message := range []string{"gh: Forbidden (HTTP 403)", "TLS handshake timeout"} {
		execute := func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] != "api" {
				t.Fatal("authentication and network failures must not create releases")
			}
			return []byte(message), errors.New("request failed")
		}
		if _, err := prepareRelease(t.Context(), execute, p); err == nil {
			t.Fatal("GitHub failure treated as missing release")
		}
	}
}
