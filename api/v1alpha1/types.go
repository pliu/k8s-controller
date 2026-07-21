// SPDX-License-Identifier: AGPL-3.0-only
// +kubebuilder:object:generate=true
// +groupName=k8s.pliu.dev
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "k8s.pliu.dev", Version: "v1alpha1"}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ManagedNamespace{}, &ManagedNamespaceList{}, &ClusterAccessMapping{}, &ClusterAccessMappingList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})
var AddToScheme = SchemeBuilder.AddToScheme

// AccessMapping grants a single group OR a list of users a set of ClusterRoles.
// It is namespace-scoped as an entry in a ManagedNamespace, and cluster-wide as
// the spec of a ClusterAccessMapping. Exactly one of group or users is set.
// +kubebuilder:validation:XValidation:rule="(has(self.group) && size(self.group) > 0) != (has(self.users) && size(self.users) > 0)",message="set exactly one of group or users"
type AccessMapping struct {
	Group string   `json:"group,omitempty"`
	Users []string `json:"users,omitempty"`
	// +kubebuilder:validation:MinItems=1
	ClusterRoles []string `json:"clusterRoles"`
}

// ResourceQuota is the desired quota applied to the managed namespace.
type ResourceQuota struct {
	Hard corev1.ResourceList `json:"hard,omitempty"`
}

// ManagedNamespaceSpec configures the namespace named by metadata.name. The
// resource's own name is the namespace name, so there is exactly one
// ManagedNamespace per namespace.
type ManagedNamespaceSpec struct {
	// Labels are applied to the managed Namespace and kept in sync. Labels
	// managed by other actors are preserved; controller-owned labels take
	// precedence on conflicts.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are applied to the managed Namespace and kept in sync.
	// Annotations managed by other actors are preserved.
	Annotations map[string]string `json:"annotations,omitempty"`
	// ResourceQuota, when set, is applied to the namespace; clearing it removes
	// the managed quota.
	ResourceQuota  *ResourceQuota  `json:"resourceQuota,omitempty"`
	AccessMappings []AccessMapping `json:"accessMappings,omitempty"`
}

type InvalidReference struct {
	ClusterRole string `json:"clusterRole,omitempty"`
	Reason      string `json:"reason"`
}

type ManagedNamespaceStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	InvalidReferences  []InvalidReference `json:"invalidReferences,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=mns
// +kubebuilder:subresource:status
type ManagedNamespace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ManagedNamespaceSpec   `json:"spec"`
	Status            ManagedNamespaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ManagedNamespaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedNamespace `json:"items"`
}

type ClusterAccessMappingStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	InvalidReferences  []InvalidReference `json:"invalidReferences,omitempty"`
}

// ClusterAccessMapping binds a single group OR a list of users to a set of
// ClusterRoles cluster-wide, via ClusterRoleBindings. Its spec is an
// AccessMapping evaluated without a namespace.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cam
// +kubebuilder:subresource:status
type ClusterAccessMapping struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccessMapping              `json:"spec"`
	Status            ClusterAccessMappingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterAccessMappingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterAccessMapping `json:"items"`
}
