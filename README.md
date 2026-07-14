# RBAC Controller

RBAC Controller maps exact usernames and AD groups to reusable ClusterRoles and
issues short-lived kubeconfigs backed by managed ServiceAccounts.

`AccessMapping` entries create namespace RoleBindings or explicit cluster-wide
ClusterRoleBindings. Active ServiceAccounts are re-evaluated against AD every
five minutes, so membership changes update permissions without new credentials.
Deleting a managed ServiceAccount revokes all of its tokens.

```sh
make verify
make kind-test # requires Docker, kind, kubectl, and curl
kubectl apply -k config
kubectl apply -f config/sample.yaml
```

Configure `KUBERNETES_PUBLIC_SERVER`, `ADMIN_IDENTITIES` (comma-separated
usernames or AD group IDs), and the `LDAP_*` environment variables before
exposing the service. `AUTH_USERNAME_HEADER` defaults to `X-Remote-User`. Only
a trusted proxy that replaces that header may reach the HTTP service; the
authentication package exposes an interface for replacing this middleware.

The embedded UI and API provide managed ClusterRole and AccessMapping
list/create operations, effective-access search, credential issuance, lifecycle
extension, and revocation under `/api/v1`.
The project is licensed under GNU AGPL version 3 only.
