// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The server's metrics register into controller-runtime's global registry so a
// single /metrics endpoint serves both reconciler and HTTP-server series.
var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "k8s_controller_http_requests_total",
		Help: "HTTP requests served by the viewer, by path and status code.",
	}, []string{"path", "code"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "k8s_controller_http_request_duration_seconds",
		Help:    "HTTP request latency of the viewer, by path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"path"})
)

func init() {
	metrics.Registry.MustRegister(httpRequests, httpDuration)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// instrument records request count and latency. Static assets are lumped under
// one label to keep cardinality bounded to the fixed route set.
func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") && path != "/livez" && path != "/metrics" {
			path = "static"
		}
		httpRequests.WithLabelValues(path, strconv.Itoa(rec.code)).Inc()
		httpDuration.WithLabelValues(path).Observe(time.Since(start).Seconds())
	})
}
