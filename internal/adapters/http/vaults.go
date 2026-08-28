package httpapi

import (
	"context"
	"fmt"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/services"
	"github.com/google/uuid"
)

// ListVaults returns one stable tenant-scoped vault page.
func (s *Server) ListVaults(ctx context.Context, request ListVaultsRequestObject) (ListVaultsResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	result, err := s.vaults.List(ctx, services.VaultListRequest{
		TenantID: request.TenantId.String(), Limit: limit,
		Cursor: optionalAlias(request.Params.Cursor), Name: optionalAlias(request.Params.Name),
		ExternalID: optionalAlias(request.Params.ExternalId),
	})
	if err != nil {
		mapped := s.problem(ctx, "listVaults", err)
		switch mapped.problem.Status {
		case 400:
			return ListVaults400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return ListVaults404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 410:
			return ListVaults410ApplicationProblemPlusJSONResponse{cursorExpiredResponse(mapped.problem)}, nil
		default:
			return ListVaults500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	items := make([]Vault, 0, len(result.Items))
	for _, vault := range result.Items {
		converted, err := vaultResponse(vault)
		if err != nil {
			mapped := s.problem(ctx, "listVaults", err)
			return ListVaults500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
		items = append(items, converted)
	}
	return ListVaults200JSONResponse{
		Body:    VaultList{Items: items, Page: PageInfo{HasMore: result.HasMore, NextCursor: result.NextCursor}},
		Headers: ListVaults200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

// CreateVault creates or idempotently replays a tenant-scoped vault.
func (s *Server) CreateVault(ctx context.Context, request CreateVaultRequestObject) (CreateVaultResponseObject, error) {
	if request.Body == nil {
		problem := badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")
		return CreateVault400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	labels := map[string]string{}
	if request.Body.Labels != nil {
		labels = domain.CloneLabels(map[string]string(*request.Body.Labels))
	}
	result, err := s.vaults.Create(ctx, principalID(ctx), request.TenantId.String(), domain.VaultCreate{
		Name: request.Body.Name, Description: request.Body.Description,
		ExternalID: request.Body.ExternalId, Labels: labels,
	}, optionalAlias(request.Params.IdempotencyKey))
	if err != nil {
		mapped := s.problem(ctx, "createVault", err)
		switch mapped.problem.Status {
		case 400:
			return CreateVault400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return CreateVault404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 409:
			return CreateVault409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		default:
			return CreateVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	vault, err := vaultResponse(result.Vault)
	if err != nil {
		mapped := s.problem(ctx, "createVault", err)
		return CreateVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	location := fmt.Sprintf("/api/v1/tenants/%s/vaults/%s", result.Vault.TenantID, result.Vault.ID)
	return CreateVault201JSONResponse{
		Body: vault,
		Headers: CreateVault201ResponseHeaders{
			ETag: result.ETag, Location: location, XRequestID: requestID(ctx),
		},
	}, nil
}

// GetVault retrieves one vault and honors If-None-Match.
func (s *Server) GetVault(ctx context.Context, request GetVaultRequestObject) (GetVaultResponseObject, error) {
	if request.Params.IfNoneMatch != nil && *request.Params.IfNoneMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_none_match", "If-None-Match must not be empty.")
		return GetVault400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	vault, etag, err := s.vaults.Get(ctx, request.TenantId.String(), request.VaultId.String())
	if err != nil {
		mapped := s.problem(ctx, "getVault", err)
		if mapped.problem.Status == 404 {
			return GetVault404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		}
		return GetVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	if request.Params.IfNoneMatch != nil && services.IfNoneMatch(string(*request.Params.IfNoneMatch), etag) {
		return GetVault304Response{Headers: NotModifiedResponseHeaders{ETag: etag, XRequestID: requestID(ctx)}}, nil
	}
	response, err := vaultResponse(vault)
	if err != nil {
		mapped := s.problem(ctx, "getVault", err)
		return GetVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	return GetVault200JSONResponse{
		Body: response, Headers: GetVault200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// UpdateVault replaces all mutable vault state.
func (s *Server) UpdateVault(ctx context.Context, request UpdateVaultRequestObject) (UpdateVaultResponseObject, error) {
	if request.Body == nil {
		problem := badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")
		return UpdateVault400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	if request.Params.IfMatch != nil && *request.Params.IfMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_match", "If-Match must not be empty.")
		return UpdateVault400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	vault, etag, err := s.vaults.Update(ctx, request.TenantId.String(), request.VaultId.String(), domain.VaultUpdate{
		Name: request.Body.Name, Description: request.Body.Description,
		Labels: domain.CloneLabels(map[string]string(request.Body.Labels)),
	}, optionalAlias(request.Params.IfMatch))
	if err != nil {
		mapped := s.problem(ctx, "updateVault", err)
		switch mapped.problem.Status {
		case 400:
			return UpdateVault400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return UpdateVault404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 409:
			return UpdateVault409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		case 412:
			return UpdateVault412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		default:
			return UpdateVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	response, err := vaultResponse(vault)
	if err != nil {
		mapped := s.problem(ctx, "updateVault", err)
		return UpdateVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	return UpdateVault200JSONResponse{
		Body: response, Headers: UpdateVault200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// DeleteVault removes one currently empty vault.
func (s *Server) DeleteVault(ctx context.Context, request DeleteVaultRequestObject) (DeleteVaultResponseObject, error) {
	if request.Params.IfMatch != nil && *request.Params.IfMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_match", "If-Match must not be empty.")
		return DeleteVault400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	cascade := request.Params.Cascade != nil && bool(*request.Params.Cascade)
	err := s.vaults.Delete(ctx, request.TenantId.String(), request.VaultId.String(), optionalAlias(request.Params.IfMatch), cascade)
	if err != nil {
		mapped := s.problem(ctx, "deleteVault", err)
		switch mapped.problem.Status {
		case 404:
			return DeleteVault404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 409:
			return DeleteVault409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		case 412:
			return DeleteVault412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		default:
			return DeleteVault500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	return DeleteVault204Response{Headers: DeleteVault204ResponseHeaders{XRequestID: requestID(ctx)}}, nil
}

func vaultResponse(vault domain.Vault) (Vault, error) {
	id, err := uuid.Parse(vault.ID)
	if err != nil {
		return Vault{}, fmt.Errorf("parse persisted vault ID: %w", err)
	}
	tenantID, err := uuid.Parse(vault.TenantID)
	if err != nil {
		return Vault{}, fmt.Errorf("parse persisted vault tenant ID: %w", err)
	}
	return Vault{
		Id: id, TenantId: tenantID, Name: vault.Name,
		Description: vault.Description, ExternalId: vault.ExternalID,
		Labels: Labels(domain.CloneLabels(vault.Labels)), Revision: vault.Revision,
		CreatedAt: vault.CreatedAt, UpdatedAt: vault.UpdatedAt,
	}, nil
}
