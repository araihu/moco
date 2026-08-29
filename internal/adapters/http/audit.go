package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

const auditWriteTimeout = 500 * time.Millisecond

// ListAuditEvents returns request metadata from the restricted internal ledger.
func (s *Server) ListAuditEvents(ctx context.Context, request internalapi.ListAuditEventsRequestObject) (internalapi.ListAuditEventsResponseObject, error) {
	if s.audit == nil {
		return internalapi.ListAuditEvents503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: internalapi.ServiceUnavailableApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(serviceUnavailableProblem(requestID(ctx))),
				Headers: internalapi.ServiceUnavailableResponseHeaders{RetryAfter: 1, XRequestID: requestID(ctx)},
			},
		}, nil
	}
	afterSequence := int64(0)
	if request.Params.AfterSequence != nil {
		afterSequence = int64(*request.Params.AfterSequence)
		if afterSequence < 0 {
			return listAuditEventsBadRequest(ctx, "invalid_audit_sequence", "afterSequence must not be negative."), nil
		}
	}
	pageSize := int64(services.DefaultAuditPageSize)
	if request.Params.Limit != nil {
		pageSize = int64(*request.Params.Limit)
		if pageSize < 1 || pageSize > services.MaxAuditPageSize {
			return listAuditEventsBadRequest(ctx, "invalid_limit", "limit must be between 1 and 200."), nil
		}
	}
	page, err := s.audit.List(ctx, afterSequence, pageSize)
	if err != nil {
		mapped := s.problem(ctx, "listAuditEvents", err)
		return internalapi.ListAuditEvents500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	items := make([]internalapi.AuditEvent, 0, len(page.Items))
	for _, event := range page.Items {
		items = append(items, auditEventResponse(event))
	}
	return internalapi.ListAuditEvents200JSONResponse{
		Body: internalapi.AuditEventList{
			Items: items, HasMore: page.HasMore, NextAfterSequence: page.NextAfterSequence,
		},
		Headers: internalapi.ListAuditEvents200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

func listAuditEventsBadRequest(ctx context.Context, code, detail string) internalapi.ListAuditEvents400ApplicationProblemPlusJSONResponse {
	return internalapi.ListAuditEvents400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: internalapi.BadRequestApplicationProblemPlusJSONResponse{
			Body:    internalProblemBody(badRequestProblem(requestID(ctx), code, detail)),
			Headers: internalapi.BadRequestResponseHeaders{XRequestID: requestID(ctx)},
		},
	}
}

func auditEventResponse(event ports.AuditEvent) internalapi.AuditEvent {
	statusCode := int32(500)
	if event.StatusCode >= 100 && event.StatusCode <= 599 {
		statusCode = int32(event.StatusCode)
	}
	response := internalapi.AuditEvent{
		Sequence: event.Sequence, OccurredAt: event.OccurredAt, RequestId: event.RequestID,
		Method: event.Method, Route: event.Route, StatusCode: statusCode,
		Outcome: internalapi.AuditEventOutcome(event.Outcome),
	}
	if event.PrincipalID != nil {
		value := *event.PrincipalID
		response.PrincipalId = &value
	}
	if event.SecretPathSHA256 != nil {
		value := *event.SecretPathSHA256
		response.SecretPathSha256 = &value
	}
	return response
}

func auditMiddleware(audit *services.AuditService, pathHMACKey []byte, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProtectedAPIPath(r.URL.Path) || r.URL.Path == auditPath {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &auditResponseWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		statusCode := recorder.statusCode()
		event := ports.AuditEvent{
			OccurredAt: time.Now().UTC(), RequestID: requestID(r.Context()),
			PrincipalID: optionalAuditString(principalID(r.Context())), Method: r.Method,
			Route: r.URL.Path, StatusCode: statusCode, Outcome: auditOutcome(statusCode),
			SecretPathSHA256: auditSecretPathDigest(r, pathHMACKey),
		}
		writeContext, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
		defer cancel()
		if _, err := audit.Record(writeContext, event); err != nil {
			logger.ErrorContext(r.Context(), "audit event failed", "requestId", event.RequestID, "error", err)
		}
	})
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(payload)
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *auditResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *auditResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func auditOutcome(statusCode int) string {
	if statusCode >= 200 && statusCode < 400 {
		return "success"
	}
	return "failure"
}

func optionalAuditString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func auditSecretPathDigest(r *http.Request, pathHMACKey []byte) *string {
	var value string
	switch {
	case strings.HasSuffix(r.URL.Path, "/secret"), strings.HasSuffix(r.URL.Path, "/secret/metadata"):
		value = singleQueryValue(r, "path")
	case strings.HasSuffix(r.URL.Path, "/secrets"):
		value = singleQueryValue(r, "prefix")
	}
	if value == "" {
		return nil
	}
	mac := hmac.New(sha256.New, pathHMACKey)
	_, _ = mac.Write([]byte(value))
	encoded := hex.EncodeToString(mac.Sum(nil))
	return &encoded
}
