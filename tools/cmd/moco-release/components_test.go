package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const controllerDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestBothComponentImagesMustMatchBeforeAnyChartPublication(t *testing.T) {
	for _, failure := range []string{"", "missing-controller", "wrong-controller-source", "wrong-controller-digest"} {
		t.Run(failure, func(t *testing.T) {
			p := fixturePlan(t)
			out := t.TempDir()
			planPath := filepath.Join(out, "plan.json")
			if err := writeJSON(planPath, p); err != nil {
				t.Fatal(err)
			}
			charts, uploads, published := 0, 0, false
			assets := map[string][]byte{}
			execute := func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch name {
				case "git":
					if args[0] == "rev-parse" {
						return []byte(p.Commit), nil
					}
					return nil, nil
				case "docker":
					ref := args[len(args)-1]
					image := imageFixture(p)
					if strings.HasPrefix(ref, p.ControllerImageRepository+":") {
						image.Digest = controllerDigest
						switch failure {
						case "missing-controller":
							return []byte("MANIFEST_UNKNOWN"), errors.New("missing")
						case "wrong-controller-source":
							image.Annotations["org.opencontainers.image.revision"] = strings.Repeat("f", 40)
						case "wrong-controller-digest":
							image.Digest = testDigest
						}
					} else if ref != p.ImageRepository+":"+p.OCITag && !strings.HasPrefix(ref, strings.TrimPrefix(p.ChartRegistry, "oci://")+"/") {
						t.Fatalf("unexpected registry reference: %s", ref)
					}
					return json.Marshal(image)
				case "helm":
					if args[0] != "pull" {
						t.Fatal("existing chart should be reused", args)
					}
					charts++
					c, digest := server(p), testDigest
					if args[1] == p.ChartRegistry+"/moco-controller" {
						c, digest = controller(p), controllerDigest
					}
					chartFixture(t, filepath.Join(out, c.Name+"-"+p.Version+".tgz"), p, c, digest, true, true)
					return nil, nil
				case "gh":
					if args[0] == "api" {
						return []byte("gh: Not Found (HTTP 404)"), errors.New("missing")
					}
					switch args[1] {
					case "create":
						return nil, nil
					case "view":
						return []byte(`{"assets":[]}`), nil
					case "upload":
						data, err := readLocal(args[3])
						if err != nil {
							return nil, err
						}
						assets[filepath.Base(args[3])] = data
						uploads++
						return nil, nil
					case "edit":
						published = true
						return nil, nil
					}
				}
				return nil, errors.New("unexpected command")
			}
			err := run(t.Context(), []string{"publish-release", "--plan", planPath, "--image-digest", testDigest, "--controller-image-digest", controllerDigest, "--out", out}, execute)
			if failure != "" {
				if err == nil || charts != 0 || uploads != 0 || published {
					t.Fatalf("invalid controller image allowed publication: charts=%d uploads=%d published=%v err=%v", charts, uploads, published, err)
				}
				return
			}
			if err != nil || charts != 2 || uploads != 4 || !published {
				t.Fatalf("incomplete release: charts=%d uploads=%d published=%v err=%v", charts, uploads, published, err)
			}
			var metadata releaseMetadata
			if err := json.Unmarshal(assets["release.json"], &metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata.Components) != 2 || metadata.ImageDigest != testDigest || metadata.Components[0].Name != "moco" || metadata.Components[1].Name != "moco-controller" || metadata.Components[1].ImageRepository != p.ControllerImageRepository || metadata.Components[1].ImageDigest != controllerDigest {
				t.Fatalf("cross-wired release manifest: %+v", metadata)
			}
			for _, c := range metadata.Components {
				if !strings.Contains(string(assets["SHA256SUMS"]), c.ChartSHA256+"  "+c.Archive+"\n") {
					t.Fatal("chart absent from checksums", c.Name)
				}
			}
		})
	}
}

func TestControllerChartCannotOmitCRDsOrUseServerImage(t *testing.T) {
	p := fixturePlan(t)
	path := filepath.Join(t.TempDir(), "controller.tgz")
	chartFixture(t, path, p, controller(p), controllerDigest, true, true)
	if err := verifyChart(path, p, controller(p), controllerDigest); err != nil {
		t.Fatal(err)
	}
	if err := verifyChart(path, p, server(p), controllerDigest); err == nil {
		t.Fatal("controller chart accepted as server chart")
	}
	bad := controller(p)
	bad.ImageRepository = p.ImageRepository
	chartFixture(t, path, p, bad, controllerDigest, true, true)
	if err := verifyChart(path, p, controller(p), controllerDigest); err == nil {
		t.Fatal("controller chart accepted server image")
	}
	// A chart with matching metadata but no CRDs is still incomplete.
	chartFixture(t, path, p, controller(p), controllerDigest, true, false)
	if err := verifyChart(path, p, controller(p), controllerDigest); err == nil {
		t.Fatal("controller chart missing CRDs accepted")
	}

}
