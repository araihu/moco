package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

const recoveryCursorKey = "recovery-test-cursor-key-at-least-32-bytes"
const recoveryToken = "recovery-test-bearer-token-at-least-32-bytes"

type recoveryFixture struct {
	store          *db.Store
	path           string
	oldKey, newKey []byte
	tenants        []ports.CreateTenantCommand
	vaults         []domain.Vault
	etags          []string
	policy         ports.AuthorizationState
	audit          ports.AuditEvent
}

func seedRecovery(t *testing.T) recoveryFixture {
	t.Helper()
	ctx := t.Context()
	f := recoveryFixture{
		path:   filepath.Join(t.TempDir(), "live.db"),
		oldKey: bytes.Repeat([]byte{0x31}, 32), newKey: bytes.Repeat([]byte{0x52}, 32),
	}
	var err error
	f.store, err = db.Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	if _, err := f.store.EnsureEncryptionKeyState(ctx, "root-v1", 1); err != nil {
		t.Fatal(err)
	}
	cipher := recoveryCipher(t, "root-v1", map[string][]byte{"root-v1": f.oldKey})
	service := recoverySecrets(t, f.store, cipher)
	now := time.Now().UTC()
	for i := 1; i <= 2; i++ {
		tenantID := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i)
		vaultID := fmt.Sprintf("%08d-2222-4222-8222-222222222222", i)
		command := ports.CreateTenantCommand{
			Tenant:      domain.Tenant{ID: tenantID, Name: fmt.Sprintf("tenant-%d", i), Labels: map[string]string{"environment": "prod"}, Revision: 1, CreatedAt: now, UpdatedAt: now},
			PrincipalID: "creator", IdempotencyKey: fmt.Sprintf("tenant-create-%d", i), RequestHash: "request-hash",
			ResponseETag: `"tenant-1"`, IdempotencyExpiresAt: now.Add(24 * time.Hour),
		}
		result, err := f.store.CreateTenant(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		command.Tenant = result.Tenant
		f.tenants = append(f.tenants, command)
		vault := domain.Vault{ID: vaultID, TenantID: tenantID, Name: "database", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
		created, err := f.store.CreateVault(ctx, ports.CreateVaultCommand{Vault: vault, ResponseETag: `"vault-1"`})
		if err != nil {
			t.Fatal(err)
		}
		f.vaults = append(f.vaults, created.Vault)
		_, etag, err := service.Put(ctx, tenantID, vaultID, "prod/password", domain.SecretWrite{Value: []byte(fmt.Sprintf("recovery-secret-for-tenant-%d", i))}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.etags = append(f.etags, etag)
	}
	// Include a persisted data key with no remaining secrets: restore must
	// authenticate it too, not just keys encountered by a secret join.
	empty := domain.Vault{ID: "33333333-3333-4333-8333-333333333333", TenantID: f.tenants[0].Tenant.ID, Name: "empty", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := f.store.CreateVault(ctx, ports.CreateVaultCommand{Vault: empty, ResponseETag: `"vault-1"`}); err != nil {
		t.Fatal(err)
	}
	key, err := cipher.NewVaultKey(empty.TenantID, empty.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateVaultKey(ctx, empty.TenantID, empty.ID, key, nil); err != nil {
		t.Fatal(err)
	}
	prefix := "prod/"
	f.policy = ports.AuthorizationState{
		RoleBindings: []ports.AuthorizationRoleBinding{{Principal: "reader", Role: "secret-reader", Domain: f.tenants[0].Tenant.ID}},
		Policies:     []ports.AuthorizationPolicy{{Subject: "secret-reader", Domain: f.tenants[0].Tenant.ID, Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET", SecretPathPrefix: &prefix}},
	}
	if err := f.store.ReplaceAuthorization(ctx, f.policy); err != nil {
		t.Fatal(err)
	}
	f.policy, err = f.store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := "reader"
	f.audit, err = f.store.AppendAuditEvent(ctx, ports.AuditEvent{OccurredAt: now, RequestID: "before-backup", PrincipalID: &reader, Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func recoveryCipher(t *testing.T, active string, keys map[string][]byte) *encryption.HKDFAESGCMEnvelope {
	t.Helper()
	cipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: active, RootKeys: keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cipher.Destroy)
	return cipher
}

func recoverySecrets(t *testing.T, store *db.Store, cipher ports.SecretCipher) *services.SecretService {
	t.Helper()
	service, err := services.NewSecretService(store, cipher, services.SecretServiceOptions{CursorHMACKey: []byte(recoveryCursorKey), KeyState: store})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestRecoveryBeforeDuringAndAfterRootKeyRotation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := seedRecovery(t)
	if info, err := os.Stat(f.path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("expected live uncheckpointed WAL: %v", err)
	}
	directory := t.TempDir()
	beforePath := filepath.Join(directory, "before.tar")
	before, err := Create(ctx, f.path, beforePath)
	if err != nil {
		t.Fatal(err)
	}
	allKeys := encryption.Keyring{ActiveKeyID: "root-v2", Keys: map[string][]byte{"root-v1": f.oldKey, "root-v2": f.newKey}}
	newOnly := encryption.Keyring{ActiveKeyID: "root-v2", Keys: map[string][]byte{"root-v2": f.newKey}}
	if _, err := f.store.EnsureEncryptionKeyState(ctx, "root-v2", 2); err != nil {
		t.Fatal(err)
	}
	cipher := recoveryCipher(t, allKeys.ActiveKeyID, allKeys.Keys)
	rotation, err := services.NewVaultKeyRotationService(f.store, cipher, services.VaultKeyRotationServiceOptions{KeyState: f.store})
	if err != nil {
		t.Fatal(err)
	}
	page, err := rotation.Rotate(ctx, services.VaultKeyRotationRequest{Limit: 1})
	if err != nil || page.Rewrapped != 1 || !page.HasMore {
		t.Fatalf("partial rotation: %+v err=%v", page, err)
	}
	duringPath := filepath.Join(directory, "during.tar")
	during, err := Create(ctx, f.path, duringPath)
	if err != nil {
		t.Fatal(err)
	}
	page, err = rotation.Rotate(ctx, services.VaultKeyRotationRequest{AfterTenantID: *page.NextTenantID, AfterVaultID: *page.NextVaultID, Limit: 10})
	if err != nil || !page.Complete || page.RemainingOldKeys != 0 {
		t.Fatalf("completed rotation: %+v err=%v", page, err)
	}
	afterPath := filepath.Join(directory, "after.tar")
	after, err := Create(ctx, f.path, afterPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.State.RequiredRootKeyIDs, []string{"root-v1"}) ||
		!slices.Equal(during.State.RequiredRootKeyIDs, []string{"root-v1", "root-v2"}) ||
		!slices.Equal(after.State.RequiredRootKeyIDs, []string{"root-v2"}) {
		t.Fatalf("unexpected key eras: before=%+v during=%+v after=%+v", before.State, during.State, after.State)
	}
	if before.State.ResourceVersion != during.State.ResourceVersion || before.State.ResourceVersion != after.State.ResourceVersion {
		t.Fatal("backup or rotation advanced resource versions")
	}
	// Mutate the live service after every snapshot, then lose the database.
	newCipher := recoveryCipher(t, newOnly.ActiveKeyID, newOnly.Keys)
	if _, _, err := recoverySecrets(t, f.store, newCipher).Put(ctx, f.tenants[0].Tenant.ID, f.vaults[0].ID, "prod/password", domain.SecretWrite{Value: []byte("written-after-snapshot")}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AppendAuditEvent(ctx, ports.AuditEvent{OccurredAt: time.Now().UTC(), RequestID: "after-backup", Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, path string
		manifest   Manifest
		keys       *encryption.Keyring
	}{
		{"before", beforePath, before, &allKeys}, {"during", duringPath, during, &allKeys}, {"after", afterPath, after, &newOnly},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "restored.db")
			if scenario.name != "after" {
				if _, err := Restore(ctx, scenario.path, destination, &newOnly); err == nil || !strings.Contains(err.Error(), "root-v1") {
					t.Fatalf("restore without historical key: %v", err)
				}
				assertAbsent(t, destination)
			}
			manifest, err := Restore(ctx, scenario.path, destination, scenario.keys)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(manifest, scenario.manifest) {
				t.Fatal("restore changed manifest")
			}
			assertPrivate(t, destination)
			assertRecovered(t, destination, scenario.manifest, f, scenario.keys)
		})
	}
}

func assertRecovered(t *testing.T, path string, manifest Manifest, f recoveryFixture, keys *encryption.Keyring) {
	t.Helper()
	ctx := t.Context()
	store, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	state, err := store.InspectSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(state, manifest.State) {
		t.Fatalf("restored state=%+v err=%v", state, err)
	}
	policy, err := store.LoadAuthorization(ctx)
	if err != nil || !reflect.DeepEqual(policy, f.policy) {
		t.Fatalf("restored policies=%+v err=%v", policy, err)
	}
	events, err := store.ListAuditEvents(ctx, ports.ListAuditEventsQuery{PageSize: 10})
	if err != nil || len(events) != 1 || events[0].RequestID != f.audit.RequestID || events[0].Sequence != f.audit.Sequence {
		t.Fatalf("restored audit=%+v err=%v", events, err)
	}
	for i, command := range f.tenants {
		tenant, err := store.GetTenant(ctx, command.Tenant.ID)
		if err != nil || !reflect.DeepEqual(tenant, command.Tenant) {
			t.Fatalf("tenant recovery: %+v err=%v", tenant, err)
		}
		vault, err := store.GetVault(ctx, tenant.ID, f.vaults[i].ID)
		if err != nil || !reflect.DeepEqual(vault, f.vaults[i]) {
			t.Fatalf("vault recovery: %+v err=%v", vault, err)
		}
		replay, err := store.CreateTenant(ctx, command)
		if err != nil || !replay.Replayed || replay.ETag != command.ResponseETag {
			t.Fatalf("idempotency recovery: %+v err=%v", replay, err)
		}
		tenantRevision, err := store.CurrentTenantResourceVersion(ctx, tenant.ID)
		if err != nil || tenantRevision == 0 {
			t.Fatalf("tenant watch recovery: %d err=%v", tenantRevision, err)
		}
	}
	cipher := recoveryCipher(t, keys.ActiveKeyID, keys.Keys)
	service := recoverySecrets(t, store, cipher)
	for i, command := range f.tenants {
		secret, etag, err := service.Get(ctx, command.Tenant.ID, f.vaults[i].ID, "prod/password")
		if err != nil || string(secret.Value) != fmt.Sprintf("recovery-secret-for-tenant-%d", i+1) || etag != f.etags[i] {
			t.Fatalf("secret recovery for tenant %d failed: err=%v", i+1, err)
		}
		clear(secret.Value)
	}
	assertRecoveredHTTPAccess(t, store, service, policy, f)
	// A pre-rotation backup preserves its old epoch. Explicitly advance it as
	// a restarted deployment would, then rewrap and retire the old key again.
	if _, err := store.EnsureEncryptionKeyState(ctx, keys.ActiveKeyID, 2); err != nil {
		t.Fatal(err)
	}
	rotation, err := services.NewVaultKeyRotationService(store, cipher, services.VaultKeyRotationServiceOptions{KeyState: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rotation.Rotate(ctx, services.VaultKeyRotationRequest{Limit: 10})
	if err != nil || !result.Complete {
		t.Fatalf("rotation after recovery: %+v err=%v", result, err)
	}
	newCipher := recoveryCipher(t, "root-v2", map[string][]byte{"root-v2": f.newKey})
	if err := store.VerifySnapshotEncryption(ctx, newCipher); err != nil {
		t.Fatalf("retire historical key after recovery: %v", err)
	}
	if _, _, err := recoverySecrets(t, store, newCipher).Put(ctx, f.tenants[0].Tenant.ID, f.vaults[0].ID, "prod/new", domain.SecretWrite{Value: []byte("new-after-recovery")}, nil, nil); err != nil {
		t.Fatalf("write after recovery: %v", err)
	}
	event, err := store.AppendAuditEvent(ctx, ports.AuditEvent{OccurredAt: time.Now().UTC(), RequestID: "after-recovery", Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success"})
	if err != nil || event.Sequence <= f.audit.Sequence {
		t.Fatalf("audit sequence did not continue: %+v err=%v", event, err)
	}
}

func assertRecoveredHTTPAccess(t *testing.T, store *db.Store, secrets *services.SecretService, policy ports.AuthorizationState, f recoveryFixture) {
	t.Helper()
	tenantService, err := services.NewTenantService(store, services.TenantServiceOptions{CursorHMACKey: []byte(recoveryCursorKey)})
	if err != nil {
		t.Fatal(err)
	}
	vaultService, err := services.NewVaultService(store, services.VaultServiceOptions{CursorHMACKey: []byte(recoveryCursorKey)})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(recoveryToken))
	authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{PrincipalID: "reader", TokenSHA256: fmt.Sprintf("%x", digest)}})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewStaticAuthorizer(policy.RoleBindings, policy.Policies)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{Tenants: tenantService, Vaults: vaultService, Secrets: secrets, Readiness: store, Authenticator: authenticator, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		tenant        int
		authenticated bool
		status        int
	}{{0, true, 200}, {1, true, 403}, {0, false, 401}} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tenants/"+f.tenants[test.tenant].Tenant.ID+"/vaults/"+f.vaults[test.tenant].ID+"/secret?path=prod/password", nil)
		if test.authenticated {
			request.Header.Set("Authorization", "Bearer "+recoveryToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("recovered API status=%d want=%d", response.Code, test.status)
		}
		if test.status == 200 && (response.Header().Get("ETag") != f.etags[0] || !strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString([]byte("recovery-secret-for-tenant-1")))) {
			t.Fatal("recovered API value or ETag mismatch")
		}
	}
}

func TestBackupRejectsWrongKeysAndNeverOverwrites(t *testing.T) {
	t.Parallel()
	f := seedRecovery(t)
	ctx := t.Context()
	directory := t.TempDir()
	archive := filepath.Join(directory, "backup.tar")
	if _, err := Create(ctx, f.path, archive); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, archive)
	if _, err := Verify(ctx, archive, nil); err != nil {
		t.Fatal(err)
	}
	wrong := encryption.Keyring{ActiveKeyID: "root-v1", Keys: map[string][]byte{"root-v1": bytes.Repeat([]byte{0x77}, 32)}}
	destination := filepath.Join(directory, "restore.db")
	if _, err := Restore(ctx, archive, destination, &wrong); err == nil {
		t.Fatal("wrong key material accepted")
	}
	assertAbsent(t, destination)
	if _, err := Restore(ctx, archive, destination, nil); err == nil {
		t.Fatal("restore without keys accepted")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		target := destination + suffix
		if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Restore(ctx, archive, destination, &wrong); err == nil {
			t.Fatalf("existing destination %q accepted", suffix)
		}
		content, err := os.ReadFile(target) // #nosec G304 -- test TempDir.
		if err != nil || string(content) != "sentinel" {
			t.Fatal("existing destination changed")
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(archive) // #nosec G304 -- test TempDir.
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{f.oldKey, f.newKey, []byte("recovery-secret-for-tenant-1"), []byte("recovery-secret-for-tenant-2"), []byte(recoveryToken), []byte(recoveryCursorKey)} {
		if bytes.Contains(before, secret) {
			t.Fatal("archive contains plaintext secret or deployment credential")
		}
	}
	if _, err := Create(ctx, f.path, archive); err == nil {
		t.Fatal("existing archive overwritten")
	}
	after, err := os.ReadFile(archive) // #nosec G304 -- test TempDir.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing archive changed")
	}
	if _, err := Create(ctx, filepath.Join(directory, "missing.db"), filepath.Join(directory, "missing.tar")); err == nil {
		t.Fatal("missing source accepted")
	}
	assertAbsent(t, filepath.Join(directory, "missing.db"))
	assertAbsent(t, filepath.Join(directory, "missing.tar"))
	assertNoStagingFiles(t, directory)
}

func TestBackupRejectsCorruptionAndUnsafeArchives(t *testing.T) {
	t.Parallel()
	f := seedRecovery(t)
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "valid.tar")
	if _, err := Create(t.Context(), f.path, archivePath); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(archivePath) // #nosec G304 -- test TempDir.
	if err != nil {
		t.Fatal(err)
	}
	entries := readTestArchive(t, original)
	keys := encryption.Keyring{ActiveKeyID: "root-v1", Keys: map[string][]byte{"root-v1": f.oldKey}}
	for _, name := range []string{"checksum", "manifest-state", "unknown-format", "truncated", "trailing", "extra-entry", "traversal", "symlink", "public-permissions", "dirty-schema", "foreign-key", "ciphertext", "missing-vault-key", "empty-vault-key", "future-schema"} {
		t.Run(name, func(t *testing.T) {
			payload := append([]byte(nil), original...)
			modified := []testArchiveEntry{{name: manifestName, payload: bytes.Clone(entries[0].payload), kind: tar.TypeReg}, {name: databaseName, payload: bytes.Clone(entries[1].payload), kind: tar.TypeReg}}
			switch name {
			case "checksum":
				modified[1].payload[len(modified[1].payload)-1] ^= 1
			case "manifest-state", "unknown-format":
				var manifest Manifest
				if err := json.Unmarshal(modified[0].payload, &manifest); err != nil {
					t.Fatal(err)
				}
				if name == "manifest-state" {
					manifest.State.ResourceVersion++
				} else {
					manifest.Format = "moco.backup/v99"
				}
				modified[0].payload, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
			case "extra-entry":
				modified = append(modified, testArchiveEntry{name: "extra", kind: tar.TypeReg, payload: []byte("extra")})
			case "traversal":
				modified[1].name = "../escape.db"
			case "symlink":
				modified[1].kind = tar.TypeSymlink
				modified[1].payload = nil
			case "dirty-schema", "foreign-key", "ciphertext", "missing-vault-key", "empty-vault-key", "future-schema":
				// Recompute the checksum to ensure SQLite validation, rather
				// than only the archive checksum, rejects damaged state.
				modified = corruptSnapshot(t, modified, name)
			}
			switch name {
			case "truncated":
				payload = payload[:len(payload)/2]
			case "trailing":
				payload = append(payload, []byte("trailing")...)
			default:
				payload = writeTestArchive(t, modified)
			}
			path := filepath.Join(t.TempDir(), "invalid.tar")
			// #nosec G703 -- fixed filename inside this test's private TempDir; archive names never reach this path.
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "public-permissions" {
				if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- deliberately test rejection of public backups.
					t.Fatal(err)
				}
			}
			destination := filepath.Join(t.TempDir(), "restore.db")
			if _, err := Restore(t.Context(), path, destination, &keys); err == nil {
				t.Fatal("unsafe backup accepted")
			}
			assertAbsent(t, destination)
			assertNoStagingFiles(t, filepath.Dir(destination))
		})
	}
}

type testArchiveEntry struct {
	name    string
	kind    byte
	payload []byte
}

func corruptSnapshot(t *testing.T, entries []testArchiveEntry, damage string) []testArchiveEntry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "damaged.db")
	if err := os.WriteFile(path, entries[1].payload, 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	statements := map[string]string{
		"dirty-schema":      "UPDATE schema_migrations SET dirty = 1",
		"future-schema":     "UPDATE schema_migrations SET version = 999",
		"foreign-key":       "DELETE FROM tenants",
		"ciphertext":        "UPDATE secrets SET ciphertext = zeroblob(length(ciphertext))",
		"missing-vault-key": "DELETE FROM vault_keys",
		"empty-vault-key":   "UPDATE vault_keys SET wrapped_key = zeroblob(48) WHERE vault_id = '33333333-3333-4333-8333-333333333333'",
	}
	if _, err := database.ExecContext(t.Context(), statements[damage]); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	entries[1].payload, err = os.ReadFile(path) // #nosec G304 -- test TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(entries[0].payload, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(entries[1].payload)
	manifest.DatabaseSHA256 = fmt.Sprintf("%x", digest)
	manifest.DatabaseBytes = int64(len(entries[1].payload))
	entries[0].payload, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func readTestArchive(t *testing.T, payload []byte) []testArchiveEntry {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(payload))
	var entries []testArchiveEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, testArchiveEntry{header.Name, header.Typeflag, content})
	}
}

func writeTestArchive(t *testing.T, entries []testArchiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0o600, Size: int64(len(entry.payload)), Linkname: "elsewhere"}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output file: %v", err)
	}
}

