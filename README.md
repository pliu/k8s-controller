# RBAC Controller

RBAC Controller maps exact usernames and groups to reusable ClusterRoles by
reconciling native Kubernetes bindings from an `AccessMapping` custom resource.

Each `AccessMapping` entry produces namespace `RoleBinding`s or explicit
cluster-wide `ClusterRoleBinding`s. The mapping's usernames and groups become
`User` and `Group` binding subjects directly, so Kubernetes evaluates them
against the authenticated identity on every request. The controller manages no
ServiceAccounts and issues no credentials; editing or deleting a mapping
converges or removes its bindings.

ClusterRoles are not managed by this operator. A mapping may reference any
existing ClusterRole; the reusable ones you intend to grant should be installed
alongside the operator (ideally in the same chart/manifests) so they version
together. A referenced ClusterRole that does not exist makes that grant fail
closed. Policy is authored with `kubectl` or GitOps against the `AccessMapping`
and ClusterRole objects directly; the bundled HTTP server is a read-only viewer.

```sh
make verify
make kind-test # requires Docker, kind, kubectl, and curl
kubectl apply -k config
kubectl apply -f config/sample.yaml
```

The image ships one binary. It runs both the reconciler and the HTTP API by
default; pass `-controller` or `-server` to run only one. The default manifests
deploy the combined mode, with the reconciler under leader election and the API
served by every replica.

The server exposes `GET /api/v1/accessmappings` and `GET /api/v1/clusterroles`
(the ClusterRoles referenced by mappings) plus an embedded UI that lists both. It
performs no writes and no authentication;
restrict access to it with a NetworkPolicy or an authenticating proxy as your
environment requires. The `group` values in an `AccessMapping` must match the
stable group identifiers your cluster's authenticator asserts to kube-apiserver.

The project is licensed under GNU AGPL version 3 only.
