// SPDX-License-Identifier: AGPL-3.0-only
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"sort"
	"time"
)

const ManagedBy = "rbac-controller"
const LabelManagedBy = "app.kubernetes.io/managed-by"
const LabelOwnerUID = "rbac.pliu.dev/owner-uid"
const LabelCredential = "rbac.pliu.dev/credential-name"
const AnnUsername = "rbac.pliu.dev/principal-username"
const AnnRequestedBy = "rbac.pliu.dev/requested-by"
const AnnRequestedAt = "rbac.pliu.dev/requested-at"
const AnnExtendedAt = "rbac.pliu.dev/last-extended-at"
const AnnExpires = "rbac.pliu.dev/expires-at"
const AnnRevision = "rbac.pliu.dev/request-revision"
const AnnObserved = "rbac.pliu.dev/observed-request-revision"
const AnnState = "rbac.pliu.dev/credential-state"
const AnnGroups = "rbac.pliu.dev/resolved-ad-groups"
const AnnDirectorySync = "rbac.pliu.dev/last-directory-sync"
const AnnPolicy = "rbac.pliu.dev/policy-digest"
const Finalizer = "rbac.pliu.dev/credential-cleanup"

func Hash(v string) string              { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func CredentialName(u string) string    { return "user-" + Hash(u)[:16] }
func BindingName(u, r, s string) string { return "rbacctl-" + Hash(u + "\x00" + r + "\x00" + s)[:20] }

type Grant struct {
	ClusterRole string   `json:"clusterRole"`
	Namespace   string   `json:"namespace,omitempty"`
	ClusterWide bool     `json:"clusterWide,omitempty"`
	Mappings    []string `json:"mappings"`
}

func Evaluate(user string, groups []string, maps []api.AccessMapping) ([]Grant, string) {
	gs := map[string]bool{}
	for _, g := range groups {
		gs[g] = true
	}
	out := map[string]*Grant{}
	for _, m := range maps {
		match := false
		for _, u := range m.Spec.Usernames {
			match = match || u == user
		}
		for _, g := range m.Spec.ADGroups {
			match = match || gs[g]
		}
		if !match {
			continue
		}
		for _, e := range m.Spec.ClusterRoles {
			if e.ClusterWide {
				add(out, Grant{ClusterRole: e.Name, ClusterWide: true}, m.Name)
			} else {
				for _, n := range e.Namespaces {
					add(out, Grant{ClusterRole: e.Name, Namespace: n}, m.Name)
				}
			}
		}
	}
	grants := make([]Grant, 0, len(out))
	for _, g := range out {
		sort.Strings(g.Mappings)
		grants = append(grants, *g)
	}
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].ClusterRole+grants[i].Namespace < grants[j].ClusterRole+grants[j].Namespace
	})
	raw, _ := json.Marshal(grants)
	return grants, Hash(user + string(raw))
}
func add(out map[string]*Grant, g Grant, m string) {
	scope := g.Namespace
	if g.ClusterWide {
		scope = "*"
	}
	k := g.ClusterRole + "\x00" + scope
	if old := out[k]; old != nil {
		old.Mappings = append(old.Mappings, m)
	} else {
		g.Mappings = []string{m}
		out[k] = &g
	}
}
func ParseTime(v string) (time.Time, error) { return time.Parse(time.RFC3339, v) }
