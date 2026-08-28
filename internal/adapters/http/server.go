package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/araihu/moco/internal/core/services"
	"github.com/google/uuid"
)

type contextKey string

const (
	requestIDContextKey contextKey = "moco-request-id"
	principalContextKey contextKey = "moco-principal"
)

// ReadinessChecker reports whether a required runtime dependency is available.
type ReadinessChecker interface {
	Ping(context.Context) error
}

// HandlerOptions contains all dependencies and deployment credentials.
type HandlerOptions struct {
	Tenants        *services.TenantService
	Vaults         *services.VaultService
	Readiness      ReadinessChecker
	BearerToken    string
	ServiceVersion string
	Logger         *slog.Logger
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
	if options.Readiness == nil {
		return nil, errors.New("readiness checker is required")
	}
	if len(options.BearerToken) < 32 {
		return nil, errors.New("bearer token must contain at least 32 bytes")
	}
	if options.ServiceVersion == "" {
		options.ServiceVersion = "dev"
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	server := &Server{
		tenants:        options.Tenants,
		vaults:         options.Vaults,
		serviceVersion: options.ServiceVersion,
		logger:         options.Logger,
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

	mux := http.NewServeMux()
	HandlerFromMux(strict, mux)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := options.Readiness.Ping(ctx); err != nil {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	handler := strictResourceJSON(mux)
	handler = bearerAuthentication(options.BearerToken, handler)
	handler = requestIDs(handler)
	return handler, nil
}

// Server implements the generated strict server contract.
type Server struct {
	tenants        *services.TenantService
	vaults         *services.VaultService
	serviceVersion string
	logger         *slog.Logger
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

func bearerAuthentication(expectedToken string, next http.Handler) http.Handler {
	expectedDigest := sha256.Sum256([]byte(expectedToken))
	principal := base64.RawURLEncoding.EncodeToString(expectedDigest[:])
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPublicAPIPath(r.URL.Path) {
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
		providedDigest := sha256.Sum256([]byte(provided))
		if provided == "" || subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, unauthorizedProblem(requestID(r.Context())))
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func strictResourceJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := resourceWriteTarget(r)
		if target == nil {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_content_type", "Content-Type must be application/json."))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
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
		if !validResourceJSONShape(body, r.Method) {
			writeProblem(w, badRequestProblem(requestID(r.Context()), "invalid_json", "The JSON body is missing a required field or contains null where an object or string is required."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicAPIPath(path string) bool {
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

func resourceWriteTarget(r *http.Request) any {
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
	default:
		return nil
	}
}

func validResourceJSONShape(body []byte, method string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return false
	}
	required := []string{"name"}
	nonNullable := []string{"name", "labels", "externalId"}
	if method == http.MethodPut {
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
