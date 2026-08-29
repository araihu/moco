package httpapi

import (
	"context"
	"strconv"
	"strings"
	"time"
)

const (
	watchPollInterval = 100 * time.Millisecond
	maxWatchTimeout   = 25 * time.Second
)

// WatchChanges waits for a durable resource revision to advance. The endpoint
// intentionally returns only a checkpoint: callers perform a full resync of
// the collections they own after changed=true.
func (s *Server) WatchChanges(ctx context.Context, request WatchChangesRequestObject) (WatchChangesResponseObject, error) {
	if s.resourceVersion == nil {
		problem := serviceUnavailableProblem(requestID(ctx))
		return WatchChanges503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: serviceUnavailableResponse(problem, 5),
		}, nil
	}

	requested, supplied, err := parseResourceVersion(optionalAlias(request.Params.ResourceVersion))
	if err != nil {
		problem := badRequestProblem(requestID(ctx), "invalid_resource_version", "resourceVersion must match rv-<non-negative-integer>.")
		return WatchChanges400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	timeout := time.Duration(0)
	if request.Params.TimeoutSeconds != nil {
		seconds := int32(*request.Params.TimeoutSeconds)
		if seconds < 0 || seconds > int32(maxWatchTimeout/time.Second) {
			problem := badRequestProblem(requestID(ctx), "invalid_watch_timeout", "timeoutSeconds must be between 0 and 25.")
			return WatchChanges400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
		}
		timeout = time.Duration(seconds) * time.Second
	}

	current, err := s.resourceVersion.CurrentResourceVersion(ctx)
	if err != nil {
		problem := serviceUnavailableProblem(requestID(ctx))
		return WatchChanges503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: serviceUnavailableResponse(problem, 1),
		}, nil
	}
	if !supplied {
		return watchResponse(ctx, current, false), nil
	}
	if requested > current {
		problem := badRequestProblem(requestID(ctx), "resource_version_ahead", "resourceVersion is newer than the server checkpoint.")
		return WatchChanges400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
	}
	if current > requested || timeout == 0 {
		return watchResponse(ctx, current, current > requested), nil
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(watchPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			current, err := s.resourceVersion.CurrentResourceVersion(ctx)
			if err != nil {
				problem := serviceUnavailableProblem(requestID(ctx))
				return WatchChanges503ApplicationProblemPlusJSONResponse{
					ServiceUnavailableApplicationProblemPlusJSONResponse: serviceUnavailableResponse(problem, 1),
				}, nil
			}
			if current < requested {
				problem := badRequestProblem(requestID(ctx), "resource_version_ahead", "resourceVersion is newer than the server checkpoint.")
				return WatchChanges400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
			}
			return watchResponse(ctx, current, current > requested), nil
		case <-ticker.C:
			current, err := s.resourceVersion.CurrentResourceVersion(ctx)
			if err != nil {
				problem := serviceUnavailableProblem(requestID(ctx))
				return WatchChanges503ApplicationProblemPlusJSONResponse{
					ServiceUnavailableApplicationProblemPlusJSONResponse: serviceUnavailableResponse(problem, 1),
				}, nil
			}
			if current < requested {
				problem := badRequestProblem(requestID(ctx), "resource_version_ahead", "resourceVersion is newer than the server checkpoint.")
				return WatchChanges400ApplicationProblemPlusJSONResponse{badRequestResponse(problem)}, nil
			}
			if current > requested {
				return watchResponse(ctx, current, true), nil
			}
		}
	}
}

func parseResourceVersion(value *string) (revision int64, supplied bool, err error) {
	if value == nil {
		return 0, false, nil
	}
	text := string(*value)
	if len(text) < 4 || len(text) > 64 || !strings.HasPrefix(text, "rv-") {
		return 0, true, strconv.ErrSyntax
	}
	revision, err = strconv.ParseInt(text[3:], 10, 64)
	if err != nil || revision < 0 {
		return 0, true, strconv.ErrSyntax
	}
	return revision, true, nil
}

func watchResponse(ctx context.Context, revision int64, changed bool) WatchChanges200JSONResponse {
	return WatchChanges200JSONResponse{
		Body:    WatchResult{ResourceVersion: "rv-" + strconv.FormatInt(revision, 10), Changed: changed},
		Headers: WatchChanges200ResponseHeaders{CacheControl: "no-store", XRequestID: requestID(ctx)},
	}
}
