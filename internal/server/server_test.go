// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/pliu/k8s-controller/internal/core"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestDevUserNamespaceLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	h := (&Server{Client: cl, Dev: true}).Handler()

	create := httptest.NewRequest("POST", "/api/v1/managednamespaces", nil)
	create.Header.Set("X-Remote-User", "alice")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, create)
	if rr.Code != 201 {
		t.Fatalf("create status %d: %s", rr.Code, rr.Body.String())
	}

	var got api.ManagedNamespace
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "user-alice"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[core.LabelDeleteNamespace] != "true" {
		t.Fatalf("cleanup label = %q", got.Labels[core.LabelDeleteNamespace])
	}
	if len(got.Spec.AccessMappings) != 1 || len(got.Spec.AccessMappings[0].Users) != 1 || got.Spec.AccessMappings[0].Users[0] != "alice" {
		t.Fatalf("access mappings = %#v", got.Spec.AccessMappings)
	}
	if roles := got.Spec.AccessMappings[0].ClusterRoles; len(roles) != 1 || roles[0] != "cluster-admin" {
		t.Fatalf("cluster roles = %#v", roles)
	}

	remove := httptest.NewRequest("DELETE", "/api/v1/managednamespaces", nil)
	remove.Header.Set("X-Remote-User", "alice")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, remove)
	if rr.Code != 204 {
		t.Fatalf("delete status %d: %s", rr.Code, rr.Body.String())
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "user-alice"}, &got); err == nil {
		t.Fatal("ManagedNamespace still exists")
	}
}

func TestDevUserNamespaceRejectsInvalidUser(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Dev: true}).Handler()
	for _, username := range []string{"", "Alice@example.com"} {
		req := httptest.NewRequest("POST", "/api/v1/managednamespaces", nil)
		req.Header.Set("X-Remote-User", username)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 400 {
			t.Fatalf("username %q: status %d", username, rr.Code)
		}
	}
}

func TestDevEndpointsAreDisabledByDefault(t *testing.T) {
	h := (&Server{}).Handler()
	req := httptest.NewRequest("POST", "/api/v1/managednamespaces", nil)
	req.Header.Set("X-Remote-User", "alice")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("status %d, want 404", rr.Code)
	}
}
