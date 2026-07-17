// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"encoding/json"

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

// Reconciler converges the RoleBindings and ClusterRoleBindings implied by each
// AccessMapping. A mapping's usernames and AD groups become User and Group
// binding subjects directly, so Kubernetes evaluates them against the
// authenticated identity on every request; the controller manages no
// ServiceAccounts or credentials.
type Reconciler struct{ client.Client }

func (r *Reconciler) Reconcile(ctx context.Context, q ctrl.Request) (ctrl.Result, error) {
	var mapping api.AccessMapping
	if e := r.Get(ctx, q.NamespacedName, &mapping); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}
	if !mapping.DeletionTimestamp.IsZero() {
		if e := r.cleanup(ctx, &mapping, nil, nil); e != nil {
			return ctrl.Result{}, e
		}
		base := mapping.DeepCopy()
		controllerutil.RemoveFinalizer(&mapping, core.Finalizer)
		return ctrl.Result{}, r.Patch(ctx, &mapping, client.MergeFrom(base))
	}
	if mapping.Labels[core.LabelManagedBy] != core.ManagedBy || !controllerutil.ContainsFinalizer(&mapping, core.Finalizer) {
		base := mapping.DeepCopy()
		if mapping.Labels == nil {
			mapping.Labels = map[string]string{}
		}
		mapping.Labels[core.LabelManagedBy] = core.ManagedBy
		controllerutil.AddFinalizer(&mapping, core.Finalizer)
		return ctrl.Result{Requeue: true}, r.Patch(ctx, &mapping, client.MergeFrom(base))
	}

	subjects := subjectsFor(&mapping)
	invalid := []api.InvalidReference{}
	wantR := map[types.NamespacedName]bool{}
	wantC := map[string]bool{}
	for _, grant := range mapping.Spec.ClusterRoles {
		// ClusterRoles are not managed by this operator; a mapping may reference
		// any existing ClusterRole. A missing one fails closed (no binding).
		var role rbacv1.ClusterRole
		if e := r.Get(ctx, client.ObjectKey{Name: grant.Name}, &role); e != nil {
			if !apierrors.IsNotFound(e) {
				return ctrl.Result{}, e
			}
			invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Reason: "ClusterRole not found"})
			continue
		}
		if grant.ClusterWide {
			name := core.BindingName(mapping.Name, grant.Name, "*")
			if e := r.ensureClusterRoleBinding(ctx, &mapping, name, grant.Name, subjects); e != nil {
				return ctrl.Result{}, e
			}
			wantC[name] = true
			continue
		}
		for _, ns := range grant.Namespaces {
			var namespace corev1.Namespace
			e := r.Get(ctx, client.ObjectKey{Name: ns}, &namespace)
			if apierrors.IsNotFound(e) {
				invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Namespace: ns, Reason: "Namespace not found"})
				continue
			} else if e != nil {
				return ctrl.Result{}, e
			} else if !namespace.DeletionTimestamp.IsZero() {
				invalid = append(invalid, api.InvalidReference{ClusterRole: grant.Name, Namespace: ns, Reason: "Namespace is terminating"})
				continue
			}
			name := core.BindingName(mapping.Name, grant.Name, ns)
			if e := r.ensureRoleBinding(ctx, &mapping, name, ns, grant.Name, subjects); e != nil {
				return ctrl.Result{}, e
			}
			wantR[types.NamespacedName{Name: name, Namespace: ns}] = true
		}
	}
	if e := r.cleanup(ctx, &mapping, wantR, wantC); e != nil {
		return ctrl.Result{}, e
	}
	return ctrl.Result{}, r.status(ctx, &mapping, invalid)
}

func subjectsFor(m *api.AccessMapping) []rbacv1.Subject {
	subjects := make([]rbacv1.Subject, 0, len(m.Spec.Usernames)+len(m.Spec.Groups))
	for _, u := range m.Spec.Usernames {
		subjects = append(subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: u})
	}
	for _, g := range m.Spec.Groups {
		subjects = append(subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "Group", Name: g})
	}
	return subjects
}

func (r *Reconciler) status(ctx context.Context, mapping *api.AccessMapping, invalid []api.InvalidReference) error {
	base := mapping.DeepCopy()
	raw, _ := json.Marshal(mapping.Spec)
	mapping.Status.ObservedGeneration = mapping.Generation
	mapping.Status.InvalidReferences = invalid
	mapping.Status.PolicyDigest = core.Hash(string(raw))
	status, reason, message := metav1.ConditionTrue, "ReferencesValid", "All references are valid"
	if len(invalid) > 0 {
		status, reason, message = metav1.ConditionFalse, "InvalidReferences", "One or more grants have invalid references"
	}
	meta.SetStatusCondition(&mapping.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: mapping.Generation})
	return r.Status().Patch(ctx, mapping, client.MergeFrom(base))
}

func (r *Reconciler) ensureRoleBinding(ctx context.Context, m *api.AccessMapping, name, namespace, role string, subjects []rbacv1.Subject) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = r.labels(m)
		obj.RoleRef = want
		obj.Subjects = subjects
		return nil
	})
	return e
}

func (r *Reconciler) ensureClusterRoleBinding(ctx context.Context, m *api.AccessMapping, name, role string, subjects []rbacv1.Subject) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = r.labels(m)
		obj.RoleRef = want
		obj.Subjects = subjects
		return nil
	})
	return e
}

func (r *Reconciler) labels(m *api.AccessMapping) map[string]string {
	return map[string]string{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerUID: string(m.UID), core.LabelOwnerName: m.Name}
}

// cleanup deletes generated bindings owned by the mapping that are no longer
// wanted. When wr and wc are nil (the mapping is being deleted) every owned
// binding is removed.
func (r *Reconciler) cleanup(ctx context.Context, m *api.AccessMapping, wr map[types.NamespacedName]bool, wc map[string]bool) error {
	sel := client.MatchingLabels{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerUID: string(m.UID)}
	var rl rbacv1.RoleBindingList
	if e := r.List(ctx, &rl, sel); e != nil {
		return e
	}
	for i := range rl.Items {
		if wr == nil || !wr[client.ObjectKeyFromObject(&rl.Items[i])] {
			if e := client.IgnoreNotFound(r.Delete(ctx, &rl.Items[i])); e != nil {
				return e
			}
		}
	}
	var cl rbacv1.ClusterRoleBindingList
	if e := r.List(ctx, &cl, sel); e != nil {
		return e
	}
	for i := range cl.Items {
		if wc == nil || !wc[cl.Items[i].Name] {
			if e := client.IgnoreNotFound(r.Delete(ctx, &cl.Items[i])); e != nil {
				return e
			}
		}
	}
	return nil
}

func (r *Reconciler) allMappings(ctx context.Context, _ client.Object) []reconcile.Request {
	var l api.AccessMappingList
	if r.List(ctx, &l) != nil {
		return nil
	}
	o := make([]reconcile.Request, 0, len(l.Items))
	for i := range l.Items {
		o = append(o, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&l.Items[i])})
	}
	return o
}

func (r *Reconciler) owner(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetLabels()[core.LabelManagedBy] != core.ManagedBy {
		return nil
	}
	name := obj.GetLabels()[core.LabelOwnerName]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}

func (r *Reconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).
		For(&api.AccessMapping{}).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(r.allMappings)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.allMappings)).
		Watches(&rbacv1.RoleBinding{}, handler.EnqueueRequestsFromMapFunc(r.owner)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(r.owner)).
		Complete(r)
}
