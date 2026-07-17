// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"encoding/json"
	"fmt"
	api "github.com/pliu/k8s-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sort"
)

// Server is a read-only viewer over the AccessMappings and the ClusterRoles they
// reference. It performs no mutations; policy is authored with kubectl or GitOps
// against the same Kubernetes objects.
type Server struct {
	Client client.Client
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /api/v1/clusterroles", s.roles)
	m.HandleFunc("GET /api/v1/accessmappings", s.mappings)
	m.Handle("/", http.HandlerFunc(index))
	return m
}

// roles lists the ClusterRoles referenced by any AccessMapping. ClusterRoles are
// not managed by this operator, so the set is derived from the mappings rather
// than a label selector.
func (s *Server) roles(w http.ResponseWriter, r *http.Request) {
	var maps api.AccessMappingList
	if e := s.Client.List(r.Context(), &maps); e != nil {
		respond(w, nil, e)
		return
	}
	names := map[string]bool{}
	for _, m := range maps.Items {
		for _, g := range m.Spec.ClusterRoles {
			names[g.Name] = true
		}
	}
	out := []rbacv1.ClusterRole{}
	for name := range names {
		var cr rbacv1.ClusterRole
		if e := s.Client.Get(r.Context(), client.ObjectKey{Name: name}, &cr); e == nil {
			out = append(out, cr)
		} else if !apierrors.IsNotFound(e) {
			respond(w, nil, e)
			return
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	respond(w, out, nil)
}
func (s *Server) mappings(w http.ResponseWriter, r *http.Request) {
	var l api.AccessMappingList
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
func index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>k8s-controller</title><style>
body{font:15px system-ui;margin:2rem auto;max-width:1000px;padding:0 1rem;color:#17202a}section{border:1px solid #ccd1d1;border-radius:8px;padding:1rem;margin:1rem 0}pre{box-sizing:border-box;width:100%;padding:.6rem;background:#f4f6f7;white-space:pre-wrap;max-height:28rem;overflow:auto}button{padding:.55rem .9rem;margin:.4rem .3rem .4rem 0}.row{display:grid;grid-template-columns:1fr 1fr;gap:1rem}@media(max-width:700px){.row{display:block}}</style></head><body>
<h1>k8s-controller</h1><p>Read-only view of the AccessMappings and the ClusterRoles they reference. Author policy with <code>kubectl</code> or GitOps.</p>
<div class="row"><section><h2>Referenced ClusterRoles</h2><button onclick="load('/api/v1/clusterroles','roles')">Refresh</button><pre id="roles"></pre></section><section><h2>AccessMappings</h2><button onclick="load('/api/v1/accessmappings','maps')">Refresh</button><pre id="maps"></pre></section></div>
<script>
const out=(id,v)=>document.getElementById(id).textContent=typeof v==='string'?v:JSON.stringify(v,null,2);
async function req(url){const r=await fetch(url);const t=await r.text();if(!r.ok)throw Error(t||r.status);try{return JSON.parse(t)}catch{return t}}
async function load(url,id){try{out(id,await req(url))}catch(e){out(id,e.message)}}
load('/api/v1/clusterroles','roles');load('/api/v1/accessmappings','maps');
</script></body></html>`)
}
