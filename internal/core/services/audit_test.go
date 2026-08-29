package services_test

import (
	"context"
	"testing"
	"time"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestAuditServiceValidatesAndPagesEvents(t *testing.T) {
	t.Parallel()
	principal := "operator"
	digest := "2bb80d537b1da3e38bd30361aa855686bde0ba53e611d8a8a5e47993629e366f"
	repository := &fakeAuditRepository{append: func(event ports.AuditEvent) (ports.AuditEvent, error) {
		event.Sequence = 7
		return event, nil
	}}
	service, err := services.NewAuditService(repository)
	if err != nil {
		t.Fatal(err)
	}
	event := ports.AuditEvent{
		OccurredAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), RequestID: "request-1",
		PrincipalID: &principal, Method: "GET", Route: "/api/v1", StatusCode: 200,
		Outcome: "success", SecretPathSHA256: &digest,
	}
	recorded, err := service.Record(context.Background(), event)
	if err != nil || recorded.Sequence != 7 {
		t.Fatalf("recorded event = %#v, err=%v", recorded, err)
	}
	if _, err := service.Record(context.Background(), ports.AuditEvent{}); err == nil {
		t.Fatal("invalid event unexpectedly recorded")
	}
	repository.list = []ports.AuditEvent{{Sequence: 8}, {Sequence: 9}, {Sequence: 10}}
	page, err := service.List(context.Background(), 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextAfterSequence == nil || *page.NextAfterSequence != 9 {
		t.Fatalf("page = %#v, want two items and checkpoint 9", page)
	}
	if repository.lastQuery.AfterSequence != 7 || repository.lastQuery.PageSize != 3 {
		t.Fatalf("repository query = %#v, want after=7 pageSize=3", repository.lastQuery)
	}
}

func TestAuditServiceRejectsOversizedPage(t *testing.T) {
	t.Parallel()
	service, err := services.NewAuditService(&fakeAuditRepository{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), 0, services.MaxAuditPageSize+1); err == nil {
		t.Fatal("oversized audit page unexpectedly accepted")
	}
}

func TestAuditRetentionServicePurgesBoundedPages(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	repository := &fakeAuditRetentionRepository{remaining: 3}
	service, err := services.NewAuditRetentionService(repository, services.AuditRetentionServiceOptions{
		Clock: func() time.Time { return cutoff.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Purge(context.Background(), services.AuditRetentionRequest{Before: cutoff, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deleted != 2 || first.Remaining != 1 || !first.HasMore || first.Complete || !first.Before.Equal(cutoff) {
		t.Fatalf("first retention result = %#v", first)
	}
	second, err := service.Purge(context.Background(), services.AuditRetentionRequest{Before: cutoff, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 || second.Remaining != 0 || second.HasMore || !second.Complete {
		t.Fatalf("second retention result = %#v", second)
	}
	if !repository.lastQuery.Before.Equal(cutoff) || repository.lastQuery.PageSize != 2 {
		t.Fatalf("retention query = %#v", repository.lastQuery)
	}
}

func TestAuditRetentionServiceRejectsUnsafeRequests(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	service, err := services.NewAuditRetentionService(&fakeAuditRetentionRepository{}, services.AuditRetentionServiceOptions{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []services.AuditRetentionRequest{
		{Limit: 1},
		{Before: now.Add(time.Minute), Limit: 1},
		{Before: now, Limit: services.MaxAuditPageSize + 1},
	} {
		if _, err := service.Purge(context.Background(), request); err == nil {
			t.Fatalf("unsafe retention request %#v unexpectedly accepted", request)
		}
	}
}

type fakeAuditRepository struct {
	append    func(ports.AuditEvent) (ports.AuditEvent, error)
	list      []ports.AuditEvent
	lastQuery ports.ListAuditEventsQuery
}

type fakeAuditRetentionRepository struct {
	remaining int64
	lastQuery ports.PurgeAuditEventsQuery
}

func (r *fakeAuditRetentionRepository) PurgeAuditEvents(_ context.Context, query ports.PurgeAuditEventsQuery) (int64, error) {
	r.lastQuery = query
	deleted := int64(query.PageSize)
	if deleted > r.remaining {
		deleted = r.remaining
	}
	r.remaining -= deleted
	return deleted, nil
}

func (r *fakeAuditRetentionRepository) CountAuditEventsBefore(_ context.Context, _ time.Time) (int64, error) {
	return r.remaining, nil
}

func (r *fakeAuditRepository) AppendAuditEvent(_ context.Context, event ports.AuditEvent) (ports.AuditEvent, error) {
	if r.append == nil {
		return event, nil
	}
	return r.append(event)
}

func (r *fakeAuditRepository) ListAuditEvents(_ context.Context, query ports.ListAuditEventsQuery) ([]ports.AuditEvent, error) {
	r.lastQuery = query
	return r.list, nil
}
