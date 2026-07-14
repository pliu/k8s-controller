# RBAC Controller Design

## Status

Proposed. This revision incorporates initial review feedback and intentionally
keeps the custom API small. The API remains `v1alpha1` until its mapping and
credential-lifecycle semantics have been exercised in real clusters.

## Overview

### Problem

Kubernetes RBAC is expressive, but administering it directly does not provide a
single answer to these operational questions:

- Which reusable `ClusterRole` definitions are approved?
- Which exact users or Active Directory (AD) groups receive those ClusterRoles,
  and in which namespaces?
- What effective managed access would a particular user receive?
- How can an authenticated user receive revocable, expiring kubeconfig
  credentials backed by a ServiceAccount?
- Which ClusterRoles, mappings, ServiceAccounts, and bindings belong to this
  system, and will drift or partial failure be repaired?

Manually maintaining these relationships makes access hard to explain and easy
to let drift. Permanent bearer-token Secrets also make credential rotation and
revocation unnecessarily risky.

### Proposed solution

Build a Go operator and credential service which use Kubernetes as the desired
state store:

- Use ordinary `ClusterRole` resources as reusable permission definitions. The
  UI and API create and manage labeled ClusterRoles directly; a separate
  permission-definition custom resource is not needed initially.
- Define one cluster-scoped `AccessMapping` custom resource. It contains lists of
  exact usernames, AD groups, and ClusterRole names with their namespace lists.
  Any listed username, or a member of any listed group, receives every listed
  ClusterRole entry.
- Create one managed ServiceAccount for each active exact username in the
  same namespace as the controller and service. The ServiceAccount's labels and
  annotations are the credential lifecycle record, including username,
  requestor, expiry, extension, directory freshness, and observed policy metadata.
  A separate credential custom resource is not needed initially.
- Reconcile namespace-scoped `RoleBinding` objects and, where explicitly
  requested, cluster-scoped `ClusterRoleBinding` objects from the effective
  mappings. Both kinds of binding refer to ClusterRoles.
- Issue short-lived ServiceAccount tokens through the Kubernetes `TokenRequest`
  API and construct kubeconfigs in memory. Tokens are never stored in a custom
  resource, application database, log, or long-lived token Secret. Deleting the
  ServiceAccount invalidates every token for that credential identity.
- Put request authentication behind middleware which produces exactly the
  identity attributes supported by AccessMapping v1: one exact username and the
  user's current stable AD group IDs. The initial implementation reads the
  username from a properly isolated reverse-proxy header and resolves groups
  from AD.

Kubernetes remains the enforcement point on every API request. ClusterRole rule
changes take effect immediately through existing bindings; mapping changes are
converged by the controller. Watches, periodic resynchronization, deterministic
names, ownership checks, and finalizers repair generated binding drift and
clean up expired credentials.

The first version manages access to the cluster in which it runs. It does not
run an authorization webhook, synchronize every directory user in advance, or
manage fleets of remote clusters.

## Goals and non-goals

### Goals

- Create, list, edit, and delete reusable managed ClusterRoles.
- Map lists of exact usernames and LDAP/AD groups to lists of ClusterRole and
  namespace pairs through a Custom Resource.
- Reconcile `RoleBinding`, `ClusterRoleBinding`, and ServiceAccount resources
  idempotently and repair generated-resource drift.
- Label every object managed by the system consistently so it can be filtered,
  audited, and cleaned up safely.
- Issue short-lived kubeconfigs for managed ServiceAccounts and support
  ServiceAccount-lifecycle extension, credential refresh, and ServiceAccount-level
  revocation.
- Keep authentication behind middleware whose v1 output is an exact username and
  AD group list, matching the AccessMapping inputs.
- Provide an authenticated UI and API for ClusterRole and mapping CRUD,
  effective-access search, credential issuance, lifecycle extension, credential
  refresh, and revocation.
- Remain correct across restarts, concurrent updates, directory outages, drift,
  and partial reconciliation.
- Provide unit, API, reconciliation, and real-cluster integration coverage,
  including kind-based tests.

### Non-goals for the initial release

- Managing namespaced `Role` definitions. ClusterRoles are used for all reusable
  rule definitions, including rules later bound into one namespace.
- A deny policy language. Effective permissions are the union of matching
  mappings, consistent with Kubernetes RBAC.
- Discovering or adopting arbitrary pre-existing RBAC resources.
- Persisting token material, issuing client certificates, or creating permanent
  ServiceAccount token Secrets.
- Independently tracking multiple credential sessions for one user.
- Cross-cluster policy distribution.
- A complete replacement for Kubernetes audit logging or an identity provider.

## Architecture

### Components

