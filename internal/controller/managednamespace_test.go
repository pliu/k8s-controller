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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestManagedNamespaceDeletionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		labels    map[string]string
		wantExist bool
	}{
		{name: "ordinary ManagedNamespace retains namespace", wantExist: true},
		{name: "dev ManagedNamespace deletes namespace", labels: map[string]string{core.LabelDeleteNamespace: "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			mns := &api.ManagedNamespace{ObjectMeta: metav1.ObjectMeta{
				Name:              "user-alice",
				UID:               types.UID("mns-uid"),
				Labels:            tc.labels,
				Finalizers:        []string{core.Finalizer},
				DeletionTimestamp: &now,
			}}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mns.Name}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mns, ns).Build()
			r := &ManagedNamespaceReconciler{Client: cl}

			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mns)}); err != nil {
				t.Fatal(err)
			}
			err := cl.Get(context.Background(), client.ObjectKeyFromObject(ns), &corev1.Namespace{})
			if tc.wantExist && err != nil {
				t.Fatalf("namespace should exist: %v", err)
			}
			if !tc.wantExist && !apierrors.IsNotFound(err) {
				t.Fatalf("namespace should be deleted, got %v", err)
			}
		})
	}
}
