// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
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
