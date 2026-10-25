package operations

import (
	"os"
	"path/filepath"
	"testing"
)

func runtimeFixture(t *testing.T) RuntimeOptions {
	t.Helper()
	root := t.TempDir()
	options := RuntimeOptions{Source: filepath.Join(root, "projected"), Credentials: filepath.Join(root, "private"),
		Data: filepath.Join(root, "data"), Backups: filepath.Join(root, "backups"), Mirror: filepath.Join(root, "mirror"),
		MirrorID: "11111111-1111-4111-8111-111111111111"}
	version := filepath.Join(options.Source, "..version")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..version", filepath.Join(options.Source, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"access.json", "cursor-key", "keyring.json", "metrics-token", "tls.crt", "tls.key", "ca.pem"} {
		if err := os.WriteFile(filepath.Join(version, name), []byte("fixture-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(options.Source, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(options.Mirror, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InitializeMirror(options.Mirror, options.MirrorID); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestPrepareRuntimeCopiesProjectionsAndRepairsVolumePermissions(t *testing.T) {
	t.Parallel()
	options := runtimeFixture(t)
	if err := os.Mkdir(options.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(options.Data, "moco.db")
	if err := os.WriteFile(database, []byte("existing-sqlite-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{database, filepath.Join(options.Mirror, ".moco-destination")} {
		if err := os.Chmod(path, 0o640); err != nil { // #nosec G302 -- simulate CSI fsGroup widening before initialization.
			t.Fatal(err)
		}
	}
	for range 2 { // A retried init container must preserve existing data and work.
		if err := PrepareRuntime(options); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"access.json", "cursor-key", "keyring.json", "metrics-token", "tls.crt", "tls.key", "ca.pem"} {
		data, err := ReadPrivateFile(filepath.Join(options.Credentials, name), 1<<20)
		if err != nil || string(data) != "fixture-"+name {
			t.Fatalf("private copy %s: %v", name, err)
		}
	}
	data, err := ReadPrivateFile(database, 128)
	if err != nil || string(data) != "existing-sqlite-fixture" {
		t.Fatal("runtime preparation changed existing database contents", err)
	}
}

func TestPrepareRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"mirror identity", "absent mirror", "source escape", "storage symlink", "overlap"} {
		t.Run(failure, func(t *testing.T) {
			options := runtimeFixture(t)
			switch failure {
			case "mirror identity":
				options.MirrorID = "22222222-2222-4222-8222-222222222222"
			case "absent mirror":
				options.Mirror += "-absent"
			case "source escape":
				if err := os.Remove(filepath.Join(options.Source, "keyring.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../mirror/.moco-destination", filepath.Join(options.Source, "keyring.json")); err != nil {
					t.Fatal(err)
				}
			case "storage symlink":
				if err := os.Symlink(options.Mirror, options.Data); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				options.Backups = filepath.Join(options.Data, "backups")
			}
			if err := PrepareRuntime(options); err == nil {
				t.Fatal("unsafe runtime preparation accepted")
			}
		})
	}
}
