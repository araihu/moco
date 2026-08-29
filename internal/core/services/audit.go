package services

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/araihu/moco/internal/core/ports"
)

const (
	DefaultAuditPageSize = 50
	MaxAuditPageSize     = 200

	auditOutcomeSuccess = "success"
	auditOutcomeFailure = "failure"
)

// AuditEventPage is one bounded page of the append-only audit ledger.
type AuditEventPage struct {
	Items             []ports.AuditEvent
	HasMore           bool
	NextAfterSequence *int64
}

// AuditService coordinates validation and access to the durable audit ledger.
type AuditService struct {
	repository ports.AuditRepository
}

// NewAuditService constructs the audit persistence boundary.
func NewAuditService(repository ports.AuditRepository) (*AuditService, error) {
	if repository == nil {
		return nil, errors.New("audit repository is required")
	}
	return &AuditService{repository: repository}, nil
}

// Record appends one validated request event and returns its assigned sequence.
func (s *AuditService) Record(ctx context.Context, event ports.AuditEvent) (ports.AuditEvent, error) {
	if err := ctx.Err(); err != nil {
		return ports.AuditEvent{}, err
	}
	if err := validateAuditEvent(event); err != nil {
		return ports.AuditEvent{}, err
	}
	recorded, err := s.repository.AppendAuditEvent(ctx, event)
	if err != nil {
		return ports.AuditEvent{}, fmt.Errorf("append audit event: %w", err)
	}
	return recorded, nil
}

// List returns events strictly after afterSequence, in sequence order.
func (s *AuditService) List(ctx context.Context, afterSequence, pageSize int64) (AuditEventPage, error) {
	if err := ctx.Err(); err != nil {
		return AuditEventPage{}, err
	}
	if afterSequence < 0 {
		return AuditEventPage{}, errors.New("audit sequence must not be negative")
	}
	if pageSize <= 0 {
		pageSize = DefaultAuditPageSize
	}
	if pageSize > MaxAuditPageSize {
		return AuditEventPage{}, fmt.Errorf("audit page size must not exceed %d", MaxAuditPageSize)
	}
	items, err := s.repository.ListAuditEvents(ctx, ports.ListAuditEventsQuery{
		AfterSequence: afterSequence,
		PageSize:      int(pageSize) + 1,
	})
	if err != nil {
		return AuditEventPage{}, fmt.Errorf("list audit events: %w", err)
	}
	page := AuditEventPage{Items: items}
	if len(items) <= int(pageSize) {
		return page, nil
	}
	page.HasMore = true
	page.Items = items[:pageSize]
	next := page.Items[len(page.Items)-1].Sequence
	page.NextAfterSequence = &next
	return page, nil
}

func validateAuditEvent(event ports.AuditEvent) error {
	if event.Sequence != 0 {
		return errors.New("audit sequence is assigned by the repository")
	}
	if event.OccurredAt.IsZero() {
		return errors.New("audit occurrence time is required")
	}
	if strings.TrimSpace(event.RequestID) == "" || len(event.RequestID) > 128 {
		return errors.New("audit request ID must contain between 1 and 128 bytes")
	}
	if event.PrincipalID != nil && (strings.TrimSpace(*event.PrincipalID) == "" || len(*event.PrincipalID) > 128) {
		return errors.New("audit principal ID must contain between 1 and 128 bytes")
	}
	if len(event.Method) < 1 || len(event.Method) > 16 {
		return errors.New("audit method must contain between 1 and 16 bytes")
	}
	if len(event.Route) < 1 || len(event.Route) > 2048 {
		return errors.New("audit route must contain between 1 and 2048 bytes")
	}
	if event.StatusCode < 100 || event.StatusCode > 599 {
		return errors.New("audit status code must be between 100 and 599")
	}
	if event.Outcome != auditOutcomeSuccess && event.Outcome != auditOutcomeFailure {
		return errors.New("audit outcome must be success or failure")
	}
	if event.SecretPathSHA256 != nil {
		if len(*event.SecretPathSHA256) != 64 {
			return errors.New("audit secret path digest must contain 64 hexadecimal bytes")
		}
		var digest [32]byte
		if _, err := hex.Decode(digest[:], []byte(*event.SecretPathSHA256)); err != nil {
			return errors.New("audit secret path digest must be lowercase hexadecimal")
		}
		if hex.EncodeToString(digest[:]) != *event.SecretPathSHA256 {
			return errors.New("audit secret path digest must be lowercase hexadecimal")
		}
	}
	return nil
}
