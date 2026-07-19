// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

//go:embed ui
var uiFS embed.FS
var uiRoot, _ = fs.Sub(uiFS, "ui")

// Server is a read-only viewer over the ManagedNamespaces and
// ClusterAccessMappings. Policy is authored with kubectl or GitOps against the
// same Kubernetes objects.
type Server struct {
	Client client.Client
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// The shared controller-runtime registry means this one endpoint serves the
	// reconciler metrics and the HTTP metrics from the same process.
	m.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	m.HandleFunc("GET /api/v1/managednamespaces", s.namespaces)
	m.HandleFunc("GET /api/v1/clusteraccessmappings", s.clusterAccess)
	m.Handle("/", http.FileServer(http.FS(uiRoot)))
	return instrument(m)
}

func (s *Server) namespaces(w http.ResponseWriter, r *http.Request) {
	var l api.ManagedNamespaceList
	e := s.Client.List(r.Context(), &l)
	respond(w, l.Items, e)
}

func (s *Server) clusterAccess(w http.ResponseWriter, r *http.Request) {
	var l api.ClusterAccessMappingList
	e := s.Client.List(r.Context(), &l)
	respond(w, l.Items, e)
}

func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
