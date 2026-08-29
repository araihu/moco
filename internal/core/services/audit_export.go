package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

// AuditExportRequest selects the exclusive sequence checkpoint from which an
// offline export starts. The exporter captures the current highest sequence
// before reading, so concurrent appends are left for the next export.
type AuditExportRequest struct {
	AfterSequence int64
}

// AuditExportResult describes the finite snapshot written to the JSONL
// stream. It contains no event payload beyond the count and sequence bounds.
type AuditExportResult struct {
	AfterSequence int64
	UpperSequence int64
	LastSequence  int64
	Exported      int
	Complete      bool
}

// AuditExportEvent is the stable JSONL representation of one audit event.
// Optional identity and digest fields are omitted when they are unavailable.
type AuditExportEvent struct {
	Sequence         int64     `json:"sequence"`
	OccurredAt       time.Time `json:"occurredAt"`
	RequestID        string    `json:"requestId"`
	PrincipalID      *string   `json:"principalId,omitempty"`
	Method           string    `json:"method"`
	Route            string    `json:"route"`
	StatusCode       int       `json:"statusCode"`
	Outcome          string    `json:"outcome"`
	SecretPathSHA256 *string   `json:"secretPathSha256,omitempty"`
}

// AuditExportService streams a finite, read-only ledger snapshot as JSONL.
type AuditExportService struct {
	repository ports.AuditExportRepository
}

// NewAuditExportService constructs the offline export boundary.
func NewAuditExportService(repository ports.AuditExportRepository) (*AuditExportService, error) {
	if repository == nil {
		return nil, errors.New("audit export repository is required")
	}
	return &AuditExportService{repository: repository}, nil
}

// Export writes one JSON object per line in ascending sequence order. The
// sequence upper bound is captured before the first page, which makes the
// export finite and prevents a constantly-appending ledger from keeping the
// command alive indefinitely. Rows deleted during the export are simply
// absent; callers can run another export from the returned checkpoint.
func (s *AuditExportService) Export(ctx context.Context, writer io.Writer, request AuditExportRequest) (AuditExportResult, error) {
	if writer == nil {
		return AuditExportResult{}, errors.New("audit export writer is required")
	}
	if err := ctx.Err(); err != nil {
		return AuditExportResult{}, err
	}
	if request.AfterSequence < 0 {
		return AuditExportResult{}, errors.New("audit export sequence must not be negative")
	}
	upperSequence, err := s.repository.CurrentAuditSequence(ctx)
	if err != nil {
		return AuditExportResult{}, fmt.Errorf("read audit export upper sequence: %w", err)
	}
	if upperSequence < 0 {
		return AuditExportResult{}, errors.New("audit export upper sequence must not be negative")
	}
	result := AuditExportResult{
		AfterSequence: request.AfterSequence,
		UpperSequence: upperSequence,
		LastSequence:  request.AfterSequence,
		Complete:      request.AfterSequence >= upperSequence,
	}
	if result.Complete {
		return result, nil
	}

	encoder := json.NewEncoder(writer)
	cursor := request.AfterSequence
	for cursor < upperSequence {
		if err := ctx.Err(); err != nil {
			return AuditExportResult{}, err
		}
		page, err := s.repository.ListAuditEvents(ctx, ports.ListAuditEventsQuery{
			AfterSequence: cursor,
			PageSize:      MaxAuditPageSize,
		})
		if err != nil {
			return AuditExportResult{}, fmt.Errorf("list audit export page after %d: %w", cursor, err)
		}
		if len(page) == 0 {
			break
		}
		advanced := false
		for _, event := range page {
			if event.Sequence <= cursor {
				return AuditExportResult{}, fmt.Errorf("audit export sequence %d is not after %d", event.Sequence, cursor)
			}
			if event.Sequence > upperSequence {
				result.Complete = true
				break
			}
			if err := encoder.Encode(auditExportEvent(event)); err != nil {
				return AuditExportResult{}, fmt.Errorf("encode audit event %d: %w", event.Sequence, err)
			}
			cursor = event.Sequence
			result.Exported++
			result.LastSequence = cursor
			advanced = true
		}
		if result.Complete || !advanced || len(page) < MaxAuditPageSize {
			break
		}
	}
	result.Complete = true
	result.LastSequence = cursor
	return result, nil
}

func auditExportEvent(event ports.AuditEvent) AuditExportEvent {
	return AuditExportEvent{
		Sequence:         event.Sequence,
		OccurredAt:       event.OccurredAt.UTC(),
		RequestID:        event.RequestID,
		PrincipalID:      cloneAuditExportString(event.PrincipalID),
		Method:           event.Method,
		Route:            event.Route,
		StatusCode:       event.StatusCode,
		Outcome:          event.Outcome,
		SecretPathSHA256: cloneAuditExportString(event.SecretPathSHA256),
	}
}

func cloneAuditExportString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