func assertPrivate(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions=%o, want 600", info.Mode().Perm())
	}
}

func assertNoStagingFiles(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".moco-") {
			t.Fatalf("staging file leaked: %s", entry.Name())
		}
	}
}

func TestBackupCancellationCleansUp(t *testing.T) {
	t.Parallel()
	f := seedRecovery(t)
	directory := t.TempDir()
	archive := filepath.Join(directory, "backup.tar")
	if _, err := Create(t.Context(), f.path, archive); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Create(ctx, f.path, filepath.Join(directory, "cancelled.tar")); !errors.Is(err, context.Canceled) {
		t.Fatalf("create cancellation: %v", err)
	}
	keys := encryption.Keyring{ActiveKeyID: "root-v1", Keys: map[string][]byte{"root-v1": f.oldKey}}
	if _, err := Restore(ctx, archive, filepath.Join(directory, "cancelled.db"), &keys); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore cancellation: %v", err)
	}
	assertAbsent(t, filepath.Join(directory, "cancelled.tar"))
	assertAbsent(t, filepath.Join(directory, "cancelled.db"))
	assertNoStagingFiles(t, directory)
}

func TestSnapshotStaysConsistentWithConcurrentWriter(t *testing.T) {
	t.Parallel()
	f := seedRecovery(t)
	baseline, err := f.store.CurrentResourceVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan error, 1)
	consumed := false
	go func() {
		first := true
		for {
			if ctx.Err() != nil {
				done <- nil
				return
			}
			_, err := f.store.UpdateTenant(ctx, f.tenants[0].Tenant.ID, domain.TenantUpdate{Name: "tenant-1", Labels: map[string]string{"environment": "prod"}}, nil, time.Now().UTC())
			if err != nil {
				if ctx.Err() != nil {
					err = nil
				}
				done <- err
				return
			}
			if first {
				close(started)
				first = false
			}
		}
	}()
	defer func() {
		cancel()
		if consumed {
			return
		}
		if err := <-done; err != nil {
			t.Errorf("concurrent writer: %v", err)
		}
	}()
	select {
	case <-started:
	case err := <-done:
		consumed = true
		t.Fatalf("writer failed to start: %v", err)
	}
	directory := t.TempDir()
	archive := filepath.Join(directory, "concurrent.tar")
	manifest, err := Create(t.Context(), f.path, archive)
	if err != nil {
		t.Fatal(err)
	}
	keys := encryption.Keyring{ActiveKeyID: "root-v1", Keys: map[string][]byte{"root-v1": f.oldKey}}
	destination := filepath.Join(directory, "restore.db")
	if _, err := Restore(t.Context(), archive, destination, &keys); err != nil {
		t.Fatal(err)
	}
	store, err := db.OpenReadOnly(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	tenant, err := store.GetTenant(t.Context(), f.tenants[0].Tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Revision < 2 || manifest.State.ResourceVersion != baseline+tenant.Revision-1 {
		t.Fatalf("snapshot mixed transactions: revision=%d resourceVersion=%d baseline=%d", tenant.Revision, manifest.State.ResourceVersion, baseline)
	}
}
