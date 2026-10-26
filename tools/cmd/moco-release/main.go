// Command moco-release validates release identity and publishes versioned assets.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoPattern    = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)
	corePattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+($|[-+])`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	missingPattern = regexp.MustCompile(`(?i)(manifest_unknown|name_unknown|manifest unknown|manifest[^\n]*not found|ghcr\.io/[^\n]*: not found|404 not found)`)
)

type plan struct {
	Tag             string `json:"tag"`
	Version         string `json:"version"`
	OCITag          string `json:"ociTag"`
	Commit          string `json:"commit"`
	Repository      string `json:"repository"`
	ImageRepository string `json:"imageRepository"`
	ChartRegistry   string `json:"chartRegistry"`
}

func releasePlan(tag, commit, repository string, prerelease bool) (plan, error) {
	version := strings.TrimPrefix(tag, "v")
	if !corePattern.MatchString(version) || !semver.IsValid("v"+version) {
		return plan{}, errors.New("release tag must be full SemVer, optionally prefixed with v")
	}
	if (semver.Prerelease("v"+version) != "") != prerelease {
		return plan{}, errors.New("GitHub prerelease status must match the SemVer prerelease suffix")
	}
	ociTag := strings.ReplaceAll(version, "+", "_")
	if len(ociTag) > 128 || !commitPattern.MatchString(commit) || !repoPattern.MatchString(repository) {
		return plan{}, errors.New("invalid release commit/repository or OCI tag longer than 128 characters")
	}
	lower := strings.ToLower(repository)
	return plan{tag, version, ociTag, commit, repository, "ghcr.io/" + lower, "oci://ghcr.io/" + strings.Split(lower, "/")[0] + "/charts"}, nil
}

type runner func(context.Context, string, ...string) ([]byte, error)

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- fixed release tools and separately validated refs; never a shell.
}

func verifyGit(ctx context.Context, run runner, p plan) error {
	for _, ref := range []string{"HEAD", "refs/tags/" + p.Tag + "^{commit}"} {
		output, err := run(ctx, "git", "rev-parse", "--verify", ref)
		if err != nil || strings.TrimSpace(string(output)) != p.Commit {
			return errors.New("release event, checkout and tag must resolve to the same commit")
		}
	}
	status, err := run(ctx, "git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil || len(bytes.TrimSpace(status)) != 0 {
		return errors.New("release source checkout must be clean")
	}
	if _, err := run(ctx, "git", "merge-base", "--is-ancestor", p.Commit, "refs/remotes/origin/main"); err != nil {
		return errors.New("release commit must be reachable from origin/main")
	}
	return nil
}

type descriptor struct {
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
	Manifests   []struct {
		Platform struct {
			OS, Architecture string
		} `json:"platform"`
	} `json:"manifests"`
}

func inspect(ctx context.Context, run runner, ref string) (descriptor, bool, error) {
	data, err := run(ctx, "docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", ref)
	if err != nil {
		if missingPattern.Match(data) {
			return descriptor{}, false, nil
		}
		return descriptor{}, false, fmt.Errorf("registry inspection failed: %s: %w", bytes.TrimSpace(data), err)
	}
	var result descriptor
	if err := json.Unmarshal(data, &result); err != nil || !digestPattern.MatchString(result.Digest) {
		return descriptor{}, false, errors.New("registry returned an invalid OCI descriptor")
	}
	return result, true, nil
}

func imageStatus(ctx context.Context, run runner, p plan) (descriptor, bool, error) {
	result, exists, err := inspect(ctx, run, p.ImageRepository+":"+p.OCITag)
	if err != nil || !exists {
		return result, exists, err
	}
	for key, expected := range map[string]string{"org.opencontainers.image.revision": p.Commit, "org.opencontainers.image.version": p.Version, "org.opencontainers.image.source": "https://github.com/" + p.Repository} {
		if result.Annotations[key] != expected {
			return descriptor{}, false, errors.New("existing image belongs to a different release; refusing to replace it")
		}
	}
	platforms := map[string]bool{}
	for _, manifest := range result.Manifests {
		platforms[manifest.Platform.OS+"/"+manifest.Platform.Architecture] = true
	}
	if !platforms["linux/amd64"] || !platforms["linux/arm64"] {
		return descriptor{}, false, errors.New("existing release image must contain linux/amd64 and linux/arm64")
	}
	return result, true, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Open named local workspace files through their parent root. Release flags
// select these paths; package metadata never chooses filesystem destinations.
func openLocal(path string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(filepath.Base(path))
}

func readLocal(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(path))
}

func outputs(path string, values map[string]string) error {
	if path == "" {
		return nil
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	for key, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid multiline release output")
		}
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return err
		}
	}
	return nil
}

func loadPlan(path string) (plan, error) {
	data, err := readLocal(path)
	if err != nil {
		return plan{}, err
	}
	var p plan
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	expected, err := releasePlan(p.Tag, p.Commit, p.Repository, semver.Prerelease("v"+p.Version) != "")
	if err != nil || p != expected {
		return p, errors.New("release plan is invalid or inconsistent")
	}
	return p, nil
}

func yamlMap(path string) (map[string]any, error) {
	data, err := readLocal(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	err = yaml.Unmarshal(data, &value)
	return value, err
}

func packageChart(ctx context.Context, run runner, p plan, digest, source, out string) (string, error) {
	stageRoot, err := os.MkdirTemp(out, ".chart-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()
	stage := filepath.Join(stageRoot, "moco")
	if err := os.CopyFS(stage, os.DirFS(source)); err != nil {
		return "", err
	}
	chart, err := yamlMap(filepath.Join(stage, "Chart.yaml"))
	if err != nil {
		return "", err
	}
	if chart["name"] != "moco" {
		return "", errors.New("release chart must be named moco")
	}
	chart["version"], chart["appVersion"] = p.Version, p.Version
	annotations, _ := chart["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations["org.opencontainers.image.source"] = "https://github.com/" + p.Repository
	annotations["org.opencontainers.image.revision"] = p.Commit
	chart["annotations"] = annotations
	// JSON is a YAML subset and keeps version/digest values unambiguously strings.
	if err := writeJSON(filepath.Join(stage, "Chart.yaml"), chart); err != nil {
		return "", err
	}
	values, err := yamlMap(filepath.Join(stage, "values.yaml"))
	if err != nil {
		return "", err
	}
	image, ok := values["image"].(map[string]any)
	if !ok {
		return "", errors.New("source chart is missing image settings")
	}
	image["repository"], image["digest"] = p.ImageRepository, digest
	if err := writeJSON(filepath.Join(stage, "values.yaml"), values); err != nil {
		return "", err
	}
	fixture := filepath.Join(out, ".validation-values.json")
	defer func() { _ = os.Remove(fixture) }()
	if err := writeJSON(fixture, map[string]any{"credentials": map[string]any{"existingSecret": "release-validation"}, "backups": map[string]any{"mirrorClaim": "release-mirror-validation", "mirrorID": "11111111-1111-4111-8111-111111111111"}}); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"lint", "--strict", stage, "-f", fixture}, {"template", "release-validation", stage, "-f", fixture, "--kube-version", "1.32.0"}, {"package", stage, "--destination", out}} {
		if output, err := run(ctx, "helm", args...); err != nil {
			return "", fmt.Errorf("release chart validation/packaging failed: %s: %w", output, err)
		}
	}
	return filepath.Join(out, "moco-"+p.Version+".tgz"), nil
}

func verifyChart(path string, p plan, digest string) error {
	file, err := openLocal(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = compressed.Close() }()
	archive := tar.NewReader(io.LimitReader(compressed, 32<<20))
	documents := map[string][]byte{}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Size < 0 || header.Size > 8<<20 || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) {
			return errors.New("release chart contains an unsafe or oversized archive entry")
		}
		if header.Name == "moco/Chart.yaml" || header.Name == "moco/values.yaml" || header.Name == "moco/values.schema.json" {
			if _, duplicate := documents[header.Name]; duplicate {
				return errors.New("duplicate release chart metadata")
			}
			documents[header.Name], err = io.ReadAll(archive)
			if err != nil {
				return err
			}
		}
	}
	// YAML field names are explicit because appVersion is case sensitive.
	var metadata map[string]any
	var values struct {
		Image struct{ Repository, Digest string }
	}
	if yaml.Unmarshal(documents["moco/Chart.yaml"], &metadata) != nil || yaml.Unmarshal(documents["moco/values.yaml"], &values) != nil {
		return errors.New("invalid packaged chart metadata")
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if metadata["name"] != "moco" || metadata["version"] != p.Version || metadata["appVersion"] != p.Version || annotations["org.opencontainers.image.revision"] != p.Commit || annotations["org.opencontainers.image.source"] != "https://github.com/"+p.Repository || values.Image.Repository != p.ImageRepository || values.Image.Digest != digest || !json.Valid(documents["moco/values.schema.json"]) {
		return errors.New("existing chart/version/image does not match this release; refusing to replace it")
	}
	return nil
}

func publishChart(ctx context.Context, run runner, p plan, digest, source, out string) (string, descriptor, error) {
	if !digestPattern.MatchString(digest) {
		return "", descriptor{}, errors.New("release image digest is invalid")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", descriptor{}, err
	}
	ref := strings.TrimPrefix(p.ChartRegistry, "oci://") + "/moco:" + p.OCITag
	remote, exists, err := inspect(ctx, run, ref)
	if err != nil {
		return "", remote, err
	}
	var path string
	if exists {
		output, err := run(ctx, "helm", "pull", p.ChartRegistry+"/moco", "--version", p.Version, "--destination", out)
		if err != nil {
			return "", remote, fmt.Errorf("pull existing chart: %s: %w", output, err)
		}
		path = filepath.Join(out, "moco-"+p.Version+".tgz")
	} else {
		path, err = packageChart(ctx, run, p, digest, source, out)
		if err != nil {
			return "", remote, err
		}
	}
	if err := verifyChart(path, p, digest); err != nil {
		return "", remote, err
	}
	if !exists {
		if output, err := run(ctx, "helm", "push", path, p.ChartRegistry); err != nil {
			return "", remote, fmt.Errorf("push chart: %s: %w", output, err)
		}
		var found bool
		remote, found, err = inspect(ctx, run, ref)
		if err != nil || !found {
			return "", remote, errors.New("published chart descriptor could not be verified")
		}
	}
	return path, remote, nil
}

func checksum(path string) (string, error) {
	file, err := openLocal(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func attachAssets(ctx context.Context, run runner, p plan, out, chartPath, imageDigest, chartDigest string) error {
	chartSum, err := checksum(chartPath)
	if err != nil {
		return err
	}
	metadataPath := filepath.Join(out, "release.json")
	metadata := struct {
		plan
		ImageDigest string `json:"imageDigest"`
		ChartDigest string `json:"chartDigest"`
		ChartSHA256 string `json:"chartSHA256"`
	}{p, imageDigest, chartDigest, chartSum}
	if err := writeJSON(metadataPath, metadata); err != nil {
		return err
	}
	metadataSum, err := checksum(metadataPath)
	if err != nil {
		return err
	}
	sums := chartSum + "  " + filepath.Base(chartPath) + "\n" + metadataSum + "  release.json\n"
	sumsPath := filepath.Join(out, "SHA256SUMS")
	if err := os.WriteFile(sumsPath, []byte(sums), 0o600); err != nil {
		return err
	}
	data, err := run(ctx, "gh", "release", "view", p.Tag, "--repo", p.Repository, "--json", "assets")
	if err != nil {
		return fmt.Errorf("read release assets: %w", err)
	}
	var release struct{ Assets []struct{ Name string } }
	if err := json.Unmarshal(data, &release); err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, asset := range release.Assets {
		existing[asset.Name] = true
	}
	for _, path := range []string{chartPath, metadataPath, sumsPath} {
		if existing[filepath.Base(path)] {
			stage, err := os.MkdirTemp(out, ".asset-*")
			if err != nil {
				return err
			}
			output, downloadErr := run(ctx, "gh", "release", "download", p.Tag, "--repo", p.Repository, "--pattern", filepath.Base(path), "--dir", stage)
			actual, sumErr := checksum(filepath.Join(stage, filepath.Base(path)))
			_ = os.RemoveAll(stage)
			expected, err := checksum(path)
			if downloadErr != nil || sumErr != nil || err != nil || actual != expected {
				return fmt.Errorf("existing release asset differs or cannot be read: %s: %s", filepath.Base(path), output)
			}
			continue
		}
		if output, err := run(ctx, "gh", "release", "upload", p.Tag, path, "--repo", p.Repository); err != nil {
			return fmt.Errorf("upload release asset: %s: %w", output, err)
		}
	}
	return nil
}

func run(ctx context.Context, args []string, execute runner) error {
	if len(args) == 0 {
		return errors.New("usage: moco-release <plan|image-status|publish-chart> [flags]")
	}
	flags := flag.NewFlagSet("moco-release", flag.ContinueOnError)
	tag := flags.String("tag", "", "GitHub release tag")
	commit := flags.String("commit", "", "release event commit")
	repository := flags.String("repository", "", "GitHub owner/repository")
	prerelease := flags.Bool("prerelease", false, "GitHub release prerelease status")
	planPath := flags.String("plan", "release-plan.json", "validated plan JSON")
	output := flags.String("output", "", "GITHUB_OUTPUT file")
	imageDigest := flags.String("image-digest", "", "published multi-platform image digest")
	source := flags.String("chart", "charts/moco", "source chart")
	out := flags.String("out", "dist/release", "release staging directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("release helper accepts no positional arguments")
	}
	if args[0] == "plan" {
		p, err := releasePlan(*tag, *commit, *repository, *prerelease)
		if err != nil {
			return err
		}
		if err := verifyGit(ctx, execute, p); err != nil {
			return err
		}
		if err := writeJSON(*planPath, p); err != nil {
			return err
		}
		return outputs(*output, map[string]string{"version": p.Version, "oci_tag": p.OCITag, "commit": p.Commit, "image_repository": p.ImageRepository})
	}
	p, err := loadPlan(*planPath)
	if err != nil {
		return err
	}
	if err := verifyGit(ctx, execute, p); err != nil {
		return err
	}
	switch args[0] {
	case "image-status":
		image, exists, err := imageStatus(ctx, execute, p)
		if err != nil {
			return err
		}
		return outputs(*output, map[string]string{"exists": fmt.Sprint(exists), "digest": image.Digest})
	case "publish-chart":
		image, exists, err := imageStatus(ctx, execute, p)
		if err != nil || !exists || image.Digest != *imageDigest {
			return errors.New("published image is absent or differs from the validated digest")
		}
		path, chart, err := publishChart(ctx, execute, p, *imageDigest, *source, *out)
		if err != nil {
			return err
		}
		return attachAssets(ctx, execute, p, *out, path, *imageDigest, chart.Digest)
	default:
		return errors.New("unknown release operation")
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], command); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "moco-release:", err)
		os.Exit(1)
	}
}
