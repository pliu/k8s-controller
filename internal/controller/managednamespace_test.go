// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"reflect"
	"testing"
	"time"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/pliu/k8s-controller/internal/core"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestManagedNamespaceMetadataStaysInSync(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	name := "team-a"
	mns := &api.ManagedNamespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: api.ManagedNamespaceSpec{
			Labels:      map[string]string{"team": "a", "remove-label": "old"},
			Annotations: map[string]string{"contact": "alice", "remove-annotation": "old"},
		},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Labels:      map[string]string{"external-label": "keep"},
		Annotations: map[string]string{"external-annotation": "keep"},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	r := &ManagedNamespaceReconciler{Client: cl}
	ctx := context.Background()

	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}
	mns.Spec.Labels = map[string]string{"team": "b"}
	mns.Spec.Annotations = map[string]string{"contact": "bob"}
	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}

	var got corev1.Namespace
	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	wantLabels := map[string]string{
		"external-label":    "keep",
		"team":              "b",
		core.LabelManagedBy: core.ManagedBy,
		core.LabelOwnerName: name,
	}
	if !reflect.DeepEqual(got.Labels, wantLabels) {
		t.Errorf("labels = %#v, want %#v", got.Labels, wantLabels)
	}
	wantAnnotations := map[string]string{"external-annotation": "keep", "contact": "bob"}
	if !reflect.DeepEqual(got.Annotations, wantAnnotations) {
		t.Errorf("annotations = %#v, want %#v", got.Annotations, wantAnnotations)
	}

	mns.Spec.Labels = nil
	mns.Spec.Annotations = nil
	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	wantLabels = map[string]string{
		"external-label":    "keep",
		core.LabelManagedBy: core.ManagedBy,
		core.LabelOwnerName: name,
	}
	if !reflect.DeepEqual(got.Labels, wantLabels) {
		t.Errorf("labels after clearing spec = %#v, want %#v", got.Labels, wantLabels)
	}
	wantAnnotations = map[string]string{"external-annotation": "keep"}
	if !reflect.DeepEqual(got.Annotations, wantAnnotations) {
		t.Errorf("annotations after clearing spec = %#v, want %#v", got.Annotations, wantAnnotations)
	}
}

func TestManagedNamespaceDeletionRetainsNamespaceAndQuota(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now())
	mnsUID := types.UID("mns-uid")
	mns := &api.ManagedNamespace{ObjectMeta: metav1.ObjectMeta{
		Name:              "team-a",
		UID:               mnsUID,
		Finalizers:        []string{core.Finalizer},
		DeletionTimestamp: &now,
	}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mns.Name}}
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      core.QuotaName(mns.Name),
			Namespace: mns.Name,
			Labels: map[string]string{
				core.LabelManagedBy: core.ManagedBy,
				core.LabelOwnerUID:  string(mnsUID),
				core.LabelOwnerName: mns.Name,
			},
		},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourcePods: resource.MustParse("10"),
		}},
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      core.BindingName(mns.Name, "group:devs", "edit"),
			Namespace: mns.Name,
			Labels: map[string]string{
				core.LabelManagedBy: core.ManagedBy,
				core.LabelOwnerUID:  string(mnsUID),
				core.LabelOwnerName: mns.Name,
			},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mns, ns, quota, binding).Build()
	r := &ManagedNamespaceReconciler{Client: cl}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mns)}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(ns), &corev1.Namespace{}); err != nil {
		t.Fatalf("namespace should exist: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(quota), &corev1.ResourceQuota{}); err != nil {
		t.Fatalf("resource quota should exist: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(binding), &rbacv1.RoleBinding{}); err == nil {
		t.Fatal("role binding should have been deleted")
	}
}

// A ManagedNamespace recreated under the same name gets a new UID. Quotas left
// by the previous CR still carry the old owner-uid; prune must match on owner
// name so a successor without spec.resourceQuota can clear them.
func TestManagedNamespaceRecreateWithoutQuotaClearsStaleQuota(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	name := "team-a"
	mns := &api.ManagedNamespace{ObjectMeta: metav1.ObjectMeta{
		Name:       name,
		UID:        types.UID("new-uid"),
		Finalizers: []string{core.Finalizer},
		Labels:     map[string]string{core.LabelManagedBy: core.ManagedBy},
	}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			core.LabelManagedBy: core.ManagedBy,
			core.LabelOwnerName: name,
		},
	}}
	staleQuota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      core.QuotaName(name),
			Namespace: name,
			Labels: map[string]string{
				core.LabelManagedBy: core.ManagedBy,
				core.LabelOwnerUID:  "old-uid",
				core.LabelOwnerName: name,
			},
		},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourcePods: resource.MustParse("10"),
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mns, ns, staleQuota).
		WithStatusSubresource(mns).Build()
	r := &ManagedNamespaceReconciler{Client: cl}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mns)}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(staleQuota), &corev1.ResourceQuota{}); err == nil {
		t.Fatal("stale resource quota should have been deleted")
	}
}
