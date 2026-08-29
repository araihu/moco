package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

// DefaultAuditRetentionMinimumAge is the safety buffer applied to destructive
// retention requests. Operators can preview a more recent cutoff with dry-run,
// but an actual delete must leave this much time for investigation/export.
const DefaultAuditRetentionMinimumAge = time.Hour

// AuditRetentionRequest describes one bounded local retention batch.
type AuditRetentionRequest struct {
	Before time.Time
	Limit  int
	DryRun bool
}

// AuditRetentionResult reports one retention batch without returning event
// contents. Remaining is a diagnostic and is not a historical snapshot.
type AuditRetentionResult struct {
	Before    time.Time
	Deleted   int
	Remaining int64
	HasMore   bool
	Complete  bool
	DryRun    bool
}

// AuditRetentionService coordinates local, bounded audit retention.
type AuditRetentionService struct {
	repository ports.AuditRetentionRepository
	clock      func() time.Time
	minimumAge time.Duration
}

// AuditRetentionServiceOptions supplies a deterministic clock and optional
// minimum age for tests or a deployment-specific safety policy.
type AuditRetentionServiceOptions struct {
	Clock      func() time.Time
	MinimumAge time.Duration
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
	minimumAge := DefaultAuditRetentionMinimumAge
	if len(options) == 1 && options[0].Clock != nil {
		clock = options[0].Clock
	}
	if len(options) == 1 && options[0].MinimumAge != 0 {
		if options[0].MinimumAge < 0 {
			return nil, errors.New("audit retention minimum age must not be negative")
		}
		minimumAge = options[0].MinimumAge
	}
	return &AuditRetentionService{repository: repository, clock: clock, minimumAge: minimumAge}, nil
}

// MinimumAge returns the safety buffer enforced for destructive requests.
func (s *AuditRetentionService) MinimumAge() time.Duration { return s.minimumAge }

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
	if !request.DryRun && request.Before.After(now.Add(-s.minimumAge)) {
		return AuditRetentionResult{}, fmt.Errorf("audit retention cutoff must be at least %s old", s.minimumAge)
	}
	if request.Limit == 0 {
		request.Limit = DefaultAuditPageSize
	}
	if request.Limit < 1 || request.Limit > MaxAuditPageSize {
		return AuditRetentionResult{}, fmt.Errorf("audit retention limit must be between 1 and %d", MaxAuditPageSize)
	}
	cutoff := request.Before.UTC()
	var deleted int64
	var err error
	if !request.DryRun {
		deleted, err = s.repository.PurgeAuditEvents(ctx, ports.PurgeAuditEventsQuery{Before: cutoff, PageSize: request.Limit})
		if err != nil {
			return AuditRetentionResult{}, fmt.Errorf("purge audit events: %w", err)
		}
	}
	remaining, err := s.repository.CountAuditEventsBefore(ctx, cutoff)
	if err != nil {
		return AuditRetentionResult{}, fmt.Errorf("count retained audit events: %w", err)
	}
	return AuditRetentionResult{
		Before: cutoff, Deleted: int(deleted), Remaining: remaining,
		HasMore: remaining > 0, Complete: remaining == 0, DryRun: request.DryRun,
	}, nil
}
