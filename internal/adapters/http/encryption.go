package httpapi

import (
	"context"
	"strings"

	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/services"
)

// RotateEncryptionKeys performs one bounded, retry-safe root-key rewrap page.
// Deployment key material is loaded before the server starts and is never
// accepted through this request.
func (s *Server) RotateEncryptionKeys(ctx context.Context, request internalapi.RotateEncryptionKeysRequestObject) (internalapi.RotateEncryptionKeysResponseObject, error) {
	if s.keyRotation == nil {
		return internalapi.RotateEncryptionKeys503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: internalapi.ServiceUnavailableApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(serviceUnavailableProblem(requestID(ctx))),
				Headers: internalapi.ServiceUnavailableResponseHeaders{RetryAfter: 1, XRequestID: requestID(ctx)},
			},
		}, nil
	}
	requestValue := services.VaultKeyRotationRequest{}
	if request.Params.AfterTenantId != nil {
		requestValue.AfterTenantID = string(*request.Params.AfterTenantId)
	}
	if request.Params.AfterVaultId != nil {
		requestValue.AfterVaultID = string(*request.Params.AfterVaultId)
	}
	if request.Params.Limit != nil {
		requestValue.Limit = int(*request.Params.Limit)
	}
	if (requestValue.AfterTenantID == "") != (requestValue.AfterVaultID == "") {
		return rotateEncryptionBadRequest(ctx, "invalid_rotation_checkpoint", "afterTenantId and afterVaultId must be supplied together."), nil
	}
	if len(requestValue.AfterTenantID) > 128 || len(requestValue.AfterVaultID) > 128 {
		return rotateEncryptionBadRequest(ctx, "invalid_rotation_checkpoint", "rotation checkpoints must contain at most 128 characters."), nil
	}
	if requestValue.Limit < 0 || requestValue.Limit > services.MaxVaultKeyRotationPageSize {
		return rotateEncryptionBadRequest(ctx, "invalid_limit", "limit must be between 1 and 200; omit it for the default page size."), nil
	}
	result, err := s.keyRotation.Rotate(ctx, requestValue)
	if err != nil {
		mapped := s.problem(ctx, "rotateEncryptionKeys", err)
		if mapped.problem.Status == 400 {
			return rotateEncryptionBadRequest(ctx, "invalid_rotation_request", "the rotation request is invalid."), nil
		}
		if mapped.problem.Status == 409 {
			return internalapi.RotateEncryptionKeys409ApplicationProblemPlusJSONResponse{
				ConflictApplicationProblemPlusJSONResponse: internalapi.ConflictApplicationProblemPlusJSONResponse{
					Body:    internalProblemBody(mapped.problem),
					Headers: internalapi.ConflictResponseHeaders{XRequestID: requestID(ctx)},
				},
			}, nil
		}
		return internalapi.RotateEncryptionKeys500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	return internalapi.RotateEncryptionKeys200JSONResponse{
		Body: internalapi.EncryptionRotationResult{
			ActiveRootKeyEpoch: result.ActiveRootKeyEpoch,
			ActiveRootKeyId:    result.ActiveRootKeyID,
			Complete:           result.Complete,
			Scanned:            rotationCount(result.Scanned),
			Rewrapped:          rotationCount(result.Rewrapped),
			Skipped:            rotationCount(result.Skipped),
			HasMore:            result.HasMore,
			NextAfterTenantId:  result.NextTenantID,
			NextAfterVaultId:   result.NextVaultID,
			RemainingOldKeys:   result.RemainingOldKeys,
		},
		Headers: internalapi.RotateEncryptionKeys200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

func rotationCount(value int) int32 {
	// #nosec G115 -- the rotation service caps every batch count at 200.
	return int32(value)
}

func rotateEncryptionBadRequest(ctx context.Context, code, detail string) internalapi.RotateEncryptionKeys400ApplicationProblemPlusJSONResponse {
	return internalapi.RotateEncryptionKeys400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: internalapi.BadRequestApplicationProblemPlusJSONResponse{
			Body:    internalProblemBody(badRequestProblem(requestID(ctx), code, strings.TrimSpace(detail))),
			Headers: internalapi.BadRequestResponseHeaders{XRequestID: requestID(ctx)},
		},
	}
}
