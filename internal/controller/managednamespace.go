// SPDX-License-Identifier: Apache-2.0
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/pliu/k8s-controller/internal/core"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ManagedNamespaceReconciler keeps the Namespace, ResourceQuota, and
// RoleBindings implied by each ManagedNamespace in sync. It creates the
// namespace if missing but never deletes it: on ManagedNamespace deletion the
// RoleBindings it owns are removed, while the Namespace and any ResourceQuota
// are left in place so existing workloads keep their resource limits.
// Referenced ClusterRoles are not managed here; a missing one fails closed and
// is reported in status.
type ManagedNamespaceReconciler struct{ client.Client }

func (r *ManagedNamespaceReconciler) Reconcile(ctx context.Context, q ctrl.Request) (ctrl.Result, error) {
	var mns api.ManagedNamespace
	if e := r.Get(ctx, q.NamespacedName, &mns); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}
	if !mns.DeletionTimestamp.IsZero() {
		if e := r.pruneBindings(ctx, &mns, nil); e != nil {
			return ctrl.Result{}, e
		}
		// The namespace and its ResourceQuota are intentionally left in place.
		invalidReferences.DeleteLabelValues("managednamespace", mns.Name)
		base := mns.DeepCopy()
		controllerutil.RemoveFinalizer(&mns, core.Finalizer)
		return ctrl.Result{}, r.Patch(ctx, &mns, client.MergeFrom(base))
	}
	if mns.Labels[core.LabelManagedBy] != core.ManagedBy || !controllerutil.ContainsFinalizer(&mns, core.Finalizer) {
		base := mns.DeepCopy()
		if mns.Labels == nil {
			mns.Labels = map[string]string{}
		}
		mns.Labels[core.LabelManagedBy] = core.ManagedBy
		controllerutil.AddFinalizer(&mns, core.Finalizer)
		return ctrl.Result{Requeue: true}, r.Patch(ctx, &mns, client.MergeFrom(base))
	}

	invalid, syncErr := r.sync(ctx, &mns)
	// status runs on both paths so a spec the API server rejects is reported on
	// the object, not only in the controller log.
	if e := r.status(ctx, &mns, invalid, syncErr); e != nil {
		return ctrl.Result{}, errors.Join(syncErr, e)
	}
	return ctrl.Result{}, terminal(syncErr)
}

func (r *ManagedNamespaceReconciler) sync(ctx context.Context, mns *api.ManagedNamespace) ([]api.InvalidReference, error) {
	if e := r.ensureNamespace(ctx, mns); e != nil {
		return nil, e
	}
	if e := r.reconcileQuota(ctx, mns); e != nil {
		return nil, e
	}
	return r.reconcileBindings(ctx, mns)
}

func (r *ManagedNamespaceReconciler) ensureNamespace(ctx context.Context, mns *api.ManagedNamespace) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mns.Name}}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		previous, e := namespaceMetadataInventory(ns)
		if e != nil {
			return e
		}
		for _, key := range previous.Labels {
			delete(ns.Labels, key)
		}
		for _, key := range previous.Annotations {
			delete(ns.Annotations, key)
		}

		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		maps.Copy(ns.Labels, mns.Spec.Labels)
		ns.Labels[core.LabelManagedBy] = core.ManagedBy
		ns.Labels[core.LabelOwnerName] = mns.Name

		if ns.Annotations == nil {
			ns.Annotations = map[string]string{}
		}
		maps.Copy(ns.Annotations, mns.Spec.Annotations)
		inventory := namespaceMetadata{
			Labels:      slices.Sorted(maps.Keys(mns.Spec.Labels)),
			Annotations: slices.Sorted(maps.Keys(mns.Spec.Annotations)),
		}
		// The inventory annotation is controller-owned, even if the same key
		// was supplied in spec.annotations.
		inventory.Annotations = slices.DeleteFunc(inventory.Annotations, func(key string) bool {
			return key == core.AnnotationManagedMetadata
		})
		data, e := json.Marshal(inventory)
		if e != nil {
			return e
		}
		ns.Annotations[core.AnnotationManagedMetadata] = string(data)
		return nil
	})
	return e
}

type namespaceMetadata struct {
	Labels      []string `json:"labels,omitempty"`
	Annotations []string `json:"annotations,omitempty"`
}

func namespaceMetadataInventory(ns *corev1.Namespace) (namespaceMetadata, error) {
	var inventory namespaceMetadata
	data := ns.Annotations[core.AnnotationManagedMetadata]
	if data == "" {
		return inventory, nil
	}
	if e := json.Unmarshal([]byte(data), &inventory); e != nil {
		return inventory, fmt.Errorf("decode %s annotation on Namespace %q: %w", core.AnnotationManagedMetadata, ns.Name, e)
	}
	return inventory, nil
}

func (r *ManagedNamespaceReconciler) reconcileQuota(ctx context.Context, mns *api.ManagedNamespace) error {
	keep := ""
	if mns.Spec.ResourceQuota != nil {
		keep = core.QuotaName(mns.Name)
	}
	if e := r.pruneQuotas(ctx, mns, keep); e != nil {
		return e
	}
	if keep == "" {
		return nil
	}
	rq := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: keep, Namespace: mns.Name}}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, rq, func() error {
		rq.Labels = r.labels(mns)
		rq.Spec = corev1.ResourceQuotaSpec{Hard: mns.Spec.ResourceQuota.Hard}
		return nil
	})
	return e
}

