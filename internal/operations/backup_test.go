package operations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/backup"
	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func backupFixture(t *testing.T) BackupOptions {
	t.Helper()
	root := t.TempDir()
	options := BackupOptions{Database: filepath.Join(root, "moco.db"), Directory: filepath.Join(root, "local"), MirrorDirectory: filepath.Join(root, "remote"), Keyring: filepath.Join(root, "keys.json"), Keep: 2}
	for _, directory := range []string{options.Directory, options.MirrorDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options.MirrorID = "12345678-1234-4234-8234-123456789012"
	if err := InitializeMirror(options.MirrorDirectory, options.MirrorID); err != nil {
		t.Fatal(err)
	}
	rootKey := bytes.Repeat([]byte{1}, 32)
	payload, err := json.Marshal(map[string]any{"activeKeyId": "root-v1", "keys": map[string]string{"root-v1": base64.StdEncoding.EncodeToString(rootKey)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.Keyring, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(t.Context(), options.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.EnsureEncryptionKeyState(t.Context(), "root-v1", 1); err != nil {
		t.Fatal(err)
	}
	tenant := domain.Tenant{ID: "11111111-1111-4111-8111-111111111111", Name: "production", Labels: map[string]string{}, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if _, err := store.CreateTenant(t.Context(), ports.CreateTenantCommand{Tenant: tenant, ResponseETag: `"tenant-1"`}); err != nil {
		t.Fatal(err)
	}
	vault := domain.Vault{ID: "22222222-2222-4222-8222-222222222222", TenantID: tenant.ID, Name: "database", Labels: map[string]string{}, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if _, err := store.CreateVault(t.Context(), ports.CreateVaultCommand{Vault: vault, ResponseETag: `"vault-1"`}); err != nil {
		t.Fatal(err)
	}
	cipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKeys: map[string][]byte{"root-v1": rootKey}})
	if err != nil {
		t.Fatal(err)
	}
	defer cipher.Destroy()
	service, err := services.NewSecretService(store, cipher, services.SecretServiceOptions{CursorHMACKey: bytes.Repeat([]byte{2}, 32), KeyState: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Put(t.Context(), tenant.ID, vault.ID, "prod/password", domain.SecretWrite{Value: []byte("recover-this-secret")}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return options
}

func managedArchives(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if archiveName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestScheduledBackupVerifiesMirrorRestoresAndRetains(t *testing.T) {
	t.Parallel()
	options := backupFixture(t)
	unrelated := filepath.Join(options.MirrorDirectory, "operator-note.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var status BackupStatus
	for range 3 {
		var err error
		status, err = RunBackup(t.Context(), options)
		if err != nil {
			t.Fatal(err)
		}
		if !status.LastAttemptOK || status.SnapshotAt.IsZero() || status.LastSuccess.Before(status.SnapshotAt) {
			t.Fatalf("bad job status: %+v", status)
		}
	}
	for _, directory := range []string{options.Directory, options.MirrorDirectory} {
		if names := managedArchives(t, directory); len(names) != 2 {
			t.Fatalf("retention: %v", names)
		}
		info, err := os.Stat(filepath.Join(directory, status.Archive))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private archive: %v", err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("retention removed an unrelated file")
	}
	local, err := os.ReadFile(filepath.Join(options.Directory, status.Archive))
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := os.ReadFile(filepath.Join(options.MirrorDirectory, status.Archive))
	if err != nil || !bytes.Equal(local, mirror) {
		t.Fatal("mirror differs")
	}
	payload, err := ReadPrivateFile(options.Keyring, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := encryption.ParseKeyring(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer keyring.Destroy()
	if _, err := backup.Restore(t.Context(), filepath.Join(options.MirrorDirectory, status.Archive), filepath.Join(t.TempDir(), "restored.db"), &keyring); err != nil {
		t.Fatal(err)
	}
	previous := status
	if err := os.Rename(options.MirrorDirectory, options.MirrorDirectory+"-offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunBackup(t.Context(), options); err == nil {
		t.Fatal("missing mirror accepted")
	}
	status, err = ReadBackupStatus(filepath.Join(options.Directory, StatusName))
	if err != nil || status.LastAttemptOK || status.Failures != 1 || !status.LastSuccess.Equal(previous.LastSuccess) || !status.SnapshotAt.Equal(previous.SnapshotAt) {
		t.Fatalf("failed mirror falsely advanced recovery freshness: %+v, %v", status, err)
	}
	if len(managedArchives(t, options.Directory)) != 2 {
		t.Fatal("failure pruned local backups")
	}
}

func TestBackupFailureAndLockLeaveRecoverableArchives(t *testing.T) {
	t.Parallel()
	options := backupFixture(t)
	if _, err := RunBackup(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	lock, err := Lock(filepath.Join(options.Directory, ".backup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunBackup(t.Context(), options); err == nil {
		t.Fatal("parallel maintenance job accepted")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RunBackup(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled job: %v", err)
	}
	if len(managedArchives(t, options.MirrorDirectory)) != 1 {
		t.Fatal("failed job pruned recoverable mirror")
	}
	if err := os.WriteFile(options.Keyring, []byte("wrong keyring"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunBackup(t.Context(), options); err == nil {
		t.Fatal("invalid recovery keyring accepted")
	}
	status, err := ReadBackupStatus(filepath.Join(options.Directory, StatusName))
	if err != nil || status.Failures != 2 || status.LastAttemptOK {
		t.Fatalf("failed job status: %+v %v", status, err)
	}
}

func TestUnmarkedMirrorDoesNotClaimRecoveryFreshness(t *testing.T) {
	t.Parallel()
	options := backupFixture(t)
	if err := os.Remove(filepath.Join(options.MirrorDirectory, ".moco-destination")); err != nil {
		t.Fatal(err)
	}
	if _, err := RunBackup(t.Context(), options); err == nil {
		t.Fatal("unmarked mountpoint accepted as the off-host mirror")
	}
	status, err := ReadBackupStatus(filepath.Join(options.Directory, StatusName))
	if err != nil || status.LastAttemptOK || !status.LastSuccess.IsZero() || status.Failures != 1 {
		t.Fatalf("false mirror freshness: %+v %v", status, err)
	}
	if len(managedArchives(t, options.Directory)) != 0 {
		t.Fatal("archive created before destination identity was checked")
	}
}

func TestPrivateCredentialsAndInitializer(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "credentials")
	if err := Initialize(directory); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(directory); err == nil {
		t.Fatal("existing credentials overwritten")
	}
	for _, name := range []string{"keyring.json", "cursor-key", "metrics-token", "admin-token", "access.json"} {
		if _, err := ReadPrivateFile(filepath.Join(directory, name), 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(directory, "cursor-key")
	if err := os.Symlink(path, filepath.Join(directory, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(filepath.Join(directory, "linked"), 4096); err == nil {
		t.Fatal("symlink credential accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- negative test: public credentials must be rejected.
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, 4096); err == nil {
		t.Fatal("public credential accepted")
	}
}
