// SPDX-License-Identifier: AGPL-3.0-only
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