1. **Kubernetes API storage**

   Ordinary ClusterRoles hold permission rules, `AccessMapping` CRs hold mapping
   policy, and annotated ServiceAccounts hold credential lifecycle state.
   Kubernetes supplies persistence, resource versions, watches, RBAC, backups,
   and audit records, so no application database is needed.

2. **Controller manager**

   A Go process built with controller-runtime/client-go watches AccessMappings,
   managed ClusterRoles, managed ServiceAccounts, generated bindings, and target
   Namespaces. Reconcilers use deterministic names and idempotent server-side
   apply, maintain mapping conditions and ServiceAccount observation annotations,
   emit Kubernetes Events, index dependencies, and periodically resync. Leader
   election permits several warm replicas with one active writer.

3. **Authentication middleware**

   HTTP authentication is represented by a small v1 interface:

   ```text
   Authenticate(request) -> Identity | authentication error

   Identity:
     Username             exact AccessMapping username key
     ADGroupIDs           current stable AD group IDs
     ADGroupsResolvedAt   group observation time
     ADGroupsValidUntil   group freshness deadline
     ADGroupsState        Resolved or Unavailable
   ```

   The initial middleware reads a configurable header such as `X-Remote-User`
   only after the request has crossed a verified proxy trust boundary, then uses
   the AD resolver to populate `ADGroupIDs`. A directory outage can return the
   authenticated username with group state `Unavailable`, allowing exact-user
   policy to remain distinguishable from unknown group policy. No other mapping
   attributes are part of the v1 contract.

4. **Directory group resolver**

   An LDAP resolver used by the authentication middleware and controller finds
   current transitive AD membership over LDAPS (or verified StartTLS). It uses a
   mounted read-only bind credential, escaped queries, bounded connection/query
   timeouts, result limits, pooling, and a short cache. Stable group IDs
   (preferably object GUIDs or canonical DNs) are the only group values stored in
   policy. The UI may resolve a current display name from AD for readability, but
   that mutable name is not persisted in AccessMapping.

   The controller schedules a jittered directory refresh for every active managed
   ServiceAccount (assumed every five minutes). It uses the exact username stored
   on the ServiceAccount to re-resolve AD groups independently of credential
   issuance. This makes group membership an actively reconciled input rather than
   a snapshot captured in the kubeconfig.

5. **Policy evaluator**

   A pure Go package takes an authenticated username, resolved AD group IDs,
   AccessMappings, and current ClusterRoles and returns the additive,
   de-duplicated set of `(ClusterRole, binding scope)` grants. It also returns
   mapping provenance and invalid-reference information. The controller, search
   API, and tests use the same implementation.

6. **HTTP API and web UI**

   A stateless Go HTTP service exposes versioned JSON endpoints and serves an
   embedded React/TypeScript application. It supports ClusterRole and mapping
   CRUD, rule editing with YAML preview, effective-access search, and self-service
   credential operations. Kubernetes `resourceVersion` provides optimistic
   concurrency; `kubectl` changes appear through the same source of truth.

   Route authorization consumes the middleware Identity rather than reading a
   header directly. Policy mutation requires a configured administrator username
   or AD group. Credential endpoints always operate on the authenticated username
   and cannot accept a request-body username override.

