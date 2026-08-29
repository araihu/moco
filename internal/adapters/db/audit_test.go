package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

func TestAuditEventsPersistAndDoNotAdvanceResourceVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := "operator"
	digest := "2bb80d537b1da3e38bd30361aa855686bde0ba53e611d8a8a5e47993629e366f"
	first := ports.AuditEvent{
		OccurredAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), RequestID: "request-1",
		PrincipalID: &principal, Method: "GET", Route: "/api/v1/tenants", StatusCode: 200,
		Outcome: "success", SecretPathSHA256: &digest,
	}
	second := first
	second.RequestID = "request-2"
	second.PrincipalID = nil
	second.SecretPathSHA256 = nil
	second.StatusCode = 401
	second.Outcome = "failure"
	gotFirst, err := store.AppendAuditEvent(ctx, first)
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	gotSecond, err := store.AppendAuditEvent(ctx, second)
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if gotFirst.Sequence != 1 || gotSecond.Sequence != 2 {
		t.Fatalf("assigned sequences = %d, %d, want 1, 2", gotFirst.Sequence, gotSecond.Sequence)
	}
	if version, err := store.CurrentResourceVersion(ctx); err != nil || version != 0 {
		t.Fatalf("resource version after audit writes = %d, err=%v, want 0", version, err)
	}
	page, err := store.ListAuditEvents(ctx, ports.ListAuditEventsQuery{AfterSequence: 0, PageSize: 10})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	want := []ports.AuditEvent{gotFirst, gotSecond}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("events = %#v, want %#v", page, want)
	}
	page, err = store.ListAuditEvents(ctx, ports.ListAuditEventsQuery{AfterSequence: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list continuation: %v", err)
	}
	if len(page) != 1 || page[0].Sequence != 2 {
		t.Fatalf("continuation = %#v, want second event", page)
	}
}
