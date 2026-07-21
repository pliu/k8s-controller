# k8s-controller

k8s-controller reconciles a namespace and everything it grants from a single
`ManagedNamespace` custom resource.

A `ManagedNamespace`'s own name is the namespace it manages (so there is exactly
one per namespace). It carries optional Namespace `labels` and `annotations`, an
optional `ResourceQuota`, and a list of `accessMappings`; each mapping binds a
single **group** or a list of **users** to a set of ClusterRoles. The operator
keeps three things in sync:

- **the Namespace** — created if missing with the requested labels and
  annotations, which are kept in sync without removing metadata owned by other
  actors. It is never deleted by the operator; deleting a `ManagedNamespace`
  removes only the RoleBindings it owns, leaving the namespace, its
  ResourceQuota, and workloads in place.
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

```sh
make verify
make kind-test # requires Docker, kind, and kubectl
kubectl apply -k config
kubectl apply -f config/sample.yaml
```

The image ships one binary. It runs both the reconciler and the HTTP API by
default; pass `-controller` or `-server` to run only one. The default manifests
deploy the combined mode, with the reconciler under leader election and the API
served by every replica. Reconciliation is watch-driven; `-sync-period` (default
`1h`) sets how often every `ManagedNamespace` is re-synced as a drift-repair
safety net. `-listen` (default `:8080`) sets the HTTP bind address.

Prometheus metrics are served at `/metrics` on the listen address in every mode,
from one shared registry: controller-runtime's reconcile/workqueue/client
series, Go and process collectors, the viewer's request count and latency
(`k8s_controller_http_*`), and `k8s_controller_invalid_references` reporting
each resource's currently-invalid ClusterRole references. In combined and
server-only modes the HTTP server serves it; in controller-only mode the
manager's own metrics listener binds the same address, so the scrape target is
identical everywhere.

The server exposes `GET /api/v1/managednamespaces` and
`GET /api/v1/clusteraccessmappings` plus an embedded UI (`internal/server/ui`)
with three views: a **Namespaces** list where selecting a namespace shows its
ResourceQuota and permission mappings (users/groups → ClusterRoles), a **Cluster
access** view listing the cluster-wide mappings, and a **Search** view that,
given a username or group, lists the namespaces (and any cluster-wide scope) and
ClusterRoles they are granted. It performs no writes and no authentication;
restrict access to it with a NetworkPolicy or an authenticating proxy as your
environment requires. The `group` values must match the stable group identifiers
your cluster's authenticator asserts to kube-apiserver.

The project is licensed under GNU AGPL version 3 only.