7. **Credential issuer**

   The API creates or extends the username's managed ServiceAccount, then waits
   for the controller to observe the exact request revision and converge its
   bindings. On initial issuance or an explicit credential refresh, it calls the
   ServiceAccount `token` subresource with the Kubernetes API audience and an
   expiration no later than either the configured maximum or the ServiceAccount
   lifecycle expiry. It constructs a kubeconfig using a trusted API endpoint and
   CA bundle and returns it over TLS with `Cache-Control: no-store`.

   Token and complete kubeconfig bytes exist only in request memory and are
   redacted from logs and traces. There is no per-token session object.

   A ServiceAccount and its bearer token are different objects. A token returned
   by `TokenRequest` is a signed JWT with an immutable expiration claim; changing
   this controller's `expires-at` annotation on the ServiceAccount does not alter
   that JWT. Extending ServiceAccount lifetime therefore keeps the identity and
   bindings alive but does not extend an already downloaded kubeconfig. Once its
   token expires, the client needs a new TokenRequest result and thus an updated
   kubeconfig token (or a future exec credential helper that refreshes it). This
   follows the Kubernetes recommendation to use
   [short-lived TokenRequest credentials](https://kubernetes.io/docs/concepts/security/service-accounts/#manually-retrieve-service-account-credentials)
   rather than permanent ServiceAccount token Secrets.

   A refresh requested before the old token expires can cause temporary overlap,
   but overlap is not required by the data model and is not tracked. Waiting for
   expiry avoids overlap at the cost of an interruption. Deleting and recreating
   the ServiceAccount is the hard-revocation operation. Keeping one kubeconfig
   token unchanged until ServiceAccount deletion would instead require a legacy
   non-expiring ServiceAccount token Secret, which Kubernetes supports but does
   not recommend for external clients.

8. **Deployment resources**

   The controller and API run as separate Deployments and ServiceAccounts in one
   installation namespace. All end-user credential ServiceAccounts also live in
   that namespace. Separate Kubernetes identities keep the public-facing API
   from receiving role-management permissions. Production manifests include
   NetworkPolicies, non-root/read-only containers, seccomp, resource limits,
   PodDisruptionBudgets, topology spread, and anti-affinity.

### Custom resource model

#### `AccessMapping.rbac.pliu.dev/v1alpha1`

`AccessMapping` is cluster-scoped because one mapping can grant permissions into
several namespaces. Its initial shape is deliberately direct:

```yaml
apiVersion: rbac.pliu.dev/v1alpha1
kind: AccessMapping
metadata:
  name: team-a-developers
spec:
  usernames:
    - alice@example.com
    - bob@example.com
  adGroups:
    - 6f9619ff-8b86-d011-b42d-00c04fc964ff
  clusterRoles:
    - name: application-reader
      namespaces: [team-a, team-a-staging]
    - name: cluster-inventory-reader
      clusterWide: true
```

Semantics and validation:

- `usernames` and `adGroups` are match alternatives. A principal matches if its
  username exactly equals any username, or if any resolved stable group ID equals
  an AD group ID. At least one identity is required.
- A matching principal receives every valid entry in `clusterRoles`; multiple
  matching AccessMappings are unioned and de-duplicated. There is no order, deny,
  priority, or override behavior.
- Each entry names an existing managed ClusterRole. It specifies either one or
  more exact namespace names or `clusterWide: true`, never both.
- A namespace list produces a `RoleBinding` in each namespace whose `roleRef`
  points to the ClusterRole. Kubernetes requires a RoleBinding—not a
  ClusterRoleBinding—to constrain a ClusterRole grant to a namespace.
- `clusterWide: true` produces a ClusterRoleBinding. This option is more powerful
  and is visibly distinguished in the UI and audit output.
- Namespace selectors are deferred so access cannot silently change because an
  otherwise unrelated Namespace label is edited.
- Empty identity/ClusterRole lists, duplicate entries, invalid names, mutually
  exclusive scopes, and structurally invalid group IDs are rejected by CRD
  OpenAPI/CEL validation.
- Missing ClusterRoles or Namespaces and terminating Namespaces make the affected
  grant invalid and fail closed. Unaffected grants still reconcile.
- Status contains `observedGeneration`, `Ready`/`Degraded` conditions, invalid
  references, the normalized policy digest, and a count of affected active
  ServiceAccounts and generated bindings. Status never contains credentials.

### ClusterRole management

The UI/API creates ordinary `rbac.authorization.k8s.io/v1` ClusterRoles and
marks them as managed. It validates RBAC rules, calls out wildcards and known
privilege-escalation risks, and uses resource versions for concurrency. Mapping
references are restricted to managed ClusterRoles by default; this prevents a
mapping from silently binding an unlabeled built-in role such as `cluster-admin`.

A direct ClusterRole is both desired and observed state. The controller can
restore required labels and report conflicting field ownership, while edits to
its rules are intentional desired-state edits and immediately affect all
bindings. With no separate permission-definition CR, the controller cannot
distinguish deletion from intended deletion or reconstruct a deleted
ClusterRole's rules. It instead marks referring mappings degraded and removes or
withholds the affected bindings. Recreating deleted ClusterRoles requires
Kubernetes backup/GitOps or re-entry through the UI. If deletion self-healing is
a requirement, a separate declarative source for rules must be retained.

ClusterRole deletion is guarded in the API/UI when mappings still refer to it.
Direct Kubernetes deletion cannot safely be prevented by the controller; a
finalizer would delay deletion but cannot serve as a durable copy of the rules.

### ServiceAccount lifecycle model

One managed ServiceAccount per exact username is the initial model. Its
deterministic name is a readable prefix plus a username hash, and it is always
created in the installation namespace.

The ServiceAccount is the lifecycle record. The API/controller maintain these
annotations (names illustrative but stable once the API is implemented):

```text
rbac.pliu.dev/principal-username=<exact normalized username>
rbac.pliu.dev/requested-by=<authenticated requestor ID>
rbac.pliu.dev/requested-at=<RFC3339 timestamp>
rbac.pliu.dev/last-extended-at=<RFC3339 timestamp>
rbac.pliu.dev/expires-at=<RFC3339 timestamp>
rbac.pliu.dev/request-revision=<opaque API-generated revision>
rbac.pliu.dev/observed-request-revision=<controller-observed revision>
rbac.pliu.dev/last-directory-sync=<RFC3339 timestamp>
rbac.pliu.dev/directory-valid-until=<RFC3339 timestamp>
rbac.pliu.dev/policy-digest=<controller-observed digest>
rbac.pliu.dev/credential-state=<Reconciling|Ready|Degraded|Revoking>
```

Raw usernames are annotations rather than labels to avoid label character and
length limits. Access to the installation namespace and ServiceAccounts is
restricted because these annotations contain identity metadata. Controller-owned
observation annotations are not accepted from an API request and are overwritten
on reconciliation.

The ServiceAccount has `automountServiceAccountToken: false`. Only the service
API can create or extend it; end users cannot edit lifecycle annotations or call
its token subresource. The API issues a token only when the observed request
revision matches the requested revision, state is `Ready`, directory evidence is
sufficiently fresh, and the policy digest still matches the evaluator's current
view.

Lifecycle extension updates `last-extended-at`, `expires-at`, and
`request-revision`, then waits for reconciliation. It does not return a new
kubeconfig and does not alter any token already issued. Credential refresh is a
separate operation that returns a kubeconfig with a newly issued short-lived
token. If refresh happens before the old token expires, the two tokens overlap;
if it happens after expiry, there is no overlap but the old kubeconfig has an
interruption.

Expiry, explicit revocation, confirmed directory deletion, or deletion of the
ServiceAccount starts cleanup. A ServiceAccount finalizer lets the controller
delete its generated cross-namespace RoleBindings and ClusterRoleBindings before
removing the finalizer. A ServiceAccount with a deletion timestamp also stops
being a durable authentication anchor, so deletion is the hard-revocation path
even if cleanup is temporarily delayed.

### Ownership, labels, and names

Every object created or managed by the system—including managed ClusterRoles,
AccessMappings, ServiceAccounts, RoleBindings, ClusterRoleBindings, and
installation resources—carries at least:

```text
app.kubernetes.io/name=rbac-controller
app.kubernetes.io/managed-by=rbac-controller
rbac.pliu.dev/owner-kind=<lowercase-kind>
rbac.pliu.dev/owner-uid=<source-object-uid>
```

Credential resources also receive `rbac.pliu.dev/principal-hash`, a
non-reversible hash suitable for indexed listing without putting a username in a
label. The controller ensures base management labels on AccessMappings submitted
directly through Kubernetes while preserving unrelated metadata.

Generated names combine a readable prefix with a digest of source UID, principal
identity, role, and scope. Before changing an object, the controller verifies its
management and ownership labels. A same-named unowned object produces a
`NameCollision` condition; it is never adopted or overwritten.

Owner references are used where scope rules allow. Finalizers cover
cross-namespace binding cleanup and tolerate deleted Namespaces and other
`NotFound` results so namespace teardown cannot wedge credential revocation.

### Data flow

#### ClusterRole and mapping administration

1. The authentication middleware produces an Identity containing the exact
   username and current AD group IDs. Route authorization verifies a configured
   policy-administrator username or AD group.
2. An administrator creates or edits a managed ClusterRole or AccessMapping
   through the UI/API (or applies an AccessMapping directly with `kubectl`).
3. Kubernetes schema validation rejects malformed mappings. The controller
   resolves ClusterRole/Namespace references and publishes mapping status.
4. Mapping changes enqueue affected managed ServiceAccounts. ClusterRole rule
   edits need no rebinding because Kubernetes evaluates referenced rules live.
5. Each ServiceAccount's desired grants are recomputed. The controller applies
   one binding per ServiceAccount, ClusterRole, and scope and deletes obsolete
   bindings. Per-credential bindings avoid one large contended subject list and
   make revocation ownership simple; scale tests validate the object count.
6. Status, observation annotations, Events, metrics, and audit logs show
   convergence or a precise error.

#### Credential issuance, extension, and refresh

1. The authentication middleware returns an Identity with the exact username and
   current AD group IDs. For the initial implementation, a hardened reverse proxy
   replaces inbound username headers and is the only network peer allowed to
   reach the API; the middleware resolves AD groups before returning.
2. The API validates the Identity and its AD group freshness state.
3. It creates or extends the deterministic ServiceAccount in the installation
   namespace, records lifecycle/request metadata, and waits for the controller to
   observe that exact request revision.
4. The controller independently evaluates mappings, applies RoleBindings and
   ClusterRoleBindings, and marks the ServiceAccount `Ready` only after the
   resulting reduced or expanded state has converged.
5. The API requests a short-lived token, constructs the kubeconfig in memory,
   returns it once, and records only issuance metadata.
6. `kubectl` presents the token to kube-apiserver. Kubernetes authenticates the
   ServiceAccount and evaluates the current ClusterRole rules and bindings on
   every request.
7. Lifecycle extension repeats steps 2–4 and may move ServiceAccount expiry
   forward without changing the kubeconfig. Credential refresh repeats step 5
   and returns a new token. Revocation deletes the ServiceAccount, invalidating
   all tokens attached to its identity.

#### AD group membership changes

ServiceAccount tokens identify the ServiceAccount; they do not contain this
controller's AD group list or effective ClusterRole grants. Therefore a user does
not need new credentials when AD membership changes:

1. The controller periodically re-resolves AD groups for each non-expired managed
   ServiceAccount using its username annotation. An authenticated request can
   also enqueue an immediate refresh, but login or credential refresh is not
   required for correctness.
2. The shared evaluator recomputes every AccessMapping that matches the username
   or current AD groups and compares the result with existing bindings.
3. For newly added groups, the controller creates the newly required
   RoleBindings/ClusterRoleBindings. For removed groups, it deletes bindings no
   longer justified by any exact-username or remaining-group mapping.
4. The controller updates directory freshness, policy digest, and credential
   state annotations only after the binding changes converge.
5. On the user's next API request, kube-apiserver evaluates the same existing
   ServiceAccount token against the current bindings, so the new permissions take
   effect without changing the kubeconfig.

The normal propagation bound is the configured refresh interval plus LDAP and
reconciliation latency. Refreshes are jittered and concurrency-limited to avoid
an LDAP thundering herd. If LDAP is unavailable, additions are never inferred;
previous group evidence follows the bounded-staleness and fail-closed removal
rules below.

#### Effective-access search

Given a username, the admin-only search endpoint applies the same authentication
normalization rules, resolves current groups if needed, and returns a
namespace/ClusterRole matrix with mapping provenance. It distinguishes:

- **Desired** managed access computed from current mappings and directory state.
- **Observed** generated bindings and ServiceAccount/controller state.
- **Unmanaged** access found by an optional diagnostic scan of direct external
  bindings, clearly identified as outside this system's ownership.

If directory membership is unavailable, exact-username results remain known and
group-derived results are reported as `unknown`; stale membership is never shown
as current without its freshness timestamp.

### Correctness, reliability, and availability

- **Source of truth:** AccessMappings, managed ClusterRoles, and lifecycle
  annotations on managed ServiceAccounts are durable Kubernetes state. No
  application database is required.
- **Idempotence and generated drift repair:** bindings and ServiceAccount-owned
  metadata are applied from normalized desired state with a dedicated field
  manager. Watches plus periodic full resync repair deletion or mutation. Direct
  ClusterRole rule edits are desired-state edits; deleted rule definitions need
  an external declarative source to be reconstructed.
- **Fail-closed convergence:** a missing ClusterRole/Namespace or expired group
  observation removes or withholds the affected binding. Stale request revision,
  stale policy digest, or incomplete reconciliation prevents token issuance.
- **Directory outage:** an outage never adds a group grant. Previously verified
  group membership remains valid only until a configured staleness deadline
  (assumed fifteen minutes) and is then removed. Exact-username grants do not
  depend on LDAP.
- **HA:** run at least three controller replicas with leader election and three
  active-active API replicas across failure domains. Readiness checks local
  ability to serve and does not eject every pod during a shared LDAP failure;
  dependency failure is reported per request. Liveness checks process health.
- **One evaluator:** the controller and API share one evaluator library and
  normalized policy digest. Issuance waits for the exact request revision and
  digest observed by the controller, avoiding mixed-generation credentials.
- **Revocation:** removing bindings changes authorization without waiting for
  token expiry. Deleting the ServiceAccount invalidates all of its credentials.
  Short token TTL bounds exposure if cleanup is delayed.
- **Backpressure:** rate-limited work queues, bounded directory concurrency,
  request limits, server timeouts, pagination, and per-principal de-duplication
  prevent spikes from exhausting the service or kube-apiserver.
- **Recovery:** durable state is in Kubernetes and follows cluster backup policy.
  Token material is intentionally unrecoverable; a user requests a replacement.

### Security and privacy

- The authentication middleware documents its trust boundary. The header input
  strips or rejects ambiguous/multiple headers at the edge, denies direct API
  access, and uses TLS or mTLS from proxy to service. All implementations must
  return the same tested username and AD-group invariants.
- Directory connections require certificate validation. Read-only bind
  credentials are supplied from an externally managed Secret and mounted only
  into processes that query LDAP.
- Controller and API Kubernetes identities are separate and neither receives
  `cluster-admin`. The controller receives explicit role/binding/ServiceAccount
  and CRD permissions, including narrowly understood `bind`/`escalate` ability.
  The API's sensitive `serviceaccounts/token` permission is restricted to the
  installation namespace.
- Writing ClusterRoles or AccessMappings can grant powerful access and is
  restricted to policy administrators. UI warnings for secret access, workload
  creation, wildcards, non-resource URLs, `bind`, `escalate`, and `impersonate`
  are not treated as an authorization boundary.
- The installation namespace does not host user workloads. Its ServiceAccounts
  have `automountServiceAccountToken: false`, ResourceQuota limits object growth,
  and NetworkPolicy restricts access to the service.
- Browser mutations use CSRF protection, same-site secure cookies where
  applicable, a restrictive Content Security Policy, and request-size limits.
- Audit policy mutations, effective-grant changes, issuance, lifecycle extension,
  credential refresh, and revocation with actor, source UID, request ID, and
  timestamps, but never token or kubeconfig bytes. Usernames are excluded from
  metric labels and may be hashed or redacted in logs.

### Observability

Expose Prometheus metrics for reconcile duration/errors/requeues, mapping
readiness, managed-object drift, policy convergence lag, active/expired
ServiceAccounts, issuance/extension/refresh/revocation outcomes, directory
latency/cache/failure, and leader status. Emit structured JSON logs with
resource/request correlation IDs and Kubernetes Events for actionable resource
failures. Provide
`/livez`, `/readyz`, and dependency status. Alert on sustained reconciliation
errors, policy lag, directory staleness approaching its bound, TokenRequest
failure, and absence of an elected leader.

### Technology choices

- **Go, client-go, and controller-runtime/Kubebuilder:** native Kubernetes types,
  established watch/reconcile behavior, leader election, caches, indexes,
  metrics, and envtest support.
- **Direct ClusterRoles plus one AccessMapping CRD:** reusable native RBAC rules
  without duplicating rule definitions in a custom type; one small custom API
  expresses the relationship Kubernetes RBAC does not store itself.
- **Annotated ServiceAccounts:** visible lifecycle and ownership state on the
  actual authentication identity, without a second credential object.
- **TokenRequest:** expiring credentials without persisted bearer-token material.
  Legacy permanent ServiceAccount token Secrets are unsupported.
- **Username-and-AD-group authentication contract:** the middleware produces
  exactly the identity attributes understood by AccessMapping v1, keeping HTTP
  trust and directory behavior outside policy evaluation and reconciliation.
- **Go HTTP API plus React/TypeScript/Vite UI:** typed backend integration and a
  maintainable role editor/search experience; compiled assets are embedded into
  the API image.
- **kind plus envtest:** envtest provides fast reconciliation tests; kind supplies
  a real API server, RBAC authorizer, ServiceAccount token issuer, leader election,
  and revocation behavior.

## Proposed project layout

```text
.
├── api/
│   └── v1alpha1/                 # AccessMapping API and generated code
├── cmd/
│   ├── controller/               # controller manager entry point
│   ├── server/                   # API and embedded UI entry point
│   └── rbacctl/                  # optional future credential helper/admin CLI
├── config/
│   ├── crd/                      # AccessMapping schema and CEL validation
│   ├── default/                  # kustomize composition
│   ├── manager/                  # Deployments, Services, probes, security
│   ├── rbac/                     # least-privilege installation RBAC
│   ├── network-policy/
│   ├── samples/                  # safe ClusterRole/AccessMapping examples
│   └── kind/                     # development/integration overlays
├── internal/
│   ├── authn/                    # username/AD-group middleware contract
│   ├── controller/               # mapping, SA, and binding reconcilers
│   ├── credentials/              # SA lifecycle, TokenRequest, revocation
│   ├── directory/                # group resolver, AD implementation, cache
│   ├── kubeconfig/               # pure construction and redaction
│   ├── managed/                  # labels, ownership, naming, SSA helpers
│   ├── policy/                   # evaluator, digest, and explanations
│   ├── server/                   # routes, authz, API models, audit
│   └── status/                   # mapping conditions and Events
├── web/                          # React/TypeScript UI source and tests
├── test/
│   ├── envtest/                  # reconciliation integration tests
│   ├── kind/                     # real-cluster and HA/failure tests
│   ├── ldap/                     # disposable directory fixtures
│   └── e2e/                      # auth middleware → kubeconfig → API server
├── charts/rbac-controller/       # packaging after manifests stabilize
├── docs/                         # API, operations, auth middleware, threat model
├── hack/                         # generation, verification, kind helpers
├── DESIGN.md
├── LICENSE                       # GNU AGPL v3 text
├── Makefile
├── go.mod
└── README.md
```

Generated files are reproducible and checked by CI. Go source uses
`SPDX-License-Identifier: AGPL-3.0-only`; compatible notices are added to web and
generated artifacts. Dependency licenses are audited in CI. The repository is
distributed under GNU Affero General Public License version 3 only
(`AGPL-3.0-only`).

## Testing and release strategy

### Test layers

- Unit-test exact username/group matching, additive/de-duplicated evaluation,
  grant normalization, naming, lifecycle clocks, LDAP escaping, authentication
  middleware invariants, kubeconfig construction, and token redaction. Use a fake
  clock and fuzz/property tests for mapping input.
- Use controller-runtime envtest for mapping create/update/delete, missing
  references, ServiceAccount create/extend/expire/delete, finalizers, observation
  revisions, indexes, binding drift, ownership collision, namespace deletion,
  concurrent updates, and restart idempotence.
- Run kind tests against supported Kubernetes minor versions. Verify real RBAC
  using issued kubeconfigs, short token expiry, ServiceAccount revocation,
  AD group addition/removal taking effect with the same kubeconfig, namespaced
  RoleBinding versus ClusterRoleBinding scope, leader failover, controller
  restart, and clean uninstall.
- Run a basic LDAP fixture and a distinct AD-compatible lane for stable IDs and
  nested groups; do not claim AD parity from OpenLDAP alone.
- Exercise the end-to-end authentication boundary, including spoofed/ambiguous
  headers, middleware errors, route authorization, CSRF, directory outage/staleness,
  policy-change races, cache-control, and log scanning for token leakage.
- Scale-test users × mappings × namespaces, API object counts, reconciliation
  latency, directory load, and kube-apiserver throttling before beta.

### Compatibility and rollout

Publish a Kubernetes version matrix based on versions exercised in CI. In
particular, test TokenRequest and ServiceAccount deletion behavior on every
supported version. Upgrade CRDs before Deployments and add conversion only when
a second stored API version exists. Alpha makes no storage-version stability
promise, but reconciliation and deletion safety are required from the first
release.

## Milestones

### 0. Resolve semantics and threat model

- Resolve the clarifying questions below.
- Specify the Identity contract, authentication middleware trust requirements,
  ServiceAccount lifecycle state machine, mapping API conventions, and SLOs.
- Define the Kubernetes compatibility matrix and AGPL-3.0-only policy.

Exit criterion: reviewers agree on mapping shape, binding scopes, authentication,
ClusterRole ownership, credential extension/refresh/revocation, and directory
behavior.

### 1. Scaffold AccessMapping and policy evaluation

- Initialize Go/Kubebuilder, license files, generation, linting, and CI.
- Define the AccessMapping schema, status, validation, and safe samples.
- Implement and test Identity, evaluator, provenance, normalization, naming,
  labels, digests, and ownership checks.

Exit criterion: the CRD installs cleanly and fixture mappings produce stable,
explainable grants without issuing credentials.

### 2. Reconcile ServiceAccounts and bindings

- Implement managed ServiceAccount discovery and lifecycle annotations using
  fixture identities before external authentication is connected.
- Reconcile RoleBindings/ClusterRoleBindings, finalizers, server-side apply,
  drift repair, ownership collisions, indexes, Events, metrics, and status.
- Validate namespace deletion, mapping changes, expiry, restart, and leader
  failover in envtest and kind.

Exit criterion: binding and credential desired state converges after create,
update, delete, drift, restart, and partial failures without orphaned access.

### 3. Username and AD-group authentication

- Implement middleware that authenticates an exact remote-header username and
  returns its resolved stable AD group IDs.
- Add TLS-only AD group resolution, stable identity/group handling, nested groups,
  timeouts/cache, and bounded-staleness behavior.
- Add fake-middleware, fake-directory, OpenLDAP, and AD-compatible tests.

Exit criterion: middleware cannot bypass username/group invariants, and exact-user
and group changes converge within documented bounds.

### 4. Secure issuance, lifecycle extension, and credential refresh

- Implement ServiceAccount create/extend/revoke APIs, request/observation revision
  handshake, TokenRequest, in-memory kubeconfig construction, and redaction.
- Split API/controller permissions and add hardened manifests/NetworkPolicies.
- Complete authentication-to-kube-apiserver kind tests, including the distinction
  between ServiceAccount extension and token refresh, optional token overlap, and
  hard revocation through ServiceAccount deletion.

Exit criterion: an authenticated user can obtain only their current effective
permissions, extend ServiceAccount expiry, refresh an expired or expiring
kubeconfig, and revoke every outstanding credential by deleting the managed
ServiceAccount.

### 5. ClusterRole/mapping UI and search

- Implement managed ClusterRole and AccessMapping CRUD with validation, risk
  warnings, reference checks, and optimistic concurrency.
- Add desired-versus-observed username search with grant provenance and explicit
  unknown directory state.
- Add self-service credential state, lifecycle extension, credential refresh, and
  revocation views.
- Complete accessibility, browser security, and UI tests.

Exit criterion: routine role, mapping, and credential operations require no
hidden state, and every managed effective grant can be explained.

### 6. Production hardening and beta

- Establish dashboards/alerts, load limits, disruption/failover tests,
  backup/restore and upgrade/rollback procedures, and security review.
- Test supported Kubernetes and authentication-middleware configurations, AD
  variants, scale targets, and dependency/image/license scanning.
- Add Helm packaging after manifests and APIs stabilize. Consider an exec
  credential helper only after non-browser reauthentication is settled.

Exit criterion: runbooks, compatibility guarantees, scale evidence, and a
reviewed threat model support a beta release.

## Clarifying questions and working assumptions

These are ordered roughly by how much the answers could change the design.
Implementation can proceed with the stated assumptions if no answer is given.

1. **What exact username header and AD lookup configuration are authoritative?**

   Assumption: v1 middleware authenticates the exact username from a trusted
   configurable remote header and returns transitive AD group IDs resolved over
   LDAP. Those are the only two identity attributes AccessMapping consumes.
   Runtime-loaded authentication plugins and additional mapping attributes are
   not required. Browser download is sufficient initially; a CLI exec helper
   waits for an approved non-browser reauthentication flow.

2. **Must a deleted managed ClusterRole be automatically reconstructed?**

   Assumption: no. The direct ClusterRole is authoritative, so intentional edits
   need no second object but deletion loses the rule definition. Referring grants
   fail closed until the role is restored through UI, backup, or GitOps. If the
   controller must recreate deleted ClusterRoles, it needs a separate desired
   rules source such as the previously proposed permission-definition CR.

3. **May mappings reference existing unlabeled ClusterRoles, including built-in
   roles, or only ClusterRoles created and labeled by this system?**

   Assumption: only managed/labeled ClusterRoles. Explicit support for external
   roles would need an allowlist and must make clear that their rule drift and
   deletion are outside this controller's ownership.

4. **Are all grants namespace-scoped, or are explicit cluster-wide grants also
   required?**

   Assumption: both are supported. Namespace lists produce RoleBindings to a
   ClusterRole; `clusterWide: true` produces a ClusterRoleBinding. If all grants
   must name namespaces, remove `clusterWide` and ClusterRoleBinding management.

5. **What directory schema and group-membership semantics are authoritative?**

   Assumption: exact usernames have no implicit case folding. AD groups are
   matched transitively by stable object GUID, with canonical DN configurable,
   over LDAPS using a read-only account. Username lookup attribute, aliases,
   forest boundaries, disabled-user handling, and nested-group query strategy are
   explicit deployment configuration.

6. **Must one downloaded kubeconfig remain unchanged for the entire, extendable
   ServiceAccount lifetime?**

   Assumption: no; v1 retains short-lived TokenRequest credentials. Extending the
   ServiceAccount changes only the controller-managed deletion deadline. It cannot
   change the immutable expiration in an already issued JWT, so a client whose
   token expires must refresh the kubeconfig token. Refreshing early can briefly
   overlap tokens; refreshing after expiry avoids overlap. If an unchanged static
   kubeconfig is required, the design must explicitly accept a legacy non-expiring
   ServiceAccount token Secret and its larger bearer-token risk, or add an exec
   credential helper that refreshes tokens transparently.

7. **What are the token expiry, ServiceAccount extension, inactivity,
   directory-staleness, and availability targets?**

   Assumption: tokens last at most one hour, a ServiceAccount is initially valid
   for eight hours and may be extended after current authentication, active group
   membership is refreshed every five minutes, and group evidence is valid for
   at most fifteen minutes. Three replicas per component target 99.9% service
   availability. The API server may impose a lower token maximum.

8. **Can mappings combine, and are deny or priority rules required?**

   Assumption: every mapping containing the username or any current group matches;
   all grants are unioned and de-duplicated. There is no deny, priority, or
   override. Deny semantics would require enforcement beyond standard RBAC.

9. **Is the installation single-cluster, and may it adopt existing resources?**

   Assumption: one installation manages its local cluster and only objects it
   created/labeled. It never adopts same-named resources. Multi-cluster policy or
   adoption would need explicit ownership, failure, and trust semantics.
