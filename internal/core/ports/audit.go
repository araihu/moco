package ports

import (
	"context"
	"time"
)

// AuditEvent is a durable record of one API request. It intentionally stores
// no request body, query string, bearer credential, or plaintext secret path.
type AuditEvent struct {
	Sequence         int64
	OccurredAt       time.Time
	RequestID        string
	PrincipalID      *string
	Method           string
	Route            string
	StatusCode       int
	Outcome          string
	SecretPathSHA256 *string
}

// ListAuditEventsQuery selects events after one exclusive sequence checkpoint.
type ListAuditEventsQuery struct {
	AfterSequence int64
	PageSize      int
}

// AuditRepository persists and reads the append-only audit ledger.
type AuditRepository interface {
	AppendAuditEvent(context.Context, AuditEvent) (AuditEvent, error)
	ListAuditEvents(context.Context, ListAuditEventsQuery) ([]AuditEvent, error)
}
