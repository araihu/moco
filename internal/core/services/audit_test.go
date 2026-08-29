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

type fakeAuditRepository struct {
	append    func(ports.AuditEvent) (ports.AuditEvent, error)
	list      []ports.AuditEvent
	lastQuery ports.ListAuditEventsQuery
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
