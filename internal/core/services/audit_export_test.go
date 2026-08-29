package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestAuditExportStreamsFiniteJSONLSnapshot(t *testing.T) {
	t.Parallel()
	principal := "operator"
	digest := "2bb80d537b1da3e38bd30361aa855686bde0ba53e611d8a8a5e47993629e366f"
	repository := &fakeAuditExportRepository{
		upper: 3,
		events: []ports.AuditEvent{
			{Sequence: 1, OccurredAt: time.Date(2026, 8, 29, 12, 0, 0, 123, time.FixedZone("BRT", -3*60*60)), RequestID: "request-1", PrincipalID: &principal, Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success", SecretPathSHA256: &digest},
			{Sequence: 2, OccurredAt: time.Date(2026, 8, 29, 15, 1, 0, 0, time.UTC), RequestID: "request-2", Method: "POST", Route: "/api/v1/tenants", StatusCode: 201, Outcome: "success"},
			{Sequence: 3, OccurredAt: time.Date(2026, 8, 29, 15, 2, 0, 0, time.UTC), RequestID: "request-3", Method: "GET", Route: "/api/v1/audit", StatusCode: 401, Outcome: "failure"},
			{Sequence: 4, OccurredAt: time.Date(2026, 8, 29, 15, 3, 0, 0, time.UTC), RequestID: "request-4", Method: "GET", Route: "/api/v1/new", StatusCode: 200, Outcome: "success"},
		},
	}
	service, err := services.NewAuditExportService(repository)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := service.Export(context.Background(), &output, services.AuditExportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Exported != 3 || result.UpperSequence != 3 || result.LastSequence != 3 || !result.Complete {
		t.Fatalf("export result = %#v", result)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("JSONL lines = %d, want 3: %q", len(lines), output.String())
	}
	var first services.AuditExportEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || !first.OccurredAt.Equal(time.Date(2026, 8, 29, 15, 0, 0, 123, time.UTC)) || first.PrincipalID == nil || *first.PrincipalID != principal || first.SecretPathSHA256 == nil {
		t.Fatalf("first export event = %#v", first)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if _, ok := second["principalId"]; ok {
		t.Fatalf("nil principal was not omitted: %#v", second)
	}
}

func TestAuditExportResumesAfterExclusiveCheckpoint(t *testing.T) {
	t.Parallel()
	repository := &fakeAuditExportRepository{
		upper: 3,
		events: []ports.AuditEvent{
			{Sequence: 1, RequestID: "one"}, {Sequence: 2, RequestID: "two"}, {Sequence: 3, RequestID: "three"},
		},
	}
	service, err := services.NewAuditExportService(repository)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := service.Export(context.Background(), &output, services.AuditExportRequest{AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Exported != 2 || result.LastSequence != 3 || !result.Complete {
		t.Fatalf("resumed export result = %#v", result)
	}
	if strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("resumed JSONL = %q", output.String())
	}
}

func TestAuditExportRejectsUnsafeRequests(t *testing.T) {
	t.Parallel()
	service, err := services.NewAuditExportService(&fakeAuditExportRepository{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Export(context.Background(), nil, services.AuditExportRequest{}); err == nil {
		t.Fatal("nil writer unexpectedly accepted")
	}
	var output bytes.Buffer
	if _, err := service.Export(context.Background(), &output, services.AuditExportRequest{AfterSequence: -1}); err == nil {
		t.Fatal("negative checkpoint unexpectedly accepted")
	}
}

func TestAuditExportRejectsNonIncreasingRepositoryPage(t *testing.T) {
	t.Parallel()
	service, err := services.NewAuditExportService(&fakeAuditExportRepository{
		upper:  2,
		events: []ports.AuditEvent{{Sequence: 1, RequestID: "one"}, {Sequence: 1, RequestID: "duplicate"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := service.Export(context.Background(), &output, services.AuditExportRequest{}); err == nil {
		t.Fatal("non-increasing repository page unexpectedly accepted")
	}
}

type fakeAuditExportRepository struct {
	upper  int64
	events []ports.AuditEvent
}

func (r *fakeAuditExportRepository) CurrentAuditSequence(_ context.Context) (int64, error) {
	return r.upper, nil
}

func (r *fakeAuditExportRepository) ListAuditEvents(_ context.Context, query ports.ListAuditEventsQuery) ([]ports.AuditEvent, error) {
	items := make([]ports.AuditEvent, 0, query.PageSize)
	for _, event := range r.events {
		if event.Sequence <= query.AfterSequence {
			continue
		}
		items = append(items, event)
		if len(items) == query.PageSize {
			break
		}
	}
	return items, nil
}
