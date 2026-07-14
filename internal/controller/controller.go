// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"encoding/json"
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"github.com/pliu/rbac-controller/internal/core"
	"github.com/pliu/rbac-controller/internal/directory"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

type Reconciler struct {
	client.Client
	Resolver       directory.Resolver
	Namespace      string
	Refresh, Stale time.Duration
}

func (r *Reconciler) Reconcile(ctx context.Context, q ctrl.Request) (ctrl.Result, error) {
	if q.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}
	var sa corev1.ServiceAccount
	if e := r.Get(ctx, q.NamespacedName, &sa); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}
	u := sa.Annotations[core.AnnUsername]
	if u == "" || sa.Labels[core.LabelManagedBy] != core.ManagedBy {
		return ctrl.Result{}, nil
	}
	if !sa.DeletionTimestamp.IsZero() {
		if e := r.cleanup(ctx, &sa, nil, nil); e != nil {
			return ctrl.Result{}, e
		}
		b := sa.DeepCopy()
		controllerutil.RemoveFinalizer(&sa, core.Finalizer)
		return ctrl.Result{}, r.Patch(ctx, &sa, client.MergeFrom(b))
	}
	if !controllerutil.ContainsFinalizer(&sa, core.Finalizer) {
		b := sa.DeepCopy()
		controllerutil.AddFinalizer(&sa, core.Finalizer)
		return ctrl.Result{Requeue: true}, r.Patch(ctx, &sa, client.MergeFrom(b))
	}
	original := sa.DeepCopy()
	now := time.Now().UTC()
	exp, e := core.ParseTime(sa.Annotations[core.AnnExpires])
	if e != nil || !exp.After(now) {
		return ctrl.Result{}, r.Delete(ctx, &sa)
	}
	if r.Resolver == nil {
		r.Resolver = directory.None{}
	}
	if r.Refresh == 0 {
		r.Refresh = 5 * time.Minute
	}
	if r.Stale == 0 {
		r.Stale = 15 * time.Minute
	}
	groups := []string{}
	degraded := false
	dr, e := r.Resolver.Resolve(ctx, u)
	if e == nil {
		groups = dr.Groups
		raw, _ := json.Marshal(groups)
		sa.Annotations[core.AnnGroups] = string(raw)
		sa.Annotations[core.AnnDirectorySync] = dr.At.Format(time.RFC3339)
	} else {
		_ = json.Unmarshal([]byte(sa.Annotations[core.AnnGroups]), &groups)
		at, _ := core.ParseTime(sa.Annotations[core.AnnDirectorySync])
		if now.After(at.Add(r.Stale)) {
			groups = nil
			degraded = true
		}
	}
	var maps api.AccessMappingList
	if e = r.List(ctx, &maps); e != nil {
		return ctrl.Result{}, e
	}
	grants, digest := core.Evaluate(u, groups, maps.Items)
	wantR := map[types.NamespacedName]bool{}
	wantC := map[string]bool{}
	for _, g := range grants {
		var cr rbacv1.ClusterRole
		if e = r.Get(ctx, types.NamespacedName{Name: g.ClusterRole}, &cr); e != nil || cr.Labels[core.LabelManagedBy] != core.ManagedBy {
			degraded = true
			continue
		}
		if g.ClusterWide {
			name := core.BindingName(u, g.ClusterRole, "*")
			e = r.ensureClusterRoleBinding(ctx, &sa, name, g.ClusterRole)
			wantC[name] = true
		} else {
			name := core.BindingName(u, g.ClusterRole, g.Namespace)
			e = r.ensureRoleBinding(ctx, &sa, name, g.Namespace, g.ClusterRole)
			wantR[types.NamespacedName{Name: name, Namespace: g.Namespace}] = true
		}
		if e != nil {
			return ctrl.Result{}, e
		}
	}
	if e = r.cleanup(ctx, &sa, wantR, wantC); e != nil {
		return ctrl.Result{}, e
	}
	sa.Annotations[core.AnnState] = "Ready"
	if degraded {
		sa.Annotations[core.AnnState] = "Degraded"
	}
	sa.Annotations[core.AnnObserved] = sa.Annotations[core.AnnRevision]
	sa.Annotations[core.AnnPolicy] = digest
	if e = r.Patch(ctx, &sa, client.MergeFrom(original)); e != nil {
		return ctrl.Result{}, e
	}
	return ctrl.Result{RequeueAfter: r.Refresh}, nil
}
func (r *Reconciler) ensureRoleBinding(ctx context.Context, sa *corev1.ServiceAccount, name, namespace, role string) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = r.labels(sa)
		obj.RoleRef = want
		obj.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: sa.Namespace}}
		return nil
	})
	return e
}
func (r *Reconciler) ensureClusterRoleBinding(ctx context.Context, sa *corev1.ServiceAccount, name, role string) error {
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	obj := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if e := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); e == nil && obj.RoleRef != want {
		if e = r.Delete(ctx, obj); e != nil {
			return e
		}
		obj = &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	_, e := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = r.labels(sa)
		obj.RoleRef = want
		obj.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: sa.Namespace}}
		return nil
	})
	return e
}
func (r *Reconciler) labels(sa *corev1.ServiceAccount) map[string]string {
	return map[string]string{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerUID: string(sa.UID), core.LabelCredential: sa.Name}
}
func (r *Reconciler) cleanup(ctx context.Context, sa *corev1.ServiceAccount, wr map[types.NamespacedName]bool, wc map[string]bool) error {
	sel := client.MatchingLabels{core.LabelManagedBy: core.ManagedBy, core.LabelOwnerUID: string(sa.UID)}
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
func (r *Reconciler) all(ctx context.Context, _ client.Object) []reconcile.Request {
	var l corev1.ServiceAccountList
	if r.List(ctx, &l, client.InNamespace(r.Namespace), client.MatchingLabels{core.LabelManagedBy: core.ManagedBy}) != nil {
		return nil
	}
	o := []reconcile.Request{}
	for i := range l.Items {
		o = append(o, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&l.Items[i])})
	}
	return o
}
func (r *Reconciler) credential(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetLabels()[core.LabelManagedBy] != core.ManagedBy {
		return nil
	}
	name := obj.GetLabels()[core.LabelCredential]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: r.Namespace}}}
}
func (r *Reconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).
		For(&corev1.ServiceAccount{}).
		Watches(&api.AccessMapping{}, handler.EnqueueRequestsFromMapFunc(r.all)).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(r.all)).
		Watches(&rbacv1.RoleBinding{}, handler.EnqueueRequestsFromMapFunc(r.credential)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(r.credential)).
		Complete(r)
}
