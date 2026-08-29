package httpapi

import (
	"context"
	"errors"
	"testing"
)

func TestWatchChangesMapsCheckpointDependencyFailureToRetryableResponse(t *testing.T) {
	server := &Server{resourceVersion: failingResourceVersionReader{}}
	response, err := server.WatchChanges(context.Background(), WatchChangesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	unavailable, ok := response.(WatchChanges503ApplicationProblemPlusJSONResponse)
	if !ok {
		t.Fatalf("watch response type = %T, want 503", response)
	}
	if unavailable.Headers.RetryAfter != 1 || unavailable.Body.Code != "service_unavailable" {
		t.Fatalf("unexpected retryable response: %#v", unavailable)
	}
}

type failingResourceVersionReader struct{}

func (failingResourceVersionReader) CurrentResourceVersion(context.Context) (int64, error) {
	return 0, errors.New("resource version unavailable")
}
