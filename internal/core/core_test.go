// SPDX-License-Identifier: AGPL-3.0-only
package core

import (
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestEvaluateGroupChanges(t *testing.T) {
	m := api.AccessMapping{ObjectMeta: metav1.ObjectMeta{Name: "m"}, Spec: api.AccessMappingSpec{ADGroups: []string{"g"}, ClusterRoles: []api.ClusterRoleGrant{{Name: "view", Namespaces: []string{"team"}}}}}
	a, _ := Evaluate("alice", []string{"g"}, []api.AccessMapping{m})
	if len(a) != 1 {
		t.Fatal(a)
	}
	b, _ := Evaluate("alice", nil, []api.AccessMapping{m})
	if len(b) != 0 {
		t.Fatal(b)
	}
}