// pruneQuotas deletes managed ResourceQuotas other than keep in the current
// namespace. keep == "" removes all managed quotas for this name.
//
// Matching uses LabelOwnerName (not UID) so a recreated ManagedNamespace can
// reclaim or clear a quota left behind by a previous CR with the same name.
func (r *ManagedNamespaceReconciler) pruneQuotas(ctx context.Context, mns *api.ManagedNamespace, keep string) error {
	var list corev1.ResourceQuotaList
	if e := r.List(ctx, &list, client.MatchingLabels{
		core.LabelManagedBy: core.ManagedBy,
		core.LabelOwnerName: mns.Name,
	}); e != nil {
		return e
	}
	for i := range list.Items {
		it := &list.Items[i]
		if keep == "" || it.Name != keep || it.Namespace != mns.Name {
			if e := client.IgnoreNotFound(r.Delete(ctx, it)); e != nil {
				return e
			}
		}
	}
	return nil
}

func (r *ManagedNamespaceReconciler) reconcileBindings(ctx context.Context, mns *api.ManagedNamespace) ([]api.InvalidReference, error) {
	invalid := []api.InvalidReference{}
	want := map[types.NamespacedName]bool{}
	for _, am := range mns.Spec.AccessMappings {
		subjects := subjectsFor(am)
		if len(subjects) == 0 {
			continue
		}
		key := subjectKey(am)
		for _, role := range am.ClusterRoles {
			var cr rbacv1.ClusterRole
			if e := r.Get(ctx, client.ObjectKey{Name: role}, &cr); e != nil {
				if !apierrors.IsNotFound(e) {
					return nil, e
				}
				invalid = append(invalid, api.InvalidReference{ClusterRole: role, Reason: "ClusterRole not found"})
				continue
			}
			name := core.BindingName(mns.Name, key, role)
			if e := r.ensureRoleBinding(ctx, mns, name, role, subjects); e != nil {
				return nil, e
			}
			want[types.NamespacedName{Namespace: mns.Name, Name: name}] = true
		}
	}
	if e := r.pruneBindings(ctx, mns, want); e != nil {
		return nil, e
	}
	return invalid, nil
}

func (r *ManagedNamespaceReconciler) ensureRoleBinding(ctx context.Context, mns *api.ManagedNamespace, name, role string, subjects []rbacv1.Subject) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: mns.Name}}
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: mns.Name}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = r.labels(mns)
		obj.RoleRef = want
		obj.Subjects = subjects
		return nil
	})
	return e
}

// pruneBindings deletes owned RoleBindings not in want. want == nil removes all
// owned bindings.
func (r *ManagedNamespaceReconciler) pruneBindings(ctx context.Context, mns *api.ManagedNamespace, want map[types.NamespacedName]bool) error {
	var list rbacv1.RoleBindingList
	if e := r.List(ctx, &list, r.ownedBy(mns)); e != nil {
		return e
	}
	for i := range list.Items {
		if want == nil || !want[client.ObjectKeyFromObject(&list.Items[i])] {
			if e := client.IgnoreNotFound(r.Delete(ctx, &list.Items[i])); e != nil {
				return e
			}
		}
	}
	return nil
}

// status reports the outcome of a sync pass. observedGeneration, the
// invalid-reference list, and the gauge are refreshed only when the pass
// completed, so a failure part-way through cannot erase what the last good pass
// found; the Ready condition always carries the current generation, which is
// what distinguishes "failing on this spec" from a stale condition.
func (r *ManagedNamespaceReconciler) status(ctx context.Context, mns *api.ManagedNamespace, invalid []api.InvalidReference, syncErr error) error {
	base := mns.DeepCopy()
	status, reason, message := metav1.ConditionTrue, "Reconciled", "Namespace, quota, and bindings are in sync"
	switch {
	case syncErr != nil:
		status, reason, message = metav1.ConditionFalse, "SyncFailed", conditionMessage(syncErr)
	case len(invalid) > 0:
		status, reason, message = metav1.ConditionFalse, "InvalidReferences", "One or more ClusterRole references are invalid"
	}
	if syncErr == nil {
		invalidReferences.WithLabelValues("managednamespace", mns.Name).Set(float64(len(invalid)))
		mns.Status.ObservedGeneration = mns.Generation
		mns.Status.InvalidReferences = invalid
	}
	meta.SetStatusCondition(&mns.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: mns.Generation})
	return r.Status().Patch(ctx, mns, client.MergeFrom(base))
}

func (r *ManagedNamespaceReconciler) labels(mns *api.ManagedNamespace) map[string]string {
	return ownerLabels(mns.UID, mns.Name)
}

func (r *ManagedNamespaceReconciler) ownedBy(mns *api.ManagedNamespace) client.MatchingLabels {
	return client.MatchingLabels{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerUID: string(mns.UID)}
}

func (r *ManagedNamespaceReconciler) all(ctx context.Context, _ client.Object) []reconcile.Request {
	var l api.ManagedNamespaceList
	if r.List(ctx, &l) != nil {
		return nil
	}
	o := make([]reconcile.Request, 0, len(l.Items))
	for i := range l.Items {
		o = append(o, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&l.Items[i])})
	}
	return o
}

func (r *ManagedNamespaceReconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).
		For(&api.ManagedNamespace{}).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(r.all)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(ownerRequests)).
		Watches(&corev1.ResourceQuota{}, handler.EnqueueRequestsFromMapFunc(ownerRequests)).
		Watches(&rbacv1.RoleBinding{}, handler.EnqueueRequestsFromMapFunc(ownerRequests)).
		Complete(r)
}
