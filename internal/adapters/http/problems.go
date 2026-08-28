package httpapi

import (
	"errors"
	"fmt"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

type mappedProblem struct {
	problem Problem
	etag    string
}

func mapProblem(requestID string, err error) mappedProblem {
	if errors.Is(err, domain.ErrSecretTooLarge) {
		return mappedProblem{problem: secretTooLargeProblem(requestID)}
	}
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		violations := make([]ProblemError, 0, len(validation.Violations))
		for _, violation := range validation.Violations {
			code := violation.Code
			field := violation.Field
			violations = append(violations, ProblemError{
				Code: &code, Field: &field, Message: violation.Message,
			})
		}
		detail := "One or more request fields are invalid."
		problem := newProblem(requestID, 400, "Bad Request", "validation_failed", "bad-request", &detail)
		problem.Errors = &violations
		return mappedProblem{problem: problem}
	}
	if errors.Is(err, services.ErrInvalidCursor) {
		detail := "The cursor is invalid for this request."
		return mappedProblem{problem: newProblem(requestID, 400, "Bad Request", "invalid_cursor", "bad-request", &detail)}
	}
	if errors.Is(err, services.ErrCursorExpired) {
		detail := "The cursor snapshot expired; restart listing without a cursor."
		return mappedProblem{problem: newProblem(requestID, 410, "Cursor Expired", "cursor_expired", "cursor-expired", &detail)}
	}
	if errors.Is(err, ports.ErrTenantNotFound) {
		detail := "The requested tenant does not exist."
		return mappedProblem{problem: newProblem(requestID, 404, "Not Found", "resource_not_found", "not-found", &detail)}
	}
	if errors.Is(err, ports.ErrVaultNotFound) {
		detail := "The requested vault does not exist in this tenant."
		return mappedProblem{problem: newProblem(requestID, 404, "Not Found", "resource_not_found", "not-found", &detail)}
	}
	if errors.Is(err, ports.ErrSecretNotFound) {
		detail := "The requested secret does not exist in this vault."
		return mappedProblem{problem: newProblem(requestID, 404, "Not Found", "resource_not_found", "not-found", &detail)}
	}
	if errors.Is(err, ports.ErrIdempotencyConflict) {
		detail := "The idempotency key was already used with a different request."
		return mappedProblem{problem: newProblem(requestID, 409, "Conflict", "idempotency_key_conflict", "conflict", &detail)}
	}
	var conflict *ports.TenantConflictError
	if errors.As(err, &conflict) {
		detail := "A tenant with the same unique identity already exists."
		problem := newProblem(requestID, 409, "Conflict", "resource_conflict", "conflict", &detail)
		problem.ResourceId = &conflict.ResourceID
		return mappedProblem{problem: problem}
	}
	var vaultConflict *ports.VaultConflictError
	if errors.As(err, &vaultConflict) {
		detail := "A vault with the same tenant-scoped unique identity already exists."
		problem := newProblem(requestID, 409, "Conflict", "resource_conflict", "conflict", &detail)
		problem.ResourceId = &vaultConflict.ResourceID
		return mappedProblem{problem: problem}
	}
	if errors.Is(err, ports.ErrResourceHasChildren) {
		detail := "The resource has children; retry with cascade=true to delete them."
		return mappedProblem{problem: newProblem(requestID, 409, "Conflict", "resource_has_children", "conflict", &detail)}
	}
	var precondition *services.PreconditionError
	if errors.As(err, &precondition) {
		detail := "The supplied ETag does not match the current resource revision."
		return mappedProblem{
			problem: newProblem(requestID, 412, "Precondition Failed", "etag_mismatch", "precondition-failed", &detail),
			etag:    precondition.CurrentETag,
		}
	}
	return mappedProblem{problem: internalProblem(requestID)}
}

func secretTooLargeProblem(requestID string) Problem {
	detail := "The secret request exceeds the supported size limit."
	return newProblem(requestID, 413, "Payload Too Large", "secret_too_large", "payload-too-large", &detail)
}

func badRequestProblem(requestID, code, detail string) Problem {
	return newProblem(requestID, 400, "Bad Request", code, "bad-request", &detail)
}

func unauthorizedProblem(requestID string) Problem {
	detail := "A valid bearer credential is required."
	return newProblem(requestID, 401, "Unauthorized", "unauthorized", "unauthorized", &detail)
}

func internalProblem(requestID string) Problem {
	detail := "The request could not be completed."
	return newProblem(requestID, 500, "Internal Server Error", "internal_error", "internal-error", &detail)
}

func newProblem(requestID string, status int32, title, code, problemType string, detail *string) Problem {
	instance := "urn:moco:request:" + requestID
	return Problem{
		Type:      fmt.Sprintf("https://moco.araihu.com/problems/%s", problemType),
		Title:     title,
		Status:    status,
		Code:      code,
		Detail:    detail,
		Instance:  &instance,
		RequestId: requestID,
	}
}

func badRequestResponse(problem Problem) BadRequestApplicationProblemPlusJSONResponse {
	return BadRequestApplicationProblemPlusJSONResponse{
		Body: problem, Headers: BadRequestResponseHeaders{XRequestID: problem.RequestId},
	}
}

func conflictResponse(problem Problem) ConflictApplicationProblemPlusJSONResponse {
	return ConflictApplicationProblemPlusJSONResponse{
		Body: problem, Headers: ConflictResponseHeaders{XRequestID: problem.RequestId},
	}
}

func cursorExpiredResponse(problem Problem) CursorExpiredApplicationProblemPlusJSONResponse {
	return CursorExpiredApplicationProblemPlusJSONResponse{
		Body: problem, Headers: CursorExpiredResponseHeaders{XRequestID: problem.RequestId},
	}
}

func notFoundResponse(problem Problem) NotFoundApplicationProblemPlusJSONResponse {
	return NotFoundApplicationProblemPlusJSONResponse{
		Body: problem, Headers: NotFoundResponseHeaders{XRequestID: problem.RequestId},
	}
}

func preconditionResponse(problem Problem, etag string) PreconditionFailedApplicationProblemPlusJSONResponse {
	return PreconditionFailedApplicationProblemPlusJSONResponse{
		Body: problem, Headers: PreconditionFailedResponseHeaders{ETag: etag, XRequestID: problem.RequestId},
	}
}

func payloadTooLargeResponse(problem Problem) PayloadTooLargeApplicationProblemPlusJSONResponse {
	return PayloadTooLargeApplicationProblemPlusJSONResponse{
		Body: problem, Headers: PayloadTooLargeResponseHeaders{XRequestID: problem.RequestId},
	}
}

func internalResponse(problem Problem) InternalErrorApplicationProblemPlusJSONResponse {
	return InternalErrorApplicationProblemPlusJSONResponse{
		Body: problem, Headers: InternalErrorResponseHeaders{XRequestID: problem.RequestId},
	}
}
