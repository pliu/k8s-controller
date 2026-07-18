// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/pliu/k8s-controller/internal/core"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

//go:embed ui
var uiFS embed.FS
var uiRoot, _ = fs.Sub(uiFS, "ui")

// Server is normally a read-only viewer over the ManagedNamespaces. Dev enables
// self-service creation and deletion of a namespace for the authenticated user.
type Server struct {
	Client client.Client
	Dev    bool
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// The shared controller-runtime registry means this one endpoint serves the
	// reconciler metrics and the HTTP metrics from the same process.
	m.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	m.HandleFunc("GET /api/v1/managednamespaces", s.namespaces)
	m.HandleFunc("GET /api/v1/clusteraccessmappings", s.clusterAccess)
	if s.Dev {
		m.HandleFunc("POST /api/v1/managednamespaces", s.createUserNamespace)
		m.HandleFunc("DELETE /api/v1/managednamespaces", s.deleteUserNamespace)
	}
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

func (s *Server) createUserNamespace(w http.ResponseWriter, r *http.Request) {
	username, name, ok := userNamespace(w, r)
	if !ok {
		return
	}
	mns := api.ManagedNamespace{
		TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "ManagedNamespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{core.LabelDeleteNamespace: "true"},
		},
		Spec: api.ManagedNamespaceSpec{AccessMappings: []api.AccessMapping{{Users: []string{username}, ClusterRoles: []string{"cluster-admin"}}}},
	}
	if e := s.Client.Create(r.Context(), &mns); e != nil {
		if apierrors.IsAlreadyExists(e) {
			http.Error(w, "managed namespace already exists", http.StatusConflict)
			return
		}
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(mns)
}

func (s *Server) deleteUserNamespace(w http.ResponseWriter, r *http.Request) {
	_, name, ok := userNamespace(w, r)
	if !ok {
		return
	}
	mns := &api.ManagedNamespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if e := s.Client.Delete(r.Context(), mns); e != nil {
		if apierrors.IsNotFound(e) {
			http.Error(w, "managed namespace not found", http.StatusNotFound)
			return
		}
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func userNamespace(w http.ResponseWriter, r *http.Request) (username, name string, ok bool) {
	username = strings.TrimSpace(r.Header.Get("X-Remote-User"))
	if username == "" {
		http.Error(w, "X-Remote-User header is required", http.StatusBadRequest)
		return "", "", false
	}
	name = "user-" + username
	if problems := validation.IsDNS1123Label(name); len(problems) > 0 {
		http.Error(w, "X-Remote-User does not form a valid namespace name: "+strings.Join(problems, "; "), http.StatusBadRequest)
		return "", "", false
	}
	return username, name, true
}

func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
