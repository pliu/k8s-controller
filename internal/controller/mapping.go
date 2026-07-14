// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"encoding/json"

	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"github.com/pliu/rbac-controller/internal/core"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type MappingReconciler struct{ client.Client }

func (r *MappingReconciler) Reconcile(ctx context.Context, q ctrl.Request) (ctrl.Result, error) {
	var mapping api.AccessMapping
	if e := r.Get(ctx, q.NamespacedName, &mapping); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}
	if mapping.Labels[core.LabelManagedBy] != core.ManagedBy {
		before := mapping.DeepCopy()
		if mapping.Labels == nil {
			mapping.Labels = map[string]string{}
		}
		mapping.Labels[core.LabelManagedBy] = core.ManagedBy
		return ctrl.Result{Requeue: true}, r.Patch(ctx, &mapping, client.MergeFrom(before))
	}
	invalid := []api.InvalidReference{}
	for _, grant := range mapping.Spec.ClusterRoles {
		var role rbacv1.ClusterRole
		if e := r.Get(ctx, client.ObjectKey{Name: grant.Name}, &role); e != nil {
			invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Reason: "ClusterRole not found"})
			continue
		}
		if role.Labels[core.LabelManagedBy] != core.ManagedBy {
			invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Reason: "ClusterRole is not managed by this controller"})
		}
		for _, name := range grant.Namespaces {
			var namespace corev1.Namespace
			e := r.Get(ctx, client.ObjectKey{Name: name}, &namespace)
			if apierrors.IsNotFound(e) {
				invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Namespace: name, Reason: "Namespace not found"})
			} else if e != nil {
				return ctrl.Result{}, e
			} else if !namespace.DeletionTimestamp.IsZero() {
				invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Namespace: name, Reason: "Namespace is terminating"})
			}
		}
	}
	raw, _ := json.Marshal(mapping.Spec)
	before := mapping.DeepCopy()
	mapping.Status.ObservedGeneration = mapping.Generation
	mapping.Status.InvalidReferences = invalid
	mapping.Status.PolicyDigest = core.Hash(string(raw))
	status, reason, message := metav1.ConditionTrue, "ReferencesValid", "All references are valid"
	if len(invalid) > 0 {
		status, reason, message = metav1.ConditionFalse, "InvalidReferences", "One or more grants have invalid references"
	}
	meta.SetStatusCondition(&mapping.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: mapping.Generation})
	return ctrl.Result{}, r.Status().Patch(ctx, &mapping, client.MergeFrom(before))
}

func (r *MappingReconciler) allMappings(ctx context.Context, _ client.Object) []reconcile.Request {
	var mappings api.AccessMappingList
	if r.List(ctx, &mappings) != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(mappings.Items))
	for i := range mappings.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&mappings.Items[i])})
	}
	return requests
}

func (r *MappingReconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).
		For(&api.AccessMapping{}).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(r.allMappings)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.allMappings)).
		Complete(r)
}
