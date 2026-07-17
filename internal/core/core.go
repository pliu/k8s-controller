// SPDX-License-Identifier: AGPL-3.0-only
package core

import (
	"crypto/sha256"
	"encoding/hex"
)

const ManagedBy = "k8s-controller"
const LabelManagedBy = "app.kubernetes.io/managed-by"
const LabelOwnerUID = "k8s.pliu.dev/owner-uid"
const LabelOwnerName = "k8s.pliu.dev/owner-name"
const Finalizer = "k8s.pliu.dev/binding-cleanup"

func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

// BindingName is the deterministic name of the RoleBinding or ClusterRoleBinding
// generated for one (AccessMapping, ClusterRole, scope) triple. The scope is a
// namespace name for a RoleBinding or "*" for a cluster-wide ClusterRoleBinding.
func BindingName(mapping, role, scope string) string {
	return "rbacctl-" + Hash(mapping + "\x00" + role + "\x00" + scope)[:20]
}
