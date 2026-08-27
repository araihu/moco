package httpapi

import (
	"context"
	"fmt"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/services"
	"github.com/google/uuid"
)

// GetServiceInfo advertises only capabilities implemented by this binary.
func (s *Server) GetServiceInfo(ctx context.Context, _ GetServiceInfoRequestObject) (GetServiceInfoResponseObject, error) {
	id := requestID(ctx)
	return GetServiceInfo200JSONResponse{
		Body: ServiceInfo{
			ApiVersion:     V1,
			ServiceVersion: s.serviceVersion,
			Capabilities:   []string{"tenants", "conditional-writes"},
		},
		Headers: GetServiceInfo200ResponseHeaders{XRequestID: id},
	}, nil
}

// ListTenants returns a stable tenant snapshot page.
func (s *Server) ListTenants(ctx context.Context, request ListTenantsRequestObject) (ListTenantsResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	cursor := optionalAlias(request.Params.Cursor)
	name := optionalAlias(request.Params.Name)
	externalID := optionalAlias(request.Params.ExternalId)
	result, err := s.tenants.List(ctx, services.TenantListRequest{
		Limit: limit, Cursor: cursor, Name: name, ExternalID: externalID,
	})
	if err != nil {
		mapped := s.problem(ctx, "listTenants", err)
		switch mapped.problem.Status {
		case 400:
			return ListTenants400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 410:
			return ListTenants410ApplicationProblemPlusJSONResponse{cursorExpiredResponse(mapped.problem)}, nil
		default:
			return ListTenants500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	items := make([]Tenant, 0, len(result.Items))
	for _, tenant := range result.Items {
		converted, err := tenantResponse(tenant)
		if err != nil {
			mapped := s.problem(ctx, "listTenants", err)
			return ListTenants500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
		items = append(items, converted)
	}
	return ListTenants200JSONResponse{
		Body: TenantList{
			Items: items,
			Page:  PageInfo{HasMore: result.HasMore, NextCursor: result.NextCursor},
		},
		Headers: ListTenants200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

// CreateTenant creates or idempotently replays a tenant.
func (s *Server) CreateTenant(ctx context.Context, request CreateTenantRequestObject) (CreateTenantResponseObject, error) {
	if request.Body == nil {
		problem := badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")
		return CreateTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	labels := map[string]string{}
	if request.Body.Labels != nil {
		labels = domain.CloneLabels(map[string]string(*request.Body.Labels))
	}
	key := optionalAlias(request.Params.IdempotencyKey)
	result, err := s.tenants.Create(ctx, principalID(ctx), domain.TenantCreate{
		Name:        request.Body.Name,
		Description: request.Body.Description,
		ExternalID:  request.Body.ExternalId,
		Labels:      labels,
	}, key)
	if err != nil {
		mapped := s.problem(ctx, "createTenant", err)
		switch mapped.problem.Status {
		case 400:
			return CreateTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 409:
			return CreateTenant409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		default:
			return CreateTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	tenant, err := tenantResponse(result.Tenant)
	if err != nil {
		mapped := s.problem(ctx, "createTenant", err)
		return CreateTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	return CreateTenant201JSONResponse{
		Body: tenant,
		Headers: CreateTenant201ResponseHeaders{
			ETag: result.ETag, Location: "/api/v1/tenants/" + result.Tenant.ID, XRequestID: requestID(ctx),
		},
	}, nil
}

// GetTenant retrieves one tenant and honors If-None-Match.
func (s *Server) GetTenant(ctx context.Context, request GetTenantRequestObject) (GetTenantResponseObject, error) {
	if request.Params.IfNoneMatch != nil && *request.Params.IfNoneMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_none_match", "If-None-Match must not be empty.")
		return GetTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	tenant, etag, err := s.tenants.Get(ctx, request.TenantId.String())
	if err != nil {
		mapped := s.problem(ctx, "getTenant", err)
		if mapped.problem.Status == 404 {
			return GetTenant404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		}
		return GetTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	if request.Params.IfNoneMatch != nil && services.IfNoneMatch(string(*request.Params.IfNoneMatch), etag) {
		return GetTenant304Response{Headers: NotModifiedResponseHeaders{
			ETag: etag, XRequestID: requestID(ctx),
		}}, nil
	}
	response, err := tenantResponse(tenant)
	if err != nil {
		mapped := s.problem(ctx, "getTenant", err)
		return GetTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	return GetTenant200JSONResponse{
		Body:    response,
		Headers: GetTenant200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// UpdateTenant replaces a tenant's complete mutable state.
func (s *Server) UpdateTenant(ctx context.Context, request UpdateTenantRequestObject) (UpdateTenantResponseObject, error) {
	if request.Body == nil {
		problem := badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")
		return UpdateTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	if request.Params.IfMatch != nil && *request.Params.IfMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_match", "If-Match must not be empty.")
		return UpdateTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	ifMatch := optionalAlias(request.Params.IfMatch)
	tenant, etag, err := s.tenants.Update(ctx, request.TenantId.String(), domain.TenantUpdate{
		Name: request.Body.Name, Description: request.Body.Description,
		Labels: domain.CloneLabels(map[string]string(request.Body.Labels)),
	}, ifMatch)
	if err != nil {
		mapped := s.problem(ctx, "updateTenant", err)
		switch mapped.problem.Status {
		case 400:
			return UpdateTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(mapped.problem)}, nil
		case 404:
			return UpdateTenant404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 409:
			return UpdateTenant409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		case 412:
			return UpdateTenant412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		default:
			return UpdateTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	response, err := tenantResponse(tenant)
	if err != nil {
		mapped := s.problem(ctx, "updateTenant", err)
		return UpdateTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
	}
	return UpdateTenant200JSONResponse{
		Body:    response,
		Headers: UpdateTenant200ResponseHeaders{ETag: etag, XRequestID: requestID(ctx)},
	}, nil
}

// DeleteTenant removes one currently empty tenant.
func (s *Server) DeleteTenant(ctx context.Context, request DeleteTenantRequestObject) (DeleteTenantResponseObject, error) {
	if request.Params.IfMatch != nil && *request.Params.IfMatch == "" {
		problem := badRequestProblem(requestID(ctx), "invalid_if_match", "If-Match must not be empty.")
		return DeleteTenant400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	ifMatch := optionalAlias(request.Params.IfMatch)
	cascade := request.Params.Cascade != nil && bool(*request.Params.Cascade)
	if err := s.tenants.Delete(ctx, request.TenantId.String(), ifMatch, cascade); err != nil {
		mapped := s.problem(ctx, "deleteTenant", err)
		switch mapped.problem.Status {
		case 404:
			return DeleteTenant404ApplicationProblemPlusJSONResponse{notFoundResponse(mapped.problem)}, nil
		case 409:
			return DeleteTenant409ApplicationProblemPlusJSONResponse{conflictResponse(mapped.problem)}, nil
		case 412:
			return DeleteTenant412ApplicationProblemPlusJSONResponse{preconditionResponse(mapped.problem, mapped.etag)}, nil
		default:
			return DeleteTenant500ApplicationProblemPlusJSONResponse{internalResponse(mapped.problem)}, nil
		}
	}
	return DeleteTenant204Response{Headers: DeleteTenant204ResponseHeaders{XRequestID: requestID(ctx)}}, nil
}

func (s *Server) problem(ctx context.Context, operation string, err error) mappedProblem {
	mapped := mapProblem(requestID(ctx), err)
	if mapped.problem.Status == 500 {
		s.logger.ErrorContext(ctx, "request failed", "operation", operation,
			"requestId", mapped.problem.RequestId, "error", err)
	}
	return mapped
}

func tenantResponse(tenant domain.Tenant) (Tenant, error) {
	id, err := uuid.Parse(tenant.ID)
	if err != nil {
		return Tenant{}, fmt.Errorf("parse persisted tenant ID: %w", err)
	}
	return Tenant{
		Id:          id,
		Name:        tenant.Name,
		Description: tenant.Description,
		ExternalId:  tenant.ExternalID,
		Labels:      Labels(domain.CloneLabels(tenant.Labels)),
		Revision:    tenant.Revision,
		CreatedAt:   tenant.CreatedAt,
		UpdatedAt:   tenant.UpdatedAt,
	}, nil
}

func optionalAlias[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	converted := string(*value)
	return &converted
}
