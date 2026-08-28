package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/araihu/moco/internal/adapters/authz"
	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/ports"
)

// GetLiveness implements the unauthenticated process probe.
func (s *Server) GetLiveness(context.Context, internalapi.GetLivenessRequestObject) (internalapi.GetLivenessResponseObject, error) {
	return internalapi.GetLiveness200JSONResponse{Status: internalapi.Ok}, nil
}

// GetReadiness implements the dependency-backed traffic probe.
func (s *Server) GetReadiness(ctx context.Context, _ internalapi.GetReadinessRequestObject) (internalapi.GetReadinessResponseObject, error) {
	checkContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.readiness.Ping(checkContext); err != nil {
		return internalapi.GetReadiness503JSONResponse{
			Body:    internalapi.UnavailableStatus{Status: internalapi.Unavailable},
			Headers: internalapi.GetReadiness503ResponseHeaders{RetryAfter: 1},
		}, nil
	}
	return internalapi.GetReadiness200JSONResponse{Status: internalapi.Ok}, nil
}

// GetAuthorizationSnapshot returns the complete persisted policy state without
// exposing the principal token-digest keyring.
func (s *Server) GetAuthorizationSnapshot(ctx context.Context, _ internalapi.GetAuthorizationSnapshotRequestObject) (internalapi.GetAuthorizationSnapshotResponseObject, error) {
	if s.authorization == nil {
		return internalapi.GetAuthorizationSnapshot503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: internalapi.ServiceUnavailableApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(serviceUnavailableProblem(requestID(ctx))),
				Headers: internalapi.ServiceUnavailableResponseHeaders{RetryAfter: 1, XRequestID: requestID(ctx)},
			},
		}, nil
	}
	state, err := s.authorization.LoadAuthorization(ctx)
	if err != nil {
		mapped := s.problem(ctx, "getAuthorizationSnapshot", err)
		return internalapi.GetAuthorizationSnapshot500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	return internalapi.GetAuthorizationSnapshot200JSONResponse{
		Body:    authorizationSnapshotResponse(state),
		Headers: internalapi.GetAuthorizationSnapshot200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

// ReplaceAuthorizationSnapshot atomically commits a complete policy snapshot
// and lets the local policy bus notify the reloader after commit.
func (s *Server) ReplaceAuthorizationSnapshot(ctx context.Context, request internalapi.ReplaceAuthorizationSnapshotRequestObject) (internalapi.ReplaceAuthorizationSnapshotResponseObject, error) {
	if s.authorization == nil {
		return internalapi.ReplaceAuthorizationSnapshot503ApplicationProblemPlusJSONResponse{
			ServiceUnavailableApplicationProblemPlusJSONResponse: internalapi.ServiceUnavailableApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(serviceUnavailableProblem(requestID(ctx))),
				Headers: internalapi.ServiceUnavailableResponseHeaders{RetryAfter: 1, XRequestID: requestID(ctx)},
			},
		}, nil
	}
	if request.Body == nil {
		return internalapi.ReplaceAuthorizationSnapshot400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: internalapi.BadRequestApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(badRequestProblem(requestID(ctx), "invalid_request", "A JSON request body is required.")),
				Headers: internalapi.BadRequestResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	if err := s.validateAuthorizationSnapshot(*request.Body); err != nil {
		return internalapi.ReplaceAuthorizationSnapshot400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: internalapi.BadRequestApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(badRequestProblem(requestID(ctx), "invalid_authorization_snapshot", err.Error())),
				Headers: internalapi.BadRequestResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	state := authorizationSnapshotState(*request.Body)
	if err := s.authorization.ReplaceAuthorization(ctx, state); err != nil {
		mapped := s.problem(ctx, "replaceAuthorizationSnapshot", err)
		return internalapi.ReplaceAuthorizationSnapshot500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	state, err := s.authorization.LoadAuthorization(ctx)
	if err != nil {
		mapped := s.problem(ctx, "replaceAuthorizationSnapshot", err)
		return internalapi.ReplaceAuthorizationSnapshot500ApplicationProblemPlusJSONResponse{
			InternalErrorApplicationProblemPlusJSONResponse: internalapi.InternalErrorApplicationProblemPlusJSONResponse{
				Body:    internalProblemBody(mapped.problem),
				Headers: internalapi.InternalErrorResponseHeaders{XRequestID: requestID(ctx)},
			},
		}, nil
	}
	return internalapi.ReplaceAuthorizationSnapshot200JSONResponse{
		Body:    authorizationSnapshotResponse(state),
		Headers: internalapi.ReplaceAuthorizationSnapshot200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

func (s *Server) validateAuthorizationSnapshot(snapshot internalapi.AuthorizationSnapshotInput) error {
	if len(snapshot.RoleBindings) > 10000 || len(snapshot.Policies) > 10000 {
		return errors.New("snapshot contains too many role bindings or policies")
	}
	bindings := make([]ports.AuthorizationRoleBinding, 0, len(snapshot.RoleBindings))
	for _, binding := range snapshot.RoleBindings {
		if s.principalCheck != nil && !s.principalCheck(binding.Principal) {
			return errors.New("role binding references an unknown principal")
		}
		bindings = append(bindings, ports.AuthorizationRoleBinding{
			Principal: binding.Principal,
			Role:      binding.Role,
			Domain:    binding.Domain,
		})
	}
	policies := make([]ports.AuthorizationPolicy, 0, len(snapshot.Policies))
	for _, policy := range snapshot.Policies {
		policies = append(policies, ports.AuthorizationPolicy{
			Subject: policy.Subject,
			Domain:  policy.Domain,
			Path:    policy.Path,
			Method:  policy.Method,
		})
	}
	if _, err := authz.NewStaticAuthorizer(bindings, policies); err != nil {
		return errors.New("snapshot contains an invalid role binding or policy")
	}
	return nil
}

func authorizationSnapshotState(snapshot internalapi.AuthorizationSnapshotInput) ports.AuthorizationState {
	state := ports.AuthorizationState{
		Initialized:  true,
		RoleBindings: make([]ports.AuthorizationRoleBinding, 0, len(snapshot.RoleBindings)),
		Policies:     make([]ports.AuthorizationPolicy, 0, len(snapshot.Policies)),
	}
	for _, binding := range snapshot.RoleBindings {
		state.RoleBindings = append(state.RoleBindings, ports.AuthorizationRoleBinding{
			Principal: binding.Principal,
			Role:      binding.Role,
			Domain:    binding.Domain,
		})
	}
	for _, policy := range snapshot.Policies {
		state.Policies = append(state.Policies, ports.AuthorizationPolicy{
			Subject: policy.Subject,
			Domain:  policy.Domain,
			Path:    policy.Path,
			Method:  policy.Method,
		})
	}
	return state
}

func authorizationSnapshotResponse(state ports.AuthorizationState) internalapi.AuthorizationSnapshot {
	response := internalapi.AuthorizationSnapshot{
		Initialized:  state.Initialized,
		RoleBindings: make([]internalapi.AuthorizationRoleBinding, 0, len(state.RoleBindings)),
		Policies:     make([]internalapi.AuthorizationPolicy, 0, len(state.Policies)),
	}
	for _, binding := range state.RoleBindings {
		response.RoleBindings = append(response.RoleBindings, internalapi.AuthorizationRoleBinding{
			Principal: binding.Principal,
			Role:      binding.Role,
			Domain:    binding.Domain,
		})
	}
	for _, policy := range state.Policies {
		response.Policies = append(response.Policies, internalapi.AuthorizationPolicy{
			Subject: policy.Subject,
			Domain:  policy.Domain,
			Path:    policy.Path,
			Method:  policy.Method,
		})
	}
	return response
}

func internalProblemBody(problem Problem) internalapi.Problem {
	converted := internalapi.Problem{
		Type:       problem.Type,
		Title:      problem.Title,
		Status:     problem.Status,
		Code:       problem.Code,
		RequestId:  problem.RequestId,
		Detail:     problem.Detail,
		Instance:   problem.Instance,
		ResourceId: problem.ResourceId,
	}
	if problem.Errors != nil {
		errorsValue := make([]internalapi.ProblemError, 0, len(*problem.Errors))
		for _, item := range *problem.Errors {
			errorsValue = append(errorsValue, internalapi.ProblemError{
				Code: item.Code, Field: item.Field, Message: item.Message,
			})
		}
		converted.Errors = &errorsValue
	}
	return converted
}
