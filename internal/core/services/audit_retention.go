package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

// AuditRetentionRequest describes one bounded local retention batch.
type AuditRetentionRequest struct {
	Before time.Time
	Limit  int
}

// AuditRetentionResult reports one retention batch without returning event
// contents. Remaining is a diagnostic and is not a historical snapshot.
type AuditRetentionResult struct {
	Before    time.Time
	Deleted   int
	Remaining int64
	HasMore   bool
	Complete  bool
}

// AuditRetentionService coordinates local, bounded audit retention.
type AuditRetentionService struct {
	repository ports.AuditRetentionRepository
	clock      func() time.Time
}

// AuditRetentionServiceOptions supplies a deterministic clock for tests.
type AuditRetentionServiceOptions struct {
	Clock func() time.Time
}

// NewAuditRetentionService constructs the local retention boundary.
func NewAuditRetentionService(repository ports.AuditRetentionRepository, options ...AuditRetentionServiceOptions) (*AuditRetentionService, error) {
	if repository == nil {
		return nil, errors.New("audit retention repository is required")
	}
	if len(options) > 1 {
		return nil, errors.New("at most one audit retention options value is allowed")
	}
	clock := time.Now
	if len(options) == 1 && options[0].Clock != nil {
		clock = options[0].Clock
	}
	return &AuditRetentionService{repository: repository, clock: clock}, nil
}

// Purge deletes at most one bounded page strictly before the requested cutoff.
// Callers repeat the same request until Complete is true. New events may make
// the diagnostic count change, so retention is intentionally not a snapshot.
func (s *AuditRetentionService) Purge(ctx context.Context, request AuditRetentionRequest) (AuditRetentionResult, error) {
	if err := ctx.Err(); err != nil {
		return AuditRetentionResult{}, err
	}
	if request.Before.IsZero() {
		return AuditRetentionResult{}, errors.New("audit retention cutoff is required")
	}
	now := s.clock().UTC()
	if request.Before.After(now) {
		return AuditRetentionResult{}, errors.New("audit retention cutoff must not be in the future")
	}
	if request.Limit == 0 {
		request.Limit = DefaultAuditPageSize
	}
	if request.Limit < 1 || request.Limit > MaxAuditPageSize {
		return AuditRetentionResult{}, fmt.Errorf("audit retention limit must be between 1 and %d", MaxAuditPageSize)
	}
	cutoff := request.Before.UTC()
	deleted, err := s.repository.PurgeAuditEvents(ctx, ports.PurgeAuditEventsQuery{Before: cutoff, PageSize: request.Limit})
	if err != nil {
		return AuditRetentionResult{}, fmt.Errorf("purge audit events: %w", err)
	}
	remaining, err := s.repository.CountAuditEventsBefore(ctx, cutoff)
	if err != nil {
		return AuditRetentionResult{}, fmt.Errorf("count retained audit events: %w", err)
	}
	return AuditRetentionResult{
		Before: cutoff, Deleted: int(deleted), Remaining: remaining,
		HasMore: remaining > 0, Complete: remaining == 0,
	}, nil
}
