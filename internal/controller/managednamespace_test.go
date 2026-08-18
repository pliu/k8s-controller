// SPDX-License-Identifier: Apache-2.0
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
			Labels:      map[string]string{"team": "a"},
			Annotations: map[string]string{"contact": "alice"},
		},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"external-label":     "keep",
			"existing-different": "namespace",
			"existing-same":      "same",
		},
		Annotations: map[string]string{
			"external-annotation": "keep",
			"existing-different":  "namespace",
			"existing-same":       "same",
		},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	r := &ManagedNamespaceReconciler{Client: cl}
	ctx := context.Background()

	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}
	var got corev1.Namespace
	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	// Existing keys are untouched until the ManagedNamespace requests them.
	if got.Labels["existing-different"] != "namespace" || got.Labels["existing-same"] != "same" {
		t.Fatalf("unmanaged labels were not preserved: %#v", got.Labels)
	}
	if got.Annotations["existing-different"] != "namespace" || got.Annotations["existing-same"] != "same" {
		t.Fatalf("unmanaged annotations were not preserved: %#v", got.Annotations)
	}

	mns.Spec.Labels["existing-different"] = "managed"
	mns.Spec.Labels["existing-same"] = "same"
	mns.Spec.Annotations["existing-different"] = "managed"
	mns.Spec.Annotations["existing-same"] = "same"
	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	// Adding an existing key adopts it, whether its old value differs or not.
	if got.Labels["existing-different"] != "managed" || got.Labels["existing-same"] != "same" {
		t.Fatalf("pre-existing labels were not reconciled: %#v", got.Labels)
	}
	if got.Annotations["existing-different"] != "managed" || got.Annotations["existing-same"] != "same" {
		t.Fatalf("pre-existing annotations were not reconciled: %#v", got.Annotations)
	}

	got.Labels["team"] = "drifted"
	got.Annotations["contact"] = "drifted"
	if err := cl.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	// Out-of-band changes to adopted keys are repaired.
	if got.Labels["team"] != "a" || got.Annotations["contact"] != "alice" {
		t.Fatalf("metadata drift was not repaired: labels=%#v annotations=%#v", got.Labels, got.Annotations)
	}

	mns.Spec.Labels = map[string]string{"team": "b"}
	mns.Spec.Annotations = map[string]string{"contact": "bob"}
	if err := r.ensureNamespace(ctx, mns); err != nil {
		t.Fatal(err)
	}

	if err := cl.Get(ctx, client.ObjectKeyFromObject(ns), &got); err != nil {
		t.Fatal(err)
	}
	// Removing adopted keys from the spec removes them from the Namespace,
	// while keys that were never requested remain.
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
	wantAnnotations[core.AnnotationManagedMetadata] = `{"labels":["team"],"annotations":["contact"]}`
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
	wantAnnotations = map[string]string{
		"external-annotation":          "keep",
		core.AnnotationManagedMetadata: `{}`,
	}
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
			Name:      "compute",
			Namespace: mns.Name,
			Labels: map[string]string{
				core.LabelManagedBy: core.ManagedBy,
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
// by the previous CR are still labelled with that name; prune must match on
// owner name so a successor without spec.resourceQuotas can clear them.
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
			Name:      "compute",
			Namespace: name,
			Labels: map[string]string{
				core.LabelManagedBy: core.ManagedBy,
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

func TestManagedNamespaceReconcilesMultipleQuotas(t *testing.T) {
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
	scoped := &corev1.ScopeSelector{MatchExpressions: []corev1.ScopedResourceSelectorRequirement{{
		ScopeName: corev1.ResourceQuotaScopePriorityClass,
		Operator:  corev1.ScopeSelectorOpIn,
		Values:    []string{"low"},
	}}}
	mns := &api.ManagedNamespace{ObjectMeta: metav1.ObjectMeta{
		Name:       name,
		UID:        types.UID("mns-uid"),
		Finalizers: []string{core.Finalizer},
		Labels:     map[string]string{core.LabelManagedBy: core.ManagedBy},
	}, Spec: api.ManagedNamespaceSpec{
		ResourceQuotas: []api.ResourceQuota{
			{
				Name: "compute",
				ResourceQuotaSpec: corev1.ResourceQuotaSpec{
					Hard:   corev1.ResourceList{corev1.ResourcePods: resource.MustParse("50")},
					Scopes: []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeNotBestEffort},
				},
			},
			{
				Name: "low-priority",
				ResourceQuotaSpec: corev1.ResourceQuotaSpec{
					Hard:          corev1.ResourceList{corev1.ResourcePods: resource.MustParse("10")},
					ScopeSelector: scoped,
				},
			},
		},
	}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			core.LabelManagedBy: core.ManagedBy,
			core.LabelOwnerName: name,
		},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mns, ns).
		WithStatusSubresource(mns).Build()
	r := &ManagedNamespaceReconciler{Client: cl}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mns)}); err != nil {
		t.Fatal(err)
	}

	var compute, low corev1.ResourceQuota
	if err := cl.Get(ctx, client.ObjectKey{Namespace: name, Name: "compute"}, &compute); err != nil {
		t.Fatalf("compute quota: %v", err)
	}
	if pods, ok := compute.Spec.Hard[corev1.ResourcePods]; !ok || pods.Cmp(resource.MustParse("50")) != 0 {
		t.Errorf("compute hard = %#v", compute.Spec.Hard)
	}
	if !reflect.DeepEqual(compute.Spec.Scopes, []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeNotBestEffort}) {
		t.Errorf("compute scopes = %#v", compute.Spec.Scopes)
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: name, Name: "low-priority"}, &low); err != nil {
		t.Fatalf("low-priority quota: %v", err)
	}
	if !reflect.DeepEqual(low.Spec.ScopeSelector, scoped) {
		t.Errorf("low-priority scopeSelector = %#v", low.Spec.ScopeSelector)
	}

	if err := cl.Get(ctx, client.ObjectKeyFromObject(mns), mns); err != nil {
		t.Fatal(err)
	}
	mns.Spec.ResourceQuotas = []api.ResourceQuota{{
		Name: "compute",
		ResourceQuotaSpec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("25")},
		},
	}}
	if err := cl.Update(ctx, mns); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mns)}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(&low), &corev1.ResourceQuota{}); err == nil {
		t.Fatal("removed quota should have been deleted")
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: name, Name: "compute"}, &compute); err != nil {
		t.Fatal(err)
	}
	if pods := compute.Spec.Hard[corev1.ResourcePods]; pods.Cmp(resource.MustParse("25")) != 0 {
		t.Errorf("updated compute hard = %#v", compute.Spec.Hard)
	}
	if len(compute.Spec.Scopes) != 0 {
		t.Errorf("cleared scopes still set: %#v", compute.Spec.Scopes)
	}
}
