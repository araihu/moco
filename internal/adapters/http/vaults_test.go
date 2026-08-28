package httpapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/services"
)

func TestVaultLifecycleEndToEnd(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	missingTenant := "00000000-0000-4000-8000-000000000001"
	response := test.request(t, http.MethodPost, "/api/v1/tenants/"+missingTenant+"/vaults", []byte(`{"name":"missing-parent"}`), nil, true)
	assertStatus(t, response, http.StatusNotFound)
	response = test.request(t, http.MethodGet, "/api/v1/tenants/"+missingTenant+"/vaults", nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)

	tenant := test.createTenant(t, "vault-parent")
	tenantPath := "/api/v1/tenants/" + tenant.Id.String()
	vaultsPath := tenantPath + "/vaults"
	vaultBody := []byte(`{"name":"application","description":"runtime secrets","externalId":"kubernetes://cluster-a/application","labels":{"owner":"platform"}}`)
	createHeaders := map[string]string{"Idempotency-Key": "vault-application-key"}
	response = test.request(t, http.MethodPost, vaultsPath, vaultBody, createHeaders, true)
	assertStatus(t, response, http.StatusCreated)
	vaultETag := response.Header.Get("ETag")
	vaultLocation := response.Header.Get("Location")
	vault := decode[httpapi.Vault](t, response)
	if vault.TenantId != tenant.Id || vault.Name != "application" || vault.Revision != 1 || vaultLocation != vaultsPath+"/"+vault.Id.String() || vaultETag == "" {
		t.Fatalf("unexpected created vault: %#v, headers %v", vault, response.Header)
	}

	response = test.request(t, http.MethodPost, vaultsPath, vaultBody, createHeaders, true)
	assertStatus(t, response, http.StatusCreated)
	replay := decode[httpapi.Vault](t, response)
	if replay.Id != vault.Id || response.Header.Get("ETag") != vaultETag {
		t.Fatalf("vault idempotency replay diverged: %#v", replay)
	}
	response = test.request(t, http.MethodPost, vaultsPath, []byte(`{"name":"different"}`), createHeaders, true)
	assertStatus(t, response, http.StatusConflict)
	problem := decode[httpapi.Problem](t, response)
	if problem.Code != "idempotency_key_conflict" {
		t.Fatalf("unexpected vault idempotency problem: %#v", problem)
	}

	response = test.request(t, http.MethodPost, vaultsPath, []byte(`{"name":"application"}`), nil, true)
	assertStatus(t, response, http.StatusConflict)
	nameConflict := decode[httpapi.Problem](t, response)
	if nameConflict.ResourceId == nil || *nameConflict.ResourceId != vault.Id.String() {
		t.Fatalf("vault conflict did not identify existing resource: %#v", nameConflict)
	}
	beta := test.createVault(t, vaultsPath, "beta")

	response = test.request(t, http.MethodGet, vaultsPath+"?limit=1", nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	firstPage := decode[httpapi.VaultList](t, response)
	if len(firstPage.Items) != 1 || firstPage.Page.NextCursor == nil || !firstPage.Page.HasMore {
		t.Fatalf("unexpected first vault page: %#v", firstPage)
	}
	test.createVault(t, vaultsPath, "gamma")
	response = test.request(t, http.MethodGet, vaultsPath+"?limit=200&cursor="+*firstPage.Page.NextCursor, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	secondPage := decode[httpapi.VaultList](t, response)
	if len(secondPage.Items) != 1 || secondPage.Items[0].Id != beta.Id || secondPage.Page.HasMore {
		t.Fatalf("vault snapshot admitted a later insert or lost beta: %#v", secondPage)
	}

	otherTenant := test.createTenant(t, "other-vault-parent")
	otherVaultsPath := "/api/v1/tenants/" + otherTenant.Id.String() + "/vaults"
	other := test.createVault(t, otherVaultsPath, "application")
	if other.Name != vault.Name {
		t.Fatalf("same name should be allowed in another tenant: %#v", other)
	}
	response = test.request(t, http.MethodGet, otherVaultsPath+"?cursor="+*firstPage.Page.NextCursor, nil, nil, true)
	assertStatus(t, response, http.StatusBadRequest)

	response = test.request(t, http.MethodGet, vaultLocation, nil, map[string]string{"If-None-Match": vaultETag}, true)
	assertStatus(t, response, http.StatusNotModified)
	updateBody := []byte(`{"name":"application-updated","description":null,"labels":{"owner":"runtime"}}`)
	response = test.request(t, http.MethodPut, vaultLocation, updateBody, map[string]string{"If-Match": `"stale"`}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response = test.request(t, http.MethodPut, vaultLocation, updateBody, map[string]string{"If-Match": vaultETag}, true)
	assertStatus(t, response, http.StatusOK)
	updatedETag := response.Header.Get("ETag")
	updated := decode[httpapi.Vault](t, response)
	if updated.Revision != 2 || updated.Name != "application-updated" || updatedETag == vaultETag {
		t.Fatalf("unexpected updated vault: %#v", updated)
	}

	conflictingBody, _ := json.Marshal(map[string]any{"name": "beta", "description": nil, "labels": map[string]string{}})
	response = test.request(t, http.MethodPut, vaultLocation, conflictingBody, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusConflict)

	response = test.request(t, http.MethodGet, vaultLocation+"/secrets", nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	secretPage := decode[httpapi.SecretList](t, response)
	if len(secretPage.Items) != 0 || secretPage.Page.HasMore {
		t.Fatalf("new vault unexpectedly contains secrets: %#v", secretPage)
	}

	response = test.request(t, http.MethodDelete, vaultLocation, nil, map[string]string{"If-Match": vaultETag}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response = test.request(t, http.MethodDelete, vaultLocation, nil, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusNoContent)

	response = test.request(t, http.MethodGet, tenantPath, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	tenantETag := response.Header.Get("ETag")
	response = test.request(t, http.MethodDelete, tenantPath, nil, map[string]string{"If-Match": tenantETag}, true)
	assertStatus(t, response, http.StatusConflict)
	children := decode[httpapi.Problem](t, response)
	if children.Code != "resource_has_children" {
		t.Fatalf("unexpected tenant child conflict: %#v", children)
	}
	response = test.request(t, http.MethodDelete, tenantPath+"?cascade=true", nil, map[string]string{"If-Match": tenantETag}, true)
	assertStatus(t, response, http.StatusNoContent)
	response = test.request(t, http.MethodGet, vaultsPath+"/"+beta.Id.String(), nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
	response = test.request(t, http.MethodGet, vaultsPath+"?cursor="+*firstPage.Page.NextCursor, nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
}

func TestConcurrentConditionalVaultUpdateAllowsOneWinner(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	tenant := test.createTenant(t, "vault-race-parent")
	vaultsPath := "/api/v1/tenants/" + tenant.Id.String() + "/vaults"
	vault := test.createVault(t, vaultsPath, "vault-race")
	path := vaultsPath + "/" + vault.Id.String()
	response := test.request(t, http.MethodGet, path, nil, nil, true)
	etag := response.Header.Get("ETag")

	statuses := make(chan int, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, name := range []string{"vault-race-one", "vault-race-two"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, _ := json.Marshal(map[string]any{"name": name, "description": nil, "labels": map[string]string{}})
			result, err := test.rawRequest(http.MethodPut, path, body, map[string]string{"If-Match": etag}, true)
			if err != nil {
				errors <- err
				return
			}
			statuses <- result.StatusCode
			if err := result.Body.Close(); err != nil {
				errors <- err
			}
		}()
	}
	wait.Wait()
	close(statuses)
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent vault update failed: %v", err)
	}
	got := []int{}
	for status := range statuses {
		got = append(got, status)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusOK, http.StatusPreconditionFailed}) {
		t.Fatalf("expected one vault update winner, got statuses %v", got)
	}
}

func (f fixture) createVault(t *testing.T, vaultsPath, name string) httpapi.Vault {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "labels": map[string]string{}})
	response := f.request(t, http.MethodPost, vaultsPath, body, nil, true)
	assertStatus(t, response, http.StatusCreated)
	vault := decode[httpapi.Vault](t, response)
	return vault
}
