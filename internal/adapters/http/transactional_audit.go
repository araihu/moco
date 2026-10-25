package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

const auditAttemptContextKey contextKey = "moco-audit-attempt"
const mutationTimeout = 20 * time.Second
const maxMutationResponseBytes = 4 << 20

type auditAttempt struct{ handled bool }

var errMutationRejected = errors.New("mutation returned an unsuccessful response")

func isMutation(r *http.Request) bool {
	return isProtectedAPIPath(r.URL.Path) && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

// transactionalAudit runs after authentication and authorization. Success bytes
// and headers never reach the client until the mutation and its audit commit.
func transactionalAudit(audit *services.AuditService, pathKey []byte, logger *slog.Logger, observe func(error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutation(r) {
			next.ServeHTTP(w, r)
			return
		}
		buffer := &mutationResponse{header: w.Header().Clone()}
		ctx, cancel := context.WithTimeout(r.Context(), mutationTimeout)
		defer cancel()
		err := services.ErrTransactionalAuditUnavailable
		if audit != nil {
			err = audit.RecordMutation(ctx, func(transactionContext context.Context) (ports.AuditEvent, error) {
				next.ServeHTTP(buffer, r.WithContext(transactionContext))
				if buffer.err != nil {
					return ports.AuditEvent{}, buffer.err
				}
				if buffer.statusCode() < 200 || buffer.statusCode() >= 300 {
					return ports.AuditEvent{}, errMutationRejected
				}
				return requestAuditEvent(r, buffer.statusCode(), pathKey), nil
			})
		}
		if errors.Is(err, errMutationRejected) {
			// The transaction has rolled back. The outer attempt logger records
			// the original rejection independently, without a success event.
			buffer.publish(w)
			return
		}
		if attempt, ok := r.Context().Value(auditAttemptContextKey).(*auditAttempt); ok {
			attempt.handled = true
		}
		var notificationError *ports.PostCommitError
		if err == nil || errors.As(err, &notificationError) {
			if notificationError != nil {
				logger.ErrorContext(r.Context(), "committed authorization notification failed", "requestId", requestID(r.Context()), "error", err)
			}
			buffer.publish(w)
			return
		}
		if observe != nil {
			observe(err)
		}
		logger.ErrorContext(r.Context(), "audited mutation aborted", "requestId", requestID(r.Context()), "error", err)
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Cache-Control", "no-store")
		writeProblem(w, serviceUnavailableProblem(requestID(r.Context())))
	})
}

// A bounded non-streaming response is appropriate for all mutation endpoints.
// Watch/read responses keep their existing streaming behavior and writer APIs.
type mutationResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
	err    error
}

func (b *mutationResponse) Header() http.Header { return b.header }
func (b *mutationResponse) WriteHeader(status int) {
	if status < 200 {
		b.err = errors.New("informational mutation responses are unsupported")
		return
	}
	if b.status != 0 {
		if status >= 400 && b.status < 400 {
			b.err = errors.New("mutation response encoding failed")
		}
		return
	}
	b.status = status
}
func (b *mutationResponse) Write(payload []byte) (int, error) {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	if b.err != nil {
		return 0, b.err
	}
	if len(payload) > maxMutationResponseBytes-b.body.Len() {
		b.err = errors.New("mutation response exceeds its buffer limit")
		return 0, b.err
	}
	return b.body.Write(payload)
}
func (b *mutationResponse) statusCode() int {
	if b.status == 0 {
		return http.StatusOK
	}
	return b.status
}
func (b *mutationResponse) publish(w http.ResponseWriter) {
	for name := range w.Header() {
		w.Header().Del(name)
	}
	for name, values := range b.header {
		w.Header()[name] = values
	}
	w.WriteHeader(b.statusCode())
	_, _ = io.Copy(w, &b.body)
}
