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

// PurgeAuditEventsQuery selects one bounded, age-based retention batch.
// Records strictly before Before are deleted in sequence order.
type PurgeAuditEventsQuery struct {
	Before   time.Time
	PageSize int
}

// AuditRepository persists and reads the append-only audit ledger.
type AuditRepository interface {
	AppendAuditEvent(context.Context, AuditEvent) (AuditEvent, error)
	ListAuditEvents(context.Context, ListAuditEventsQuery) ([]AuditEvent, error)
}

// AuditExportRepository provides the read-only sequence boundary needed to
// export a finite ledger snapshot. It is deliberately separate from the
// append and retention capabilities.
type AuditExportRepository interface {
	ListAuditEvents(context.Context, ListAuditEventsQuery) ([]AuditEvent, error)
	CurrentAuditSequence(context.Context) (int64, error)
}

// AuditRetentionRepository owns bounded local retention maintenance. It is
// deliberately separate from AuditRepository so ordinary request auditing and
// reads do not require a destructive capability.
type AuditRetentionRepository interface {
	PurgeAuditEvents(context.Context, PurgeAuditEventsQuery) (int64, error)
	CountAuditEventsBefore(context.Context, time.Time) (int64, error)
}
