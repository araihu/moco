package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var latencyBuckets = [...]float64{0.005, 0.025, 0.1, 0.5, 1, 5, 30}

type metricKey struct{ route, method, status string }
type requestMetric struct {
	count   uint64
	seconds float64
	buckets [len(latencyBuckets)]uint64
}

// Metrics uses bounded labels: no IDs, paths, tokens or query values are exposed.
type Metrics struct {
	mu             sync.Mutex
	requests       map[metricKey]*requestMetric
	inflight       atomic.Int64
	auditFailures  atomic.Uint64
	auditFailureAt atomic.Int64
	started        time.Time
}

func NewMetrics() *Metrics {
	return &Metrics{requests: make(map[metricKey]*requestMetric), started: time.Now()}
}

// AuditResult latches a persistence failure until restart and operator review.
// The response may already be delivered; this is detection, not transactional auditing.
func (m *Metrics) AuditResult(err error) {
	if err != nil {
		m.auditFailures.Add(1)
		m.auditFailureAt.Store(time.Now().Unix())
	}
}

func (m *Metrics) AuditHealthy() bool { return m.auditFailures.Load() == 0 }

func routeClass(r *http.Request) string {
	p := path.Clean(r.URL.Path)
	switch {
	case p == "/livez" || p == "/readyz":
		return "probe"
	case strings.HasPrefix(p, "/internal/"):
		return "internal"
	case strings.HasPrefix(p, "/api/") && strings.HasSuffix(p, "/watch"):
		return "watch"
	case strings.HasPrefix(p, "/api/"):
		return "api"
	default:
		return "other"
	}
}

func (m *Metrics) Wrap(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		m.inflight.Add(1)
		defer m.inflight.Add(-1)
		recorder := &auditResponseWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		seconds := time.Since(started).Seconds()
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS":
		default:
			method = "other"
		}
		key := metricKey{routeClass(r), method, strconv.Itoa(recorder.statusCode())}
		m.mu.Lock()
		entry := m.requests[key]
		if entry == nil {
			entry = &requestMetric{}
			m.requests[key] = entry
		}
		entry.count++
		entry.seconds += seconds
		for i, upper := range latencyBuckets {
			if seconds <= upper {
				entry.buckets[i]++
			}
		}
		m.mu.Unlock()
		if key.route != "probe" {
			logger.InfoContext(r.Context(), "request completed", "route", key.route, "method", method,
				"status", recorder.statusCode(), "durationSeconds", seconds, "requestId", w.Header().Get("X-Request-ID"))
		}
	})
}

// ServePrometheus writes the Prometheus text 0.0.4 format.
func (m *Metrics) ServePrometheus(w http.ResponseWriter, version string) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "# TYPE moco_build_info gauge\nmoco_build_info{version=%s} 1\n", strconv.Quote(version))
	_, _ = fmt.Fprintf(w, "# TYPE moco_uptime_seconds gauge\nmoco_uptime_seconds %g\n# TYPE moco_http_inflight gauge\nmoco_http_inflight %d\n", time.Since(m.started).Seconds(), m.inflight.Load())
	_, _ = fmt.Fprintf(w, "# TYPE moco_audit_failures_total counter\nmoco_audit_failures_total %d\n# TYPE moco_audit_last_failure_timestamp_seconds gauge\nmoco_audit_last_failure_timestamp_seconds %d\n", m.auditFailures.Load(), m.auditFailureAt.Load())
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	_, _ = fmt.Fprintf(w, "# TYPE moco_go_heap_bytes gauge\nmoco_go_heap_bytes %d\n# TYPE moco_go_goroutines gauge\nmoco_go_goroutines %d\n", memory.HeapAlloc, runtime.NumGoroutine())
	m.mu.Lock()
	snapshot := make(map[metricKey]requestMetric, len(m.requests))
	for key, entry := range m.requests {
		snapshot[key] = *entry
	}
	m.mu.Unlock()
	_, _ = fmt.Fprint(w, "# TYPE moco_http_requests_total counter\n# TYPE moco_http_request_duration_seconds histogram\n")
	for key, entry := range snapshot {
		labels := fmt.Sprintf("route=%q,method=%q,status=%q", key.route, key.method, key.status)
		_, _ = fmt.Fprintf(w, "moco_http_requests_total{%s} %d\n", labels, entry.count)
		for i, upper := range latencyBuckets {
			_, _ = fmt.Fprintf(w, "moco_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(upper, 'g', -1, 64), entry.buckets[i])
		}
		_, _ = fmt.Fprintf(w, "moco_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\nmoco_http_request_duration_seconds_count{%s} %d\nmoco_http_request_duration_seconds_sum{%s} %g\n", labels, entry.count, labels, entry.count, labels, entry.seconds)
	}
}

// PublicListener hides administration even when a valid admin token is supplied.
func PublicListener(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		if p == "/metrics" || p == "/internal" || strings.HasPrefix(p, "/internal/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MetricsAuthentication protects only the operational scrape endpoint. Other
// admin routes retain normal principal authentication and authorization.
func MetricsAuthentication(token string, metrics http.Handler, admin http.Handler) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			admin.ServeHTTP(w, r)
			return
		}
		provided := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if token == "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		metrics.ServeHTTP(w, r)
	})
}
