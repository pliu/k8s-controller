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
const AnnotationManagedMetadata = "k8s.pliu.dev/managed-metadata"
const Finalizer = "k8s.pliu.dev/binding-cleanup"

func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

// BindingName is the deterministic name of the RoleBinding generated for one
// (ManagedNamespace, subject set, ClusterRole) triple. subject identifies the
// AccessMapping's group or user list so its bindings stay distinct.
func BindingName(owner, subject, role string) string {
	return "k8sc-" + Hash(owner + "\x00" + subject + "\x00" + role)[:20]
}

// QuotaName is the deterministic name of the ResourceQuota a ManagedNamespace
// manages in its namespace.
func QuotaName(owner string) string {
	return "k8sc-quota-" + Hash(owner)[:16]
}
