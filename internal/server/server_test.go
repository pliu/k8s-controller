// SPDX-License-Identifier: Apache-2.0
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticRoutes(t *testing.T) {
	h := (&Server{}).Handler()
	cases := []struct{ path, contains, ctype string }{
		{"/", "k8s-controller", "text/html"},
		{"/app.css", ".tabs", "text/css"},
		{"/app.js", "managednamespaces", "javascript"},
		{"/livez", "ok", ""},
		// Served from the shared controller-runtime registry: earlier requests in
		// this test must already appear as HTTP series. (The go/process and
		// reconciler series come from packages linked into the real binary.)
		{"/metrics", "k8s_controller_http_requests_total", ""},
		{"/metrics", "k8s_controller_http_request_duration_seconds", ""},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", c.path, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d", c.path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), c.contains) {
			t.Fatalf("%s: body missing %q", c.path, c.contains)
		}
		if c.ctype != "" && !strings.Contains(rr.Header().Get("Content-Type"), c.ctype) {
			t.Fatalf("%s: content-type %q, want %q", c.path, rr.Header().Get("Content-Type"), c.ctype)
		}
	}
}

func TestMetricsPathLabelIsBounded(t *testing.T) {
	h := (&Server{}).Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/bogus-metrics-label", nil))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rr.Body.String(), "bogus-metrics-label") {
		t.Fatal("arbitrary request path leaked into metric labels")
	}
}

func TestMutationEndpointsAreUnavailable(t *testing.T) {
	h := (&Server{}).Handler()
	for _, method := range []string{"POST", "DELETE"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, "/api/v1/managednamespaces", nil))
		if rr.Code != 404 {
			t.Errorf("%s status %d, want 404", method, rr.Code)
		}
	}
}
