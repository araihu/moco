package httpapi

import (
	"context"
	"strings"
	"time"

	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/services"
)

// PurgeAuditEvents performs one bounded local audit-retention batch. It never
// accepts or returns event contents, credentials, paths, or encryption keys.
func (s *Server) PurgeAuditEvents(ctx context.Context, request internalapi.PurgeAuditEventsRequestObject) (internalapi.PurgeAuditEventsResponseObject, error) {
	if s.auditRetention == nil {
		return internalapi.PurgeAuditEvents503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: internalapi.ServiceUnavailableApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(serviceUnavailableProblem(requestID(ctx))),
				Headers: internalapi.ServiceUnavailableResponseHeaders{RetryAfter: 1, XRequestID: requestID(ctx)},
			},
		}, nil
	}
	if request.Params.Before.IsZero() {
		return purgeAuditEventsBadRequest(ctx, "invalid_audit_cutoff", "before must be a valid date-time."), nil
	}
	now := time.Now().UTC()
	if request.Params.Before.After(now) {
		return purgeAuditEventsBadRequest(ctx, "invalid_audit_cutoff", "before must not be in the future."), nil
	}
	dryRun := request.Params.DryRun != nil && *request.Params.DryRun
	if !dryRun && request.Params.Before.After(now.Add(-s.auditRetention.MinimumAge())) {
		return purgeAuditEventsBadRequest(ctx, "audit_cutoff_too_recent", "before is too recent for the configured retention safety buffer."), nil
	}
	limit := services.DefaultAuditPageSize
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
		if limit < 1 || limit > services.MaxAuditPageSize {
			return purgeAuditEventsBadRequest(ctx, "invalid_limit", "limit must be between 1 and 200."), nil
		}
	}
	result, err := s.auditRetention.Purge(ctx, services.AuditRetentionRequest{Before: request.Params.Before, Limit: limit, DryRun: dryRun})
	if err != nil {
		mapped := s.problem(ctx, "purgeAuditEvents", err)
		return internalapi.PurgeAuditEvents500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	return internalapi.PurgeAuditEvents200JSONResponse{
		Body: internalapi.AuditRetentionResult{
			Before:    result.Before,
			Deleted:   retentionCount(result.Deleted),
			Remaining: result.Remaining,
			HasMore:   result.HasMore,
			Complete:  result.Complete,
			DryRun:    result.DryRun,
		},
		Headers: internalapi.PurgeAuditEvents200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

func retentionCount(value int) int32 {
	// #nosec G115 -- the retention service caps every batch count at 200.
	return int32(value)
}

func purgeAuditEventsBadRequest(ctx context.Context, code, detail string) internalapi.PurgeAuditEvents400ApplicationProblemPlusJSONResponse {
	return internalapi.PurgeAuditEvents400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: internalapi.BadRequestApplicationProblemPlusJSONResponse{
			Body:    internalProblemBody(badRequestProblem(requestID(ctx), code, strings.TrimSpace(detail))),
			Headers: internalapi.BadRequestResponseHeaders{XRequestID: requestID(ctx)},
		},
	}
}
