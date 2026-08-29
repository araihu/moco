package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/services"
)

// PutSecret creates or replaces one encrypted value.
func (s *Server) PutSecret(ctx context.Context, request PutSecretRequestObject) (PutSecretResponseObject, error) {
	if request.Body == nil {
		problem := badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")
		return PutSecret400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	defer wipeHTTPBytes(request.Body.Value)
	result, etag, err := s.secrets.Put(
		ctx, request.TenantId.String(), request.VaultId.String(), string(request.Params.Path),
		domain.SecretWrite{Value: request.Body.Value, ContentType: request.Body.ContentType},
		optionalAlias(request.Params.IfMatch), optionalAlias(request.Params.IfNoneMatch),
	)
	if err != nil {
		mapped := s.problem(ctx, "putSecret", err)
		switch mapped.problem.Status {
		case 400:
			return PutSecret400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return PutSecret404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 412:
			return PutSecret412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		case 413:
			return PutSecret413ApplicationProblemPlusJSONResponse{payloadTooLargeResponse(mapped.problem)}, nil
		default:
			return PutSecret500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	body := secretMetadataResponse(result.Metadata)
	if result.Created {
		return PutSecret201JSONResponse{
			Body: body, Headers: PutSecret201ResponseHeaders{
				ETag: etag, Location: secretLocation(request.TenantId.String(), request.VaultId.String(), string(request.Params.Path)),
				XRequestID: requestID(ctx),
			},
		}, nil
	}
	return PutSecret200JSONResponse{
		Body: body, Headers: PutSecret200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// GetSecret returns one decrypted value with no-store cache policy.
func (s *Server) GetSecret(ctx context.Context, request GetSecretRequestObject) (GetSecretResponseObject, error) {
	if request.Params.IfNoneMatch != nil && *request.Params.IfNoneMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_none_match", "If-None-Match must not be empty.")
		return GetSecret400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	if request.Params.IfNoneMatch != nil {
		_, etag, err := s.secrets.GetMetadata(ctx, request.TenantId.String(), request.VaultId.String(), string(request.Params.Path))
		if err != nil {
			return s.getSecretError(ctx, err)
		}
		if services.IfNoneMatch(string(*request.Params.IfNoneMatch), etag) {
			return GetSecret304Response{Headers: SecretNotModifiedResponseHeaders{CacheControl: "no-store", ETag: etag, XRequestID: requestID(ctx)}}, nil
		}
	}
	secret, etag, err := s.secrets.Get(ctx, request.TenantId.String(), request.VaultId.String(), string(request.Params.Path))
	if err != nil {
		return s.getSecretError(ctx, err)
	}
	response := GetSecret200JSONResponse{
		Body: Secret{
			Path: secret.Metadata.Path, Value: secret.Value, Version: secret.Metadata.Version,
			Digest: secret.Metadata.Digest, ContentType: secret.Metadata.ContentType,
			CreatedAt: secret.Metadata.CreatedAt, UpdatedAt: secret.Metadata.UpdatedAt,
		},
		Headers: GetSecret200ResponseHeaders{
			CacheControl: "no-store", ETag: etag, XRequestID: requestID(ctx),
		},
	}
	return wipingGetSecretResponse{response: response}, nil
}

// GetSecretMetadata retrieves state without decrypting the value.
func (s *Server) GetSecretMetadata(ctx context.Context, request GetSecretMetadataRequestObject) (GetSecretMetadataResponseObject, error) {
	if request.Params.IfNoneMatch != nil && *request.Params.IfNoneMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_none_match", "If-None-Match must not be empty.")
		return GetSecretMetadata400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	metadata, etag, err := s.secrets.GetMetadata(ctx, request.TenantId.String(), request.VaultId.String(), string(request.Params.Path))
	if err != nil {
		mapped := s.problem(ctx, "getSecretMetadata", err)
		switch mapped.problem.Status {
		case 400:
			return GetSecretMetadata400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return GetSecretMetadata404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		default:
			return GetSecretMetadata500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	if request.Params.IfNoneMatch != nil && services.IfNoneMatch(string(*request.Params.IfNoneMatch), etag) {
		return GetSecretMetadata304Response{Headers: SecretNotModifiedResponseHeaders{CacheControl: "no-store", ETag: etag, XRequestID: requestID(ctx)}}, nil
	}
	return GetSecretMetadata200JSONResponse{
		Body:    secretMetadataResponse(metadata),
		Headers: GetSecretMetadata200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// ListSecrets returns metadata only from one stable vault snapshot.
func (s *Server) ListSecrets(ctx context.Context, request ListSecretsRequestObject) (ListSecretsResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	result, err := s.secrets.List(ctx, services.SecretListRequest{
		TenantID: request.TenantId.String(), VaultID: request.VaultId.String(),
		Prefix: optionalAlias(request.Params.Prefix), Limit: limit, Cursor: optionalAlias(request.Params.Cursor),
	})
	if err != nil {
		mapped := s.problem(ctx, "listSecrets", err)
		switch mapped.problem.Status {
		case 400:
			return ListSecrets400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return ListSecrets404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 410:
			return ListSecrets410ApplicationProblemPlusJSONResponse{cursorExpiredResponse(mapped.problem)}, nil
		default:
			return ListSecrets500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	items := make([]SecretMetadata, 0, len(result.Items))
	for _, metadata := range result.Items {
		items = append(items, secretMetadataResponse(metadata))
	}
	return ListSecrets200JSONResponse{
		Body:    SecretList{Items: items, Page: PageInfo{HasMore: result.HasMore, NextCursor: result.NextCursor}},
		Headers: ListSecrets200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

// DeleteSecret removes one path, optionally at a known ETag.
func (s *Server) DeleteSecret(ctx context.Context, request DeleteSecretRequestObject) (DeleteSecretResponseObject, error) {
	err := s.secrets.Delete(
		ctx, request.TenantId.String(), request.VaultId.String(), string(request.Params.Path),
		optionalAlias(request.Params.IfMatch),
	)
	if err != nil {
		mapped := s.problem(ctx, "deleteSecret", err)
		switch mapped.problem.Status {
		case 400:
			return DeleteSecret400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return DeleteSecret404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 412:
			return DeleteSecret412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		default:
			return DeleteSecret500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	return DeleteSecret204Response{Headers: DeleteSecret204ResponseHeaders{XRequestID: requestID(ctx)}}, nil
}

func (s *Server) getSecretError(ctx context.Context, err error) (GetSecretResponseObject, error) {
	mapped := s.problem(ctx, "getSecret", err)
	switch mapped.problem.Status {
	case 400:
		return GetSecret400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
	case 404:
		return GetSecret404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
	default:
		return GetSecret500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
}

func secretMetadataResponse(metadata domain.SecretMetadata) SecretMetadata {
	return SecretMetadata{
		Path: metadata.Path, Version: metadata.Version, Digest: metadata.Digest,
		ContentType: metadata.ContentType, CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt,
	}
}

func secretLocation(tenantID, vaultID, path string) string {
	query := url.Values{}
	query.Set("path", path)
	return fmt.Sprintf("/api/v1/tenants/%s/vaults/%s/secret?%s", tenantID, vaultID, query.Encode())
}

type wipingGetSecretResponse struct {
	response GetSecret200JSONResponse
}

func (response wipingGetSecretResponse) VisitGetSecretResponse(writer http.ResponseWriter) error {
	defer wipeHTTPBytes(response.response.Body.Value)
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(response.response.Body); err != nil {
		return err
	}
	encoded := buffer.Bytes()
	defer wipeHTTPBytes(encoded)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", response.response.Headers.CacheControl)
	writer.Header().Set("ETag", response.response.Headers.ETag)
	writer.Header().Set("X-Request-ID", response.response.Headers.XRequestID)
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write(encoded)
	return err
}

func wipeHTTPBytes(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}
