package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestVaultKeyRotationServiceRewrapsBoundedPages(t *testing.T) {
	t.Parallel()
	repository := &fakeVaultKeyRotationRepository{replaceResult: true, rows: []ports.VaultKeyRecord{
		{TenantID: "tenant-a", VaultID: "vault-a", Key: ports.WrappedVaultKey{RootKeyID: "root-v1"}},
		{TenantID: "tenant-a", VaultID: "vault-b", Key: ports.WrappedVaultKey{RootKeyID: "root-v2"}},
		{TenantID: "tenant-b", VaultID: "vault-a", Key: ports.WrappedVaultKey{RootKeyID: "root-v1"}},
	}}
	rewrapper := fakeVaultKeyRewrapper{}
	service, err := services.NewVaultKeyRotationService(repository, rewrapper)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Rotate(context.Background(), services.VaultKeyRotationRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 2 || first.Rewrapped != 1 || first.Skipped != 1 || !first.HasMore || first.Complete || first.RemainingOldKeys != 1 || first.NextTenantID == nil || first.NextVaultID == nil {
		t.Fatalf("first rotation result = %#v", first)
	}
	second, err := service.Rotate(context.Background(), services.VaultKeyRotationRequest{
		AfterTenantID: *first.NextTenantID, AfterVaultID: *first.NextVaultID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 1 || second.Rewrapped != 1 || second.Skipped != 0 || second.HasMore || !second.Complete || second.RemainingOldKeys != 0 || second.NextTenantID != nil || second.NextVaultID != nil {
		t.Fatalf("second rotation result = %#v", second)
	}
	if len(repository.replacements) != 2 || repository.replacements[0].Replace.RootKeyID != "root-v2" || repository.replacements[1].Replace.RootKeyID != "root-v2" {
		t.Fatalf("replacement commands = %#v", repository.replacements)
	}
}

func TestVaultKeyRotationServiceIsRetrySafeAndValidatesCheckpoints(t *testing.T) {
	t.Parallel()
	repository := &fakeVaultKeyRotationRepository{rows: []ports.VaultKeyRecord{{TenantID: "tenant", VaultID: "vault", Key: ports.WrappedVaultKey{RootKeyID: "root-v1"}}}, replaceResult: false}
	service, err := services.NewVaultKeyRotationService(repository, fakeVaultKeyRewrapper{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Rotate(context.Background(), services.VaultKeyRotationRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rewrapped != 0 || result.Skipped != 1 {
		t.Fatalf("compare-and-swap loss = %#v", result)
	}
	for _, request := range []services.VaultKeyRotationRequest{
		{AfterTenantID: "tenant"},
		{AfterVaultID: "vault"},
		{Limit: services.MaxVaultKeyRotationPageSize + 1},
	} {
		if _, err := service.Rotate(context.Background(), request); err == nil {
			t.Fatalf("invalid rotation request %#v unexpectedly accepted", request)
		}
	}
}

func TestVaultKeyRotationServiceFencesStaleActiveEra(t *testing.T) {
	t.Parallel()
	repository := &fakeVaultKeyRotationRepository{}
	service, err := services.NewVaultKeyRotationService(repository, fakeVaultKeyRewrapper{}, services.VaultKeyRotationServiceOptions{
		KeyState: fakeEncryptionKeyStateReader{state: ports.EncryptionKeyState{ActiveRootKeyID: "root-v1", Epoch: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(context.Background(), services.VaultKeyRotationRequest{}); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
		t.Fatalf("stale rotation process returned %v, want key-state conflict", err)
	}
}

func TestVaultKeyRotationServicePropagatesMutationFence(t *testing.T) {
	t.Parallel()
	repository := &fakeVaultKeyRotationRepository{
		replaceResult: true,
		rows:          []ports.VaultKeyRecord{{TenantID: "tenant", VaultID: "vault", Key: ports.WrappedVaultKey{RootKeyID: "root-v1"}}},
	}
	service, err := services.NewVaultKeyRotationService(repository, fakeVaultKeyRewrapper{}, services.VaultKeyRotationServiceOptions{
		KeyState: fakeEncryptionKeyStateReader{state: ports.EncryptionKeyState{ActiveRootKeyID: "root-v2", Epoch: 7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(context.Background(), services.VaultKeyRotationRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if len(repository.replacements) != 1 || repository.replacements[0].ExpectedKeyState == nil {
		t.Fatalf("rotation did not propagate expected key state: %#v", repository.replacements)
	}
	if got := *repository.replacements[0].ExpectedKeyState; got.ActiveRootKeyID != "root-v2" || got.Epoch != 7 {
		t.Fatalf("unexpected expected key state: %#v", got)
	}
}

type fakeVaultKeyRotationRepository struct {
	rows          []ports.VaultKeyRecord
	replacements  []ports.ReplaceVaultKeyCommand
	replaceResult bool
}

func (f *fakeVaultKeyRotationRepository) ListVaultKeys(_ context.Context, query ports.ListVaultKeysQuery) ([]ports.VaultKeyRecord, error) {
	result := make([]ports.VaultKeyRecord, 0, query.PageSize)
	for _, row := range f.rows {
		if row.TenantID < query.AfterTenantID || (row.TenantID == query.AfterTenantID && row.VaultID <= query.AfterVaultID) {
			continue
		}
		result = append(result, row)
		if len(result) == query.PageSize {
			break
		}
	}
	return result, nil
}

func (f *fakeVaultKeyRotationRepository) ReplaceVaultKey(_ context.Context, command ports.ReplaceVaultKeyCommand) (bool, error) {
	f.replacements = append(f.replacements, command)
	if !f.replaceResult {
		return false, nil
	}
	for index, row := range f.rows {
		if row.TenantID == command.TenantID && row.VaultID == command.VaultID && row.Key.RootKeyID == command.Expected.RootKeyID {
			f.rows[index].Key = command.Replace
			break
		}
	}
	return true, nil
}

func (f *fakeVaultKeyRotationRepository) CountVaultKeysNotUsingRootKey(_ context.Context, rootKeyID string) (int64, error) {
	var count int64
	for _, row := range f.rows {
		if row.Key.RootKeyID != rootKeyID {
			count++
		}
	}
	return count, nil
}

type fakeVaultKeyRewrapper struct{}

func (fakeVaultKeyRewrapper) ActiveRootKeyID() string { return "root-v2" }

func (fakeVaultKeyRewrapper) RewrapVaultKey(_, _ string, key ports.WrappedVaultKey) (ports.WrappedVaultKey, error) {
	if key.RootKeyID == "broken" {
		return ports.WrappedVaultKey{}, errors.New("old key unavailable")
	}
	key.RootKeyID = "root-v2"
	return key, nil
}

type fakeEncryptionKeyStateReader struct {
	state ports.EncryptionKeyState
}

func (f fakeEncryptionKeyStateReader) CurrentEncryptionKeyState(context.Context) (ports.EncryptionKeyState, error) {
	return f.state, nil
}
