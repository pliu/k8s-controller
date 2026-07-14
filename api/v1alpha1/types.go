// SPDX-License-Identifier: AGPL-3.0-only
// +kubebuilder:object:generate=true
// +groupName=rbac.pliu.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "rbac.pliu.dev", Version: "v1alpha1"}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &AccessMapping{}, &AccessMappingList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})
var AddToScheme = SchemeBuilder.AddToScheme

// +kubebuilder:validation:XValidation:rule="(self.clusterWide && !has(self.namespaces)) || (!self.clusterWide && has(self.namespaces) && size(self.namespaces)>0)",message="select namespaces or clusterWide"
type ClusterRoleGrant struct {
	Name        string   `json:"name"`
	Namespaces  []string `json:"namespaces,omitempty"`
	ClusterWide bool     `json:"clusterWide,omitempty"`
}
type AccessMappingSpec struct {
	Usernames    []string           `json:"usernames,omitempty"`
	ADGroups     []string           `json:"adGroups,omitempty"`
	ClusterRoles []ClusterRoleGrant `json:"clusterRoles"`
}
type InvalidReference struct {
	ClusterRole string `json:"clusterRole,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Reason      string `json:"reason"`
}
type AccessMappingStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	InvalidReferences  []InvalidReference `json:"invalidReferences,omitempty"`
	PolicyDigest       string             `json:"policyDigest,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=amap
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="(has(self.spec.usernames) && size(self.spec.usernames)>0) || (has(self.spec.adGroups) && size(self.spec.adGroups)>0)",message="at least one username or AD group is required"
type AccessMapping struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccessMappingSpec   `json:"spec"`
	Status            AccessMappingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AccessMappingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessMapping `json:"items"`
}

func (in *AccessMapping) DeepCopyInto(out *AccessMapping) { copy := in.DeepCopy(); *out = *copy }
func (in *AccessMapping) DeepCopy() *AccessMapping {
	if in == nil {
		return nil
	}
	out := new(AccessMapping)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Usernames = append([]string(nil), in.Spec.Usernames...)
	out.Spec.ADGroups = append([]string(nil), in.Spec.ADGroups...)
	out.Spec.ClusterRoles = append([]ClusterRoleGrant(nil), in.Spec.ClusterRoles...)
	for i := range out.Spec.ClusterRoles {
		out.Spec.ClusterRoles[i].Namespaces = append([]string(nil), in.Spec.ClusterRoles[i].Namespaces...)
	}
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	out.Status.InvalidReferences = append([]InvalidReference(nil), in.Status.InvalidReferences...)
	return out
}
func (in *AccessMapping) DeepCopyObject() runtime.Object { return in.DeepCopy() }
func (in *AccessMappingList) DeepCopy() *AccessMappingList {
	if in == nil {
		return nil
	}
	out := new(AccessMappingList)
	*out = *in
	out.Items = make([]AccessMapping, len(in.Items))
	for i := range in.Items {
		in.Items[i].DeepCopyInto(&out.Items[i])
	}
	return out
}
func (in *AccessMappingList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
