// SPDX-License-Identifier: AGPL-3.0-only
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"github.com/pliu/rbac-controller/internal/auth"
	"github.com/pliu/rbac-controller/internal/core"
	"github.com/pliu/rbac-controller/internal/directory"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type Server struct {
	Client               client.Client
	Kube                 kubernetes.Interface
	Auth                 auth.Authenticator
	Resolver             directory.Resolver
	Namespace, APIServer string
	CA                   []byte
	Admins               map[string]bool
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	p := http.NewServeMux()
	p.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) { write(w, 200, auth.From(r.Context())) })
	p.HandleFunc("GET /api/v1/clusterroles", s.admin(s.roles))
	p.HandleFunc("POST /api/v1/clusterroles", s.admin(s.createRole))
	p.HandleFunc("GET /api/v1/accessmappings", s.admin(s.mappings))
	p.HandleFunc("POST /api/v1/accessmappings", s.admin(s.createMapping))
	p.HandleFunc("GET /api/v1/access", s.admin(s.search))
	p.HandleFunc("POST /api/v1/credentials", s.credential)
	p.HandleFunc("PATCH /api/v1/credentials", s.extend)
	p.HandleFunc("DELETE /api/v1/credentials", s.revoke)
	p.Handle("/", http.HandlerFunc(index))
	m.Handle("/", auth.Middleware(s.Auth, p))
	return m
}
func (s *Server) admin(n http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i := auth.From(r.Context())
		if !s.Admins[i.Username] {
			ok := false
			for _, g := range i.ADGroups {
				ok = ok || s.Admins[g]
			}
			if !ok {
				http.Error(w, "forbidden", 403)
				return
			}
		}
		n(w, r)
	}
}
func (s *Server) roles(w http.ResponseWriter, r *http.Request) {
	var l rbacv1.ClusterRoleList
	e := s.Client.List(r.Context(), &l, client.MatchingLabels{core.LabelManagedBy: core.ManagedBy})
	respond(w, l.Items, e)
}
func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	var x rbacv1.ClusterRole
	if !decode(w, r, &x) {
		return
	}
	x.ResourceVersion = ""
	x.Labels = map[string]string{core.LabelManagedBy: core.ManagedBy}
	e := s.Client.Create(r.Context(), &x)
	respond(w, x, e)
}
func (s *Server) mappings(w http.ResponseWriter, r *http.Request) {
	var l api.AccessMappingList
	e := s.Client.List(r.Context(), &l)
	respond(w, l.Items, e)
}
func (s *Server) createMapping(w http.ResponseWriter, r *http.Request) {
	var x api.AccessMapping
	if !decode(w, r, &x) {
		return
	}
	x.ResourceVersion = ""
	x.Status = api.AccessMappingStatus{}
	if x.Labels == nil {
		x.Labels = map[string]string{}
	}
	x.Labels[core.LabelManagedBy] = core.ManagedBy
	e := s.Client.Create(r.Context(), &x)
	respond(w, x, e)
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("username")
	g, e := s.Resolver.Resolve(r.Context(), u)
	if e != nil {
		g = directory.Result{}
	}
	var l api.AccessMappingList
	if e = s.Client.List(r.Context(), &l); e != nil {
		respond(w, nil, e)
		return
	}
	grants, d := core.Evaluate(u, g.Groups, l.Items)
	write(w, 200, map[string]any{"username": u, "adGroups": g.Groups, "grants": grants, "policyDigest": d})
}
func (s *Server) ensure(ctx context.Context, u string) (*corev1.ServiceAccount, error) {
	now := time.Now().UTC()
	key := types.NamespacedName{Namespace: s.Namespace, Name: core.CredentialName(u)}
	var sa corev1.ServiceAccount
	e := s.Client.Get(ctx, key, &sa)
	if apierrors.IsNotFound(e) {
		f := false
		sa = corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{core.LabelManagedBy: core.ManagedBy}, Annotations: map[string]string{core.AnnUsername: u}, Finalizers: []string{core.Finalizer}}, AutomountServiceAccountToken: &f}
	} else if e != nil {
		return nil, e
	}
	if sa.Annotations == nil {
		sa.Annotations = map[string]string{}
	}
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	if sa.Annotations[core.AnnRequestedAt] == "" {
		sa.Annotations[core.AnnRequestedAt] = now.Format(time.RFC3339)
	}
	sa.Annotations[core.AnnRequestedBy] = u
	sa.Annotations[core.AnnExtendedAt] = now.Format(time.RFC3339)
	sa.Annotations[core.AnnRevision] = hex.EncodeToString(raw)
	sa.Annotations[core.AnnExpires] = now.Add(8 * time.Hour).Format(time.RFC3339)
	sa.Annotations[core.AnnState] = "Reconciling"
	if sa.ResourceVersion == "" {
		e = s.Client.Create(ctx, &sa)
	} else {
		e = s.Client.Update(ctx, &sa)
	}
	return &sa, e
}
func (s *Server) waitReady(ctx context.Context, sa *corev1.ServiceAccount) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if e := s.Client.Get(ctx, client.ObjectKeyFromObject(sa), sa); e != nil {
			return e
		}
		if sa.Annotations[core.AnnState] == "Ready" && sa.Annotations[core.AnnObserved] == sa.Annotations[core.AnnRevision] {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("permissions did not converge: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func (s *Server) credential(w http.ResponseWriter, r *http.Request) {
	i := auth.From(r.Context())
	sa, e := s.ensure(r.Context(), i.Username)
	if e != nil {
		respond(w, nil, e)
		return
	}
	if e = s.waitReady(r.Context(), sa); e != nil {
		http.Error(w, e.Error(), 503)
		return
	}
	sec := int64(3600)
	tr, e := s.Kube.CoreV1().ServiceAccounts(s.Namespace).CreateToken(r.Context(), sa.Name, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &sec}}, metav1.CreateOptions{})
	if e != nil {
		respond(w, nil, e)
		return
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["cluster"] = &clientcmdapi.Cluster{Server: s.APIServer, CertificateAuthorityData: s.CA}
	cfg.AuthInfos[sa.Name] = &clientcmdapi.AuthInfo{Token: tr.Status.Token}
	cfg.Contexts["default"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: sa.Name}
	cfg.CurrentContext = "default"
	raw, e := clientcmd.Write(*cfg)
	if e != nil {
		respond(w, nil, e)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}
func (s *Server) extend(w http.ResponseWriter, r *http.Request) {
	sa, e := s.ensure(r.Context(), auth.From(r.Context()).Username)
	if e == nil {
		e = s.waitReady(r.Context(), sa)
	}
	if e != nil {
		http.Error(w, e.Error(), 503)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	u := auth.From(r.Context()).Username
	e := s.Client.Delete(r.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: core.CredentialName(u), Namespace: s.Namespace}})
	if e != nil && !apierrors.IsNotFound(e) {
		respond(w, nil, e)
		return
	}
	w.WriteHeader(204)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		http.Error(w, e.Error(), 400)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	write(w, 200, v)
}
func write(w http.ResponseWriter, n int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(n)
	json.NewEncoder(w).Encode(v)
}
func index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>RBAC Controller</title><style>
body{font:15px system-ui;margin:2rem auto;max-width:1000px;padding:0 1rem;color:#17202a}section{border:1px solid #ccd1d1;border-radius:8px;padding:1rem;margin:1rem 0}textarea,pre,input{box-sizing:border-box;width:100%;padding:.6rem}textarea{min-height:11rem}button{padding:.55rem .9rem;margin:.4rem .3rem .4rem 0}pre{background:#f4f6f7;white-space:pre-wrap;max-height:24rem;overflow:auto}.row{display:grid;grid-template-columns:1fr 1fr;gap:1rem}@media(max-width:700px){.row{display:block}}</style></head><body>
<h1>RBAC Controller</h1><p id="me">Loading identity…</p>
<section><h2>Access search</h2><input id="user" placeholder="Exact username"><button onclick="search()">Search</button><pre id="access"></pre></section>
<div class="row"><section><h2>Managed ClusterRoles</h2><button onclick="load('/api/v1/clusterroles','roles')">Refresh</button><pre id="roles"></pre></section><section><h2>AccessMappings</h2><button onclick="load('/api/v1/accessmappings','maps')">Refresh</button><pre id="maps"></pre></section></div>
<div class="row"><section><h2>Create ClusterRole</h2><textarea id="role">{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","metadata":{"name":"example-reader"},"rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","watch"]}]}</textarea><button onclick="create('/api/v1/clusterroles','role')">Create</button></section>
<section><h2>Create AccessMapping</h2><textarea id="mapping">{"apiVersion":"rbac.pliu.dev/v1alpha1","kind":"AccessMapping","metadata":{"name":"example"},"spec":{"usernames":["alice@example.com"],"adGroups":[],"clusterRoles":[{"name":"example-reader","namespaces":["default"]}]}}</textarea><button onclick="create('/api/v1/accessmappings','mapping')">Create</button></section></div>
<section><h2>My credential</h2><p>Issuing converges permissions before returning a one-hour bearer token. Extension renews the ServiceAccount lifecycle without replacing a downloaded token. Revocation deletes the managed ServiceAccount.</p><button onclick="credential()">Issue kubeconfig</button><button onclick="extendLife()">Extend lifecycle</button><button onclick="revoke()">Revoke</button><pre id="result"></pre></section>
<script>
const out=(id,v)=>document.getElementById(id).textContent=typeof v==='string'?v:JSON.stringify(v,null,2);
async function req(url,opt){const r=await fetch(url,opt);const t=await r.text();if(!r.ok)throw Error(t||r.status);try{return JSON.parse(t)}catch{return t}}
async function load(url,id){try{out(id,await req(url))}catch(e){out(id,e.message)}}
async function search(){const u=document.getElementById('user').value;try{out('access',await req('/api/v1/access?username='+encodeURIComponent(u)))}catch(e){out('access',e.message)}}
async function create(url,id){try{out('result',await req(url,{method:'POST',headers:{'Content-Type':'application/json'},body:document.getElementById(id).value}))}catch(e){out('result',e.message)}}
async function credential(){try{const text=await req('/api/v1/credentials',{method:'POST'});const b=new Blob([text],{type:'application/yaml'}),a=document.createElement('a');a.href=URL.createObjectURL(b);a.download='kubeconfig.yaml';a.click();URL.revokeObjectURL(a.href);out('result','Kubeconfig issued.')}catch(e){out('result',e.message)}}
async function extendLife(){try{await req('/api/v1/credentials',{method:'PATCH'});out('result','Credential lifecycle extended.')}catch(e){out('result',e.message)}}
async function revoke(){try{await req('/api/v1/credentials',{method:'DELETE'});out('result','Credential revoked.')}catch(e){out('result',e.message)}}
load('/api/v1/me','me');load('/api/v1/clusterroles','roles');load('/api/v1/accessmappings','maps');
</script></body></html>`)
}
