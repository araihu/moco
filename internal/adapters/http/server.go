package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
	"github.com/google/uuid"
)

type contextKey string

const (
	requestIDContextKey contextKey = "moco-request-id"
	principalContextKey contextKey = "moco-principal"
	maxSecretJSONBytes             = 2 << 20
)

// BearerAuthenticator resolves a bearer credential to a stable principal ID.
type BearerAuthenticator interface {
	Authenticate(string) (string, bool)
}

// ReadinessChecker reports whether a required runtime dependency is available.
type ReadinessChecker interface {
	Ping(context.Context) error
}

// HandlerOptions contains all dependencies and deployment credentials.
type HandlerOptions struct {
	Tenants         *services.TenantService
	Vaults          *services.VaultService
	Secrets         *services.SecretService
	Readiness       ReadinessChecker
	ResourceVersion ports.ResourceVersionReader
	Authenticator   BearerAuthenticator
	Authorizer      ports.Authorizer
	Authorization   *services.AuthorizationPolicyService
	PrincipalCheck  func(string) bool
	ServiceVersion  string
	Logger          *slog.Logger
}

// NewHandler composes the generated strict server, authentication, validation,
// and unauthenticated process probes.
func NewHandler(options HandlerOptions) (http.Handler, error) {
	if options.Tenants == nil {
		return nil, errors.New("tenant service is required")
	}
	if options.Vaults == nil {
		return nil, errors.New("vault service is required")
	}
	if options.Secrets == nil {
		return nil, errors.New("secret service is required")
	}
	if options.Authenticator == nil {
		return nil, errors.New("bearer authenticator is required")
	}
	if options.Authorizer == nil {
		return nil, errors.New("authorizer is required")
	}
	if options.Readiness == nil {
		return nil, errors.New("readiness checker is required")
	}
	if options.ServiceVersion == "" {
		options.ServiceVersion = "dev"
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	server := &Server{
		tenants:         options.Tenants,
		vaults:          options.Vaults,
		secrets:         options.Secrets,
		readiness:       options.Readiness,
		resourceVersion: options.ResourceVersion,
		authorizer:      options.Authorizer,
		authorization:   options.Authorization,
		principalCheck:  options.PrincipalCheck,
		serviceVersion:  options.ServiceVersion,
		logger:          options.Logger,
	}
	strict := NewStrictHandlerWithOptions(server, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_request", "The request could not be decoded."))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			options.Logger.ErrorContext(r.Context(), "HTTP response failed",
				"requestId", requestID(r.Context()), "error", err)
			writeProblem(w, internalProblem(requestID(r.Context())))
		},
	})
	internalStrict := internalapi.NewStrictHandlerWithOptions(server, nil, internalapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_request", "The request could not be decoded."))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			options.Logger.ErrorContext(r.Context(), "HTTP internal response failed",
				"requestId", requestID(r.Context()), "error", err)
			writeProblem(w, internalProblem(requestID(r.Context())))
		},
	})

	mux := http.NewServeMux()
	api := HandlerFromMux(strict, mux)
	internalapi.HandlerFromMux(internalStrict, mux)

	handler := strictResourceJSON(api)
	handler = authorizationMiddleware(options.Authorizer, options.Logger, handler)
	handler = bearerAuthentication(options.Authenticator, handler)
	handler = requestIDs(handler)
	return handler, nil
}

// Server implements the generated strict server contract.
type Server struct {
	tenants         *services.TenantService
	vaults          *services.VaultService
	secrets         *services.SecretService
	readiness       ReadinessChecker
	resourceVersion ports.ResourceVersionReader
	authorization   *services.AuthorizationPolicyService
	principalCheck  func(string) bool
	serviceVersion  string
	logger          *slog.Logger
	authorizer      ports.Authorizer
}

func requestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestIDValue := uuid.NewString()
		values := r.Header.Values("X-Request-ID")
		if len(values) == 1 && validRequestID(values[0]) {
			requestIDValue = values[0]
		} else if len(values) > 0 {
			w.Header().Set("X-Request-ID", requestIDValue)
			writeProblem(w, badRequestProblem(requestIDValue, "invalid_request_id", "X-Request-ID must contain between 1 and 128 characters."))
			return
		}
		r.Header.Set("X-Request-ID", requestIDValue)
		w.Header().Set("X-Request-ID", requestIDValue)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey, requestIDValue)))
	})
}

func bearerAuthentication(authenticator BearerAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProtectedAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, unauthorizedProblem(requestID(r.Context())))
			return
		}
		parts := strings.SplitN(values[0], " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, unauthorizedProblem(requestID(r.Context())))
			return
		}
		provided := parts[1]
		principal, authenticated := authenticator.Authenticate(provided)
		if provided == "" || !authenticated || principal == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, unauthorizedProblem(requestID(r.Context())))
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authorizationMiddleware(authorizer ports.Authorizer, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domain, resource, enforce := authorizationResource(r)
		if !enforce {
			next.ServeHTTP(w, r)
			return
		}
		principal := principalID(r.Context())
		if principal == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, unauthorizedProblem(requestID(r.Context())))
			return
		}
		action := r.Method
		if action == http.MethodHead {
			action = http.MethodGet
		}
		allowed, err := authorizer.Authorize(r.Context(), principal, domain, resource, action)
		if !allowed && err == nil && r.Method == http.MethodGet && r.URL.Path == "/api/v1/tenants" {
			if scoped, ok := authorizer.(ports.AnyDomainAuthorizer); ok {
				allowed, err = scoped.AuthorizeAnyDomain(r.Context(), principal, resourceForAnyDomain(r.URL.Path), action)
			}
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "authorization failed", "requestId", requestID(r.Context()), "error", err)
			writeProblem(w, internalProblem(requestID(r.Context())))
			return
		}
		if !allowed {
			writeProblem(w, forbiddenProblem(requestID(r.Context())))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func resourceForAnyDomain(path string) string {
	if path == "/api/v1/tenants" {
		return "/api/v1/tenants/{tenantId}"
	}
	return path
}

func authorizationResource(r *http.Request) (string, string, bool) {
	if !isProtectedAPIPath(r.URL.Path) {
		return "", "", false
	}
	path := r.URL.Path
	if path == authorizationAdminPath {
		return "*", path, r.Method == http.MethodGet || r.Method == http.MethodPut
	}
	if !isPublicAPIPath(path) {
		return "", "", false
	}
	if path != "/api/v1" && strings.HasSuffix(path, "/") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	method := r.Method
	switch {
	case path == "/api/v1":
		return "*", path, isReadMethod(method)
	case path == "/api/v1/watch":
		return "*", path, isReadMethod(method)
	case path == "/api/v1/tenants":
		return "*", path, isReadMethod(method) || method == http.MethodPost
	case len(parts) == 4 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "":
		return parts[3], path, isReadMethod(method) || method == http.MethodPut || method == http.MethodDelete
	case len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "" && parts[4] == "vaults":
		return parts[3], path, isReadMethod(method) || method == http.MethodPost
	case len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "" && parts[4] == "vaults":
		return parts[3], path, isReadMethod(method) || method == http.MethodPut || method == http.MethodDelete
	case len(parts) == 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "" && parts[4] == "vaults" && parts[6] == "secrets":
		return parts[3], path, isReadMethod(method)
	case len(parts) == 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "" && parts[4] == "vaults" && parts[6] == "secret":
		return parts[3], path, isReadMethod(method) || method == http.MethodPut || method == http.MethodDelete
	case len(parts) == 8 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "tenants" && parts[3] != "" && parts[4] == "vaults" && parts[6] == "secret" && parts[7] == "metadata":
		return parts[3], path, isReadMethod(method)
	default:
		return "", "", false
	}
}

func isReadMethod(method string) bool { return method == http.MethodGet || method == http.MethodHead }

func strictResourceJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := resourceWriteTarget(r)
		if target == nil {
			next.ServeHTTP(w, r)
			return
		}
		defer wipeResourceWriteTarget(target)
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_content_type", "Content-Type must be application/json."))
			return
		}
		bodyReader := io.Reader(r.Body)
		if _, secretWrite := target.(*SecretWrite); secretWrite {
			r.Body = http.MaxBytesReader(w, r.Body, maxSecretJSONBytes)
			bodyReader = r.Body
		} else if _, authorizationWrite := target.(*internalapi.AuthorizationSnapshotInput); authorizationWrite {
			r.Body = http.MaxBytesReader(w, r.Body, maxAuthorizationJSONBytes)
			bodyReader = r.Body
		}
		body, err := io.ReadAll(bodyReader)
		defer wipeHTTPBytes(body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				if _, authorizationWrite := target.(*internalapi.AuthorizationSnapshotInput); authorizationWrite {
					writeProblem(w, badRequestProblem(requestID(r.Context()), "authorization_snapshot_too_large", "The authorization snapshot exceeds the supported size limit."))
				} else {
					writeProblem(w, secretTooLargeProblem(requestID(r.Context())))
				}
				return
			}
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_json", "The JSON body could not be read."))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_json", "The JSON body is malformed or contains unknown fields."))
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_json", "The request body must contain exactly one JSON value."))
			return
		}
		if !validResourceJSONShape(body, target, r.Method) {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_json", "The JSON body is missing a required field or contains null where an object or string is required."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

const (
	authorizationAdminPath    = "/internal/v1/authorization"
	maxAuthorizationJSONBytes = 2 << 20
)

func isProtectedAPIPath(path string) bool {
	return isPublicAPIPath(path) || path == authorizationAdminPath
}

func isPublicAPIPath(path string) bool {
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

func resourceWriteTarget(r *http.Request) any {
	if r.Method == http.MethodPut && r.URL.Path == authorizationAdminPath {
		return &internalapi.AuthorizationSnapshotInput{}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "tenants" {
		return nil
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 3:
		return &TenantCreate{}
	case r.Method == http.MethodPut && len(parts) == 4:
		return &TenantUpdate{}
	case r.Method == http.MethodPost && len(parts) == 5 && parts[4] == "vaults":
		return &VaultCreate{}
	case r.Method == http.MethodPut && len(parts) == 6 && parts[4] == "vaults":
		return &VaultUpdate{}
	case r.Method == http.MethodPut && len(parts) == 7 && parts[4] == "vaults" && parts[6] == "secret":
		return &SecretWrite{}
	default:
		return nil
	}
}

func validResourceJSONShape(body []byte, target any, method string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return false
	}
	required := []string{"name"}
	nonNullable := []string{"name", "labels", "externalId"}
	if _, authorizationWrite := target.(*internalapi.AuthorizationSnapshotInput); authorizationWrite {
		required = []string{"roleBindings", "policies"}
		nonNullable = []string{"roleBindings", "policies"}
	} else if _, secretWrite := target.(*SecretWrite); secretWrite {
		required = []string{"value"}
		nonNullable = []string{"value", "contentType"}
	} else if method == http.MethodPut {
		required = []string{"name", "description", "labels"}
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	for _, name := range nonNullable {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func wipeResourceWriteTarget(target any) {
	if secret, ok := target.(*SecretWrite); ok {
		wipeHTTPBytes(secret.Value)
	}
}

func validRequestID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	if value == "" {
		return uuid.NewString()
	}
	return value
}

func principalID(ctx context.Context) string {
	value, _ := ctx.Value(principalContextKey).(string)
	return value
}

func writeProblem(w http.ResponseWriter, problem Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-ID", problem.RequestId)
	w.WriteHeader(int(problem.Status))
	_ = json.NewEncoder(w).Encode(problem)
}
