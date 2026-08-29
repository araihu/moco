package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"

	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/services"
)

func TestSecretLifecycleEndToEnd(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	missingVault := "00000000-0000-4000-8000-000000000002"
	missingPath := "/api/v1/tenants/00000000-0000-4000-8000-000000000001/vaults/" + missingVault
	response := test.request(t, http.MethodGet, missingPath+"/secrets", nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)

	tenant := test.createTenant(t, "secret-parent")
	vaultsPath := "/api/v1/tenants/" + tenant.Id.String() + "/vaults"
	vault := test.createVault(t, vaultsPath, "encrypted")
	vaultPath := vaultsPath + "/" + vault.Id.String()
	path := "prod/db/password"
	itemPath := secretItemPath(vaultPath, path)
	metadataPath := secretMetadataPath(vaultPath, path)

	response = test.request(t, http.MethodPut, secretItemPath(vaultPath, "a/../b"), secretWriteBody(t, []byte("value"), nil), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response = test.request(t, http.MethodGet, vaultPath+"/secrets?prefix="+url.QueryEscape("a//b"), nil, nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response = test.request(t, http.MethodPut, itemPath, []byte(`{"value":"dmFsdWU=","unknown":true}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response = test.request(t, http.MethodPut, itemPath, []byte(`{"value":null}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response = test.request(t, http.MethodPut, itemPath, secretWriteBody(t, make([]byte, domain.MaxSecretValueBytes+1), nil), nil, true)
	assertStatus(t, response, http.StatusRequestEntityTooLarge)
	tooLarge := decode[httpapi.Problem](t, response)
	if tooLarge.Code != "secret_too_large" {
		t.Fatalf("unexpected size problem: %#v", tooLarge)
	}
	response = test.request(t, http.MethodPut, itemPath, bytes.Repeat([]byte{'A'}, 3<<20), nil, true)
	assertStatus(t, response, http.StatusRequestEntityTooLarge)
	tooLarge = decode[httpapi.Problem](t, response)
	if tooLarge.Code != "secret_too_large" {
		t.Fatalf("unexpected raw-body size problem: %#v", tooLarge)
	}

	contentType := "text/plain"
	body := secretWriteBody(t, []byte("secret"), &contentType)
	response = test.request(t, http.MethodPut, itemPath, body, nil, true)
	assertStatus(t, response, http.StatusCreated)
	firstETag := response.Header.Get("ETag")
	created := decode[httpapi.SecretMetadata](t, response)
	if created.Path != path || created.Version != 1 || created.ContentType != contentType || created.Digest != digestOf([]byte("secret")) || firstETag == "" || response.Header.Get("Location") != itemPath {
		t.Fatalf("unexpected created secret: %#v, headers %v", created, response.Header)
	}
	if bytes.Contains(response.Body, []byte(`"value"`)) {
		t.Fatalf("write response leaked a value field: %s", response.Body)
	}

	response = test.request(t, http.MethodPut, itemPath, body, nil, true)
	assertStatus(t, response, http.StatusOK)
	replayed := decode[httpapi.SecretMetadata](t, response)
	if replayed.Version != 1 || response.Header.Get("ETag") != firstETag {
		t.Fatalf("idempotent PUT changed state: %#v, headers %v", replayed, response.Header)
	}
	response = test.request(t, http.MethodPut, itemPath, body, map[string]string{"If-None-Match": "*"}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	if response.Header.Get("ETag") != firstETag {
		t.Fatalf("create-only miss omitted current ETag: %v", response.Header)
	}
	response = test.request(t, http.MethodPut, itemPath, body, map[string]string{"If-Match": firstETag, "If-None-Match": "*"}, true)
	assertStatus(t, response, http.StatusBadRequest)

	response = test.request(t, http.MethodGet, itemPath, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	read := decode[httpapi.Secret](t, response)
	if !bytes.Equal(read.Value, []byte("secret")) || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != firstETag {
		t.Fatalf("unexpected secret read: %#v, headers %v", read, response.Header)
	}
	response = test.request(t, http.MethodGet, itemPath, nil, map[string]string{"If-None-Match": firstETag}, true)
	assertStatus(t, response, http.StatusNotModified)
	if len(response.Body) != 0 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("conditional secret read returned a body: %q", response.Body)
	}
	response = test.request(t, http.MethodGet, metadataPath, nil, map[string]string{"If-None-Match": firstETag}, true)
	assertStatus(t, response, http.StatusNotModified)
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("conditional metadata read omitted no-store: %v", response.Header)
	}

	updatedBody := secretWriteBody(t, []byte("new-secret"), &contentType)
	response = test.request(t, http.MethodPut, itemPath, updatedBody, map[string]string{"If-Match": `"stale"`}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response = test.request(t, http.MethodPut, itemPath, updatedBody, map[string]string{"If-Match": firstETag}, true)
	assertStatus(t, response, http.StatusOK)
	updatedETag := response.Header.Get("ETag")
	updated := decode[httpapi.SecretMetadata](t, response)
	if updated.Version != 2 || updated.Digest != digestOf([]byte("new-secret")) || updatedETag == firstETag {
		t.Fatalf("unexpected replaced secret: %#v, headers %v", updated, response.Header)
	}

	secondPath := "prod/api/token"
	response = test.request(t, http.MethodPut, secretItemPath(vaultPath, secondPath), secretWriteBody(t, []byte("token"), nil), nil, true)
	assertStatus(t, response, http.StatusCreated)
	response = test.request(t, http.MethodPut, secretItemPath(vaultPath, "dev/key"), secretWriteBody(t, []byte("development"), nil), nil, true)
	assertStatus(t, response, http.StatusCreated)
	listPath := vaultPath + "/secrets?limit=1&prefix=" + url.QueryEscape("prod/")
	response = test.request(t, http.MethodGet, listPath, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	firstPage := decode[httpapi.SecretList](t, response)
	if len(firstPage.Items) != 1 || firstPage.Items[0].Path != path || firstPage.Page.NextCursor == nil || !firstPage.Page.HasMore || bytes.Contains(response.Body, []byte(`"value"`)) {
		t.Fatalf("unexpected first metadata page: %#v", firstPage)
	}
	response = test.request(t, http.MethodPut, secretItemPath(vaultPath, "prod/later"), secretWriteBody(t, []byte("later"), nil), nil, true)
	assertStatus(t, response, http.StatusCreated)
	continuation := vaultPath + "/secrets?limit=200&prefix=" + url.QueryEscape("prod/") + "&cursor=" + url.QueryEscape(*firstPage.Page.NextCursor)
	response = test.request(t, http.MethodGet, continuation, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	secondPage := decode[httpapi.SecretList](t, response)
	if len(secondPage.Items) != 1 || secondPage.Items[0].Path != secondPath || secondPage.Page.HasMore {
		t.Fatalf("secret snapshot admitted a later insert or lost metadata: %#v", secondPage)
	}

	otherVault := test.createVault(t, vaultsPath, "other-encrypted")
	otherVaultPath := vaultsPath + "/" + otherVault.Id.String()
	response = test.request(t, http.MethodGet, secretItemPath(otherVaultPath, path), nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
	response = test.request(t, http.MethodGet, otherVaultPath+"/secrets?cursor="+url.QueryEscape(*firstPage.Page.NextCursor), nil, nil, true)
	assertStatus(t, response, http.StatusBadRequest)

	response = test.request(t, http.MethodDelete, itemPath, nil, map[string]string{"If-Match": firstETag}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response = test.request(t, http.MethodDelete, itemPath, nil, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusNoContent)
	response = test.request(t, http.MethodGet, itemPath, nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
	response = test.request(t, http.MethodPut, itemPath, secretWriteBody(t, []byte("recreated"), nil), nil, true)
	assertStatus(t, response, http.StatusCreated)
	if response.Header.Get("ETag") == updatedETag {
		t.Fatalf("delete/recreate reused a secret ETag: %q", response.Header.Get("ETag"))
	}

	response = test.request(t, http.MethodGet, vaultPath, nil, nil, true)
	vaultETag := response.Header.Get("ETag")
	response = test.request(t, http.MethodDelete, vaultPath, nil, map[string]string{"If-Match": vaultETag}, true)
	assertStatus(t, response, http.StatusConflict)
	children := decode[httpapi.Problem](t, response)
	if children.Code != "resource_has_children" {
		t.Fatalf("unexpected vault child conflict: %#v", children)
	}
	response = test.request(t, http.MethodDelete, vaultPath+"?cascade=true", nil, map[string]string{"If-Match": vaultETag}, true)
	assertStatus(t, response, http.StatusNoContent)
	response = test.request(t, http.MethodGet, secretItemPath(vaultPath, secondPath), nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
}

func TestConcurrentConditionalSecretWriteAllowsOneWinner(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	tenant := test.createTenant(t, "secret-race-parent")
	vaultsPath := "/api/v1/tenants/" + tenant.Id.String() + "/vaults"
	vault := test.createVault(t, vaultsPath, "secret-race")
	path := secretItemPath(vaultsPath+"/"+vault.Id.String(), "race/value")
	response := test.request(t, http.MethodPut, path, secretWriteBody(t, []byte("initial"), nil), nil, true)
	assertStatus(t, response, http.StatusCreated)
	etag := response.Header.Get("ETag")

	statuses := make(chan int, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, value := range []string{"one", "two"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, err := json.Marshal(map[string]any{"value": []byte(value)})
			if err != nil {
				errorsChannel <- err
				return
			}
			result, err := test.rawRequest(http.MethodPut, path, body, map[string]string{"If-Match": etag}, true)
			if err != nil {
				errorsChannel <- err
				return
			}
			statuses <- result.StatusCode
			_, readErr := io.Copy(io.Discard, result.Body)
			closeErr := result.Body.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				errorsChannel <- err
			}
		}()
	}
	wait.Wait()
	close(statuses)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent secret write failed: %v", err)
	}
	got := make([]int, 0, 2)
	for status := range statuses {
		got = append(got, status)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusOK, http.StatusPreconditionFailed}) {
		t.Fatalf("expected one secret write winner, got %v", got)
	}
}

func secretItemPath(vaultPath, path string) string {
	return vaultPath + "/secret?path=" + url.QueryEscape(path)
}

func secretMetadataPath(vaultPath, path string) string {
	return vaultPath + "/secret/metadata?path=" + url.QueryEscape(path)
}

func secretWriteBody(t *testing.T, value []byte, contentType *string) []byte {
	t.Helper()
	body, err := json.Marshal(httpapi.SecretWrite{Value: value, ContentType: contentType})
	if err != nil {
		t.Fatalf("encode secret write: %v", err)
	}
	return body
}

func digestOf(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
