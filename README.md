# k8s-controller

k8s-controller reconciles a namespace and everything it grants from a single
`ManagedNamespace` custom resource.

A `ManagedNamespace`'s own name is the namespace it manages (so there is exactly
one per namespace). It carries optional Namespace `labels` and `annotations`, an
optional `ResourceQuota`, and a list of `accessMappings`; each mapping binds a
single **group** or a list of **users** to a set of ClusterRoles. The operator
keeps three things in sync:

- **the Namespace** — created if missing with the requested labels and
  annotations. Requested keys are adopted, kept in sync, and removed when they
  leave the spec; metadata never requested by the `ManagedNamespace` is
  preserved. It is never deleted by the operator; deleting a
  `ManagedNamespace` removes only the RoleBindings it owns, leaving the
  namespace, its ResourceQuota, and workloads in place.
- **one ResourceQuota** in that namespace, from `spec.resourceQuota` (cleared if
  the field is removed while the `ManagedNamespace` still exists; retained when
  the `ManagedNamespace` itself is deleted).
- **RoleBindings** in that namespace — one per `(accessMapping, clusterRole)`,
  with the group or users as `User`/`Group` subjects bound directly, so
  Kubernetes evaluates them against the authenticated identity on every request.

For cluster-wide grants, a `ClusterAccessMapping` is a cluster-scoped resource
whose spec is the same access mapping (a single **group** or a list of **users**
→ ClusterRoles). The operator reconciles one `ClusterRoleBinding` per referenced
ClusterRole, granting that access across every namespace.

ClusterRoles are not managed by this operator. A mapping may reference any
existing ClusterRole; the reusable ones you intend to grant should be installed
alongside the operator (ideally in the same chart/manifests) so they version
together. A referenced ClusterRole that does not exist makes that grant fail
closed. Policy is authored with `kubectl` or GitOps against the
`ManagedNamespace` and ClusterRole objects directly; the bundled HTTP server is a
read-only viewer.

## Security model

Both `ManagedNamespace` and `ClusterAccessMapping` are cluster-scoped, and write
access to either is equivalent to cluster-admin. The operator holds `bind` on
all ClusterRoles — the permission that exempts it from RBAC's
privilege-escalation check — so a mapping may reference any ClusterRole,
`cluster-admin` included, and bind it to any user or group. Restrict `create`,
`update`, and `patch` on both kinds to cluster administrators or to the GitOps
pipeline that owns cluster policy.

Being cluster-scoped, neither kind can be delegated per namespace with RBAC
alone. A ClusterRole with `resourceNames` can grant a team `get` and `patch` on
one pre-provisioned `ManagedNamespace`, but RBAC cannot restrict `create` by
name — the object name is not known to the authorizer for that verb — so
letting a team create its own is letting it create any, including one named
after a namespace they do not own. To hand these resources to teams, gate them
with a ValidatingAdmissionPolicy that ties `metadata.name` to the requester and
constrains `clusterRoles` to an allowlist.

Managing system namespaces and owning Pod Security Admission labels are both
intended uses, so neither namespace names nor label keys are restricted. Three
consequences deserve care. A `metadata.name` matching an existing namespace
adopts that namespace rather than failing, so a typo is a live change to a
namespace you did not mean to touch. A label the operator has adopted and then
loses from `spec.labels` is deleted from the namespace, not reverted to its
previous value — dropping a `pod-security.kubernetes.io/enforce` key from a spec
that once carried it leaves the namespace with no enforcement at all, which is
more permissive than where it started. And a `spec.resourceQuota` on a system
namespace makes the API server reject pods that omit resource requests, which
can keep cluster components from scheduling; since the quota is retained when
the `ManagedNamespace` is deleted, backing that out means deleting the
ResourceQuota by hand.

```sh
make verify
make kind-test # requires Docker, kind, and kubectl
kubectl apply -k config
kubectl apply -f config/sample.yaml
```

Uninstall in the reverse order: delete every `ManagedNamespace` and
`ClusterAccessMapping` first and let them finish terminating, then remove the
operator and the CRDs. The bindings are cleaned up by a finalizer, so tearing
down the operator first leaves the custom resources stuck terminating with
nothing to release them; force-removing the finalizers at that point deletes
the objects while their RoleBindings and ClusterRoleBindings stay behind,
still granting access. Recreating a resource under the same name reclaims and
cleans up whatever its predecessor left, which is the way back if this has
already happened.

The image ships one binary. The reconciler always runs; `-server` additionally
serves the read-only HTTP API and UI, so that unauthenticated surface is never
exposed unless it is asked for. The default manifests pass `-server`, with the
reconciler under leader election and the API served by every replica.
Reconciliation is watch-driven; `-sync-period` (default `1h`) sets how often
every `ManagedNamespace` is re-synced as a drift-repair safety net. `-listen`
(default `:8080`) sets the HTTP bind address.

Prometheus metrics are served at `/metrics` on the listen address either way,
from one shared registry: controller-runtime's reconcile/workqueue/client
series, Go and process collectors, the viewer's request count and latency
(`k8s_controller_http_*`), and `k8s_controller_invalid_references` reporting
each resource's currently-invalid ClusterRole references. With `-server` the
HTTP server serves it; without, the manager's own metrics listener binds the
same address, so the scrape target is identical either way.

The server exposes `GET /api/v1/managednamespaces` and
`GET /api/v1/clusteraccessmappings` plus an embedded UI (`internal/server/ui`)
with three views: a **Namespaces** list where selecting a namespace shows its
ResourceQuota and permission mappings (users/groups → ClusterRoles), a **Cluster
access** view listing the cluster-wide mappings, and a **Search** view that,
given a username or group, lists the namespaces (and any cluster-wide scope) and
ClusterRoles they are granted. Both endpoints read from a watch-fed cache, so a
request costs no API-server traffic and reflects the cluster as of the last
watch event. It performs no writes and no authentication; restrict access to it
with a NetworkPolicy or an authenticating proxy as your environment requires.
The `group` values must match the stable group identifiers your cluster's
authenticator asserts to kube-apiserver.

The project is licensed under the Apache License, Version 2.0.
