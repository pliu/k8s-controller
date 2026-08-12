// SPDX-License-Identifier: Apache-2.0
package controller

import (
	"context"
	"errors"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	"github.com/pliu/k8s-controller/internal/core"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ClusterAccessReconciler converges the ClusterRoleBindings implied by each
// ClusterAccessMapping: a single group or user list bound cluster-wide to a set
// of ClusterRoles. Referenced ClusterRoles are not managed here; a missing one
// fails closed and is reported in status.
type ClusterAccessReconciler struct{ client.Client }

func (r *ClusterAccessReconciler) Reconcile(ctx context.Context, q ctrl.Request) (ctrl.Result, error) {
	var cam api.ClusterAccessMapping
	if e := r.Get(ctx, q.NamespacedName, &cam); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}
	if !cam.DeletionTimestamp.IsZero() {
		if e := r.prune(ctx, &cam, nil); e != nil {
			return ctrl.Result{}, e
		}
		invalidReferences.DeleteLabelValues("clusteraccessmapping", cam.Name)
		base := cam.DeepCopy()
		controllerutil.RemoveFinalizer(&cam, core.Finalizer)
		return ctrl.Result{}, r.Patch(ctx, &cam, client.MergeFrom(base))
	}
	if cam.Labels[core.LabelManagedBy] != core.ManagedBy || !controllerutil.ContainsFinalizer(&cam, core.Finalizer) {
		base := cam.DeepCopy()
		if cam.Labels == nil {
			cam.Labels = map[string]string{}
		}
		cam.Labels[core.LabelManagedBy] = core.ManagedBy
		controllerutil.AddFinalizer(&cam, core.Finalizer)
		return ctrl.Result{Requeue: true}, r.Patch(ctx, &cam, client.MergeFrom(base))
	}

	invalid, syncErr := r.sync(ctx, &cam)
	// status runs on both paths so a spec the API server rejects is reported on
	// the object, not only in the controller log.
	if e := r.status(ctx, &cam, invalid, syncErr); e != nil {
		return ctrl.Result{}, errors.Join(syncErr, e)
	}
	return ctrl.Result{}, terminal(syncErr)
}

func (r *ClusterAccessReconciler) sync(ctx context.Context, cam *api.ClusterAccessMapping) ([]api.InvalidReference, error) {
	subjects := subjectsFor(cam.Spec)
	invalid := []api.InvalidReference{}
	want := map[string]bool{}
	if len(subjects) > 0 {
		key := subjectKey(cam.Spec)
		for _, role := range cam.Spec.ClusterRoles {
			var cr rbacv1.ClusterRole
			if e := r.Get(ctx, client.ObjectKey{Name: role}, &cr); e != nil {
				if !apierrors.IsNotFound(e) {
					return nil, e
				}
				invalid = append(invalid, api.InvalidReference{ClusterRole: role, Reason: "ClusterRole not found"})
				continue
			}
			name := core.BindingName(cam.Name, key, role)
			if e := r.ensure(ctx, cam, name, role, subjects); e != nil {
				return nil, e
			}
			want[name] = true
		}
	}
	if e := r.prune(ctx, cam, want); e != nil {
		return nil, e
	}
	return invalid, nil
}

func (r *ClusterAccessReconciler) ensure(ctx context.Context, cam *api.ClusterAccessMapping, name, role string, subjects []rbacv1.Subject) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	// Replace rather than update, for the reason ensureRoleBinding explains.
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = ownerLabels(cam.Name)
		obj.RoleRef = want
		obj.Subjects = subjects
		return nil
	})
	return e
}

// prune deletes owned ClusterRoleBindings not in want. want == nil removes all
// owned bindings.
//
// Matching uses LabelOwnerName so a ClusterAccessMapping recreated under the
// same name reclaims and clears what its predecessor left behind. Generated
// names derive from the owner's name, so a successor adopts the bindings it
// still wants; keyed on anything narrower than the name, the ones it no longer
// wants would stay invisible here, granting access nothing references.
func (r *ClusterAccessReconciler) prune(ctx context.Context, cam *api.ClusterAccessMapping, want map[string]bool) error {
	var list rbacv1.ClusterRoleBindingList
	if e := r.List(ctx, &list, client.MatchingLabels{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerName: cam.Name}); e != nil {
		return e
	}
	for i := range list.Items {
		if want == nil || !want[list.Items[i].Name] {
			if e := client.IgnoreNotFound(r.Delete(ctx, &list.Items[i])); e != nil {
				return e
			}
		}
	}
	return nil
}

// status reports the outcome of a sync pass, on the same terms as the
// ManagedNamespace reconciler's: only a completed pass refreshes
// observedGeneration, the invalid-reference list, and the gauge.
func (r *ClusterAccessReconciler) status(ctx context.Context, cam *api.ClusterAccessMapping, invalid []api.InvalidReference, syncErr error) error {
	base := cam.DeepCopy()
	status, reason, message := metav1.ConditionTrue, "Reconciled", "ClusterRoleBindings are in sync"
	switch {
	case syncErr != nil:
		status, reason, message = metav1.ConditionFalse, "SyncFailed", conditionMessage(syncErr)
	case len(invalid) > 0:
		status, reason, message = metav1.ConditionFalse, "InvalidReferences", "One or more ClusterRole references are invalid"
	}
	if syncErr == nil {
		invalidReferences.WithLabelValues("clusteraccessmapping", cam.Name).Set(float64(len(invalid)))
		cam.Status.ObservedGeneration = cam.Generation
		cam.Status.InvalidReferences = invalid
	}
	meta.SetStatusCondition(&cam.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: cam.Generation})
	return r.Status().Patch(ctx, cam, client.MergeFrom(base))
}

func (r *ClusterAccessReconciler) all(ctx context.Context, _ client.Object) []reconcile.Request {
	var l api.ClusterAccessMappingList
	if r.List(ctx, &l) != nil {
		return nil
	}
	o := make([]reconcile.Request, 0, len(l.Items))
	for i := range l.Items {
		o = append(o, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&l.Items[i])})
	}
	return o
}

func (r *ClusterAccessReconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).
		For(&api.ClusterAccessMapping{}).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(r.all)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(ownerRequests)).
		Complete(r)
}
