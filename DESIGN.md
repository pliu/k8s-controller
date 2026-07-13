# RBAC Controller Design

## Status

Proposed. This document describes the initial design; the API is intentionally
`v1alpha1` until its reconciliation and lifecycle semantics have been exercised
in a real cluster.

## Overview

### Problem

Kubernetes RBAC is expressive, but administering it directly does not provide a
single answer to these operational questions:

- Which reusable permission sets are approved, and where are they materialized
  as `Role` or `ClusterRole` objects?
- Which exact users or Active Directory (AD) groups receive those permissions?
- What effective access would a particular user receive across namespaces?
- How can an authenticated user receive revocable, expiring kubeconfig
  credentials without distributing permanent bearer tokens?
- Which Kubernetes objects belong to this system, and will drift or partial
  failure be repaired?

Manually maintained roles, bindings, ServiceAccounts, and token Secrets make
those answers difficult to audit and easy to let drift.

### Proposed solution

Build a Go operator and credential broker which use Kubernetes as the desired
state store. Administrators declare reusable permissions in `PermissionSet`
custom resources and identity-to-permission mappings in `AccessMapping` custom
resources. A controller materializes the required RBAC objects. When a user
authenticated by a trusted reverse proxy requests credentials, the system uses
the exact `X-Remote-User` value and current LDAP group membership to compute the
user's additive grants. A controller-owned `CredentialLease` then drives a
dedicated ServiceAccount and its bindings.

The broker obtains a short-lived, audience-restricted ServiceAccount token via
the Kubernetes `TokenRequest` API and constructs a kubeconfig in memory. Tokens
are not written to a custom resource, database, log, or Kubernetes Secret. Each
issued token is bound to an otherwise data-free Secret that acts as a revocation
handle: deleting that Secret invalidates the token independently of the other
sessions for the same ServiceAccount. Deleting the ServiceAccount revokes all
of the principal's sessions.

Kubernetes RBAC remains the enforcement point on every API request. A policy
change takes effect by changing bindings rather than waiting for a token to
expire. The controller continuously converges generated objects, repairs drift,
reports conditions on source resources, and garbage-collects expired leases and
sessions.

The first version manages access to the cluster in which it runs. It does not
replace Kubernetes authentication for arbitrary `User`/`Group` subjects, run an
authorization webhook, synchronize all of AD, or manage fleets of remote
clusters.

## Goals and non-goals

### Goals

- Declaratively define namespaced and cluster-wide permission sets.
- Map exact usernames and LDAP/AD groups to permission sets at explicit scopes.
- Reconcile `Role`, `ClusterRole`, `RoleBinding`, `ClusterRoleBinding`, and
  ServiceAccount resources idempotently.
- Issue short-lived kubeconfigs for per-principal ServiceAccounts, support
  renewal, and provide bounded and explicit revocation.
- Label every generated object consistently so it can be listed, audited, and
  cleaned up safely.
- Provide an authenticated UI and API for policy CRUD, effective-access search,
  credential issuance, renewal, and revocation.
- Remain correct across restarts, concurrent updates, dependency outages, drift,
  and partial reconciliation.
- Provide unit, API, reconciliation, and real-cluster integration coverage,
  including kind-based tests.

### Non-goals for the initial release

- A deny policy language. Effective permissions are the union of matching
  grants, consistent with Kubernetes RBAC.
- Discovering or adopting arbitrary pre-existing RBAC objects.
- Password authentication, LDAP bind-as-user, or accepting a username supplied
  in a request body.
- Long-lived ServiceAccount token Secrets or client certificate issuance.
- Cross-cluster policy distribution.
- A complete replacement for Kubernetes audit logging or an identity provider.

## Architecture

### Components

1. **CRDs and Kubernetes API storage**

   `PermissionSet`, `AccessMapping`, and `CredentialLease` resources hold
   desired state and observable status. CRD OpenAPI schemas and CEL rules reject
   structurally unsafe combinations without introducing an availability-critical
   admission webhook. etcd persistence, resource versions, watches, and the
   Kubernetes audit trail are reused instead of adding a database.

2. **Controller manager**

   A Go process built with controller-runtime/client-go watches the three CRDs,
   generated resources, and relevant Namespaces. Reconcilers use deterministic
   names and idempotent server-side apply, maintain status conditions, emit
   Kubernetes Events, requeue dependents through field indexes, and periodically
   resync to detect missed events. Leader election permits multiple warm replicas
   while ensuring one active writer; a failed leader is replaced without manual
   repair.

3. **LDAP resolver**

   Exact `User` matching uses the validated canonical header value and does not
   require LDAP. When group mappings exist, a shared Go package optionally
   resolves that username to a stable directory identity and transitive AD group
   membership over LDAPS (or verified StartTLS). It uses an externally supplied
   read-only bind credential, escaped/filter-safe queries, bounded connection/
   query timeouts, pooling, result-size limits, and a short in-memory cache.
   Stable group identifiers (preferably object GUIDs or canonical DNs) are used
   in policy; display names are status/UI data only.

4. **Policy evaluator**

   A pure, deterministic package takes the exact validated user identity, resolved
   groups, `AccessMapping` objects, and `PermissionSet` objects and returns the
   normalized union of grants. The controller, search API, and tests use the same
   implementation. The result includes provenance (which mappings contributed
   each grant) for explanation and audit.

5. **HTTP API and web UI**

   A stateless Go HTTP service exposes versioned JSON endpoints and serves an
   embedded React/TypeScript application. It supports list/detail/create/update/
   delete flows for permission sets and mappings, a rule editor with YAML preview,
   effective-access search, and self-service credential operations. Writes use
   Kubernetes `resourceVersion` for optimistic concurrency. The UI is not a
   second source of truth; changes made with `kubectl` appear identically.

   Policy mutation endpoints require membership in a configured administrator
   group. Credential endpoints always derive the principal from the trusted
   header and cannot issue for an arbitrary requested username. Read, admin, and
   credential routes have distinct authorization checks and audit records.

6. **Credential issuer**

   After a `CredentialLease` is `Ready`, the API creates a labeled, data-free
   `Opaque` Secret with an expiry annotation and calls the ServiceAccount `token`
   subresource with that Secret's UID as `boundObjectRef`, the Kubernetes API as
   audience, and a configured short lifetime. It returns a kubeconfig containing
   the configured API endpoint, CA bundle, context, and token over TLS with
   `Cache-Control: no-store`. Token and complete kubeconfig bytes exist only in
   request memory and are redacted from logs and traces.

   Initially, users renew by downloading a new short-lived kubeconfig. A small
   optional `rbacctl` exec-credential helper can later make renewal transparent:
   its kubeconfig user entry invokes the helper, which authenticates to the
   broker and returns an `ExecCredential`. The helper must not turn proxy-header
   trust into a workstation-spoofable mechanism; its concrete interactive
   authentication depends on the answer to the authentication question below.

7. **Deployment resources**

   The controller and API run as separate Deployments and ServiceAccounts so the
   public-facing process does not receive role-management privileges. The API
   can manage leases/session handles and request tokens only; the controller can
   manage source CR status and generated RBAC resources. NetworkPolicy, a
   read-only root filesystem, non-root containers, seccomp, resource requests/
   limits, PodDisruptionBudgets, topology spread, and anti-affinity are supplied
   in the production manifests.

### Custom resource model

The examples below describe semantics, not the final generated OpenAPI schema.
All three resources are cluster-scoped because mappings and leases can reference
multiple namespaces. Only policy administrators and the controller/API service
accounts receive write access to them.

#### `PermissionSet.rbac.pliu.dev/v1alpha1`

A reusable RBAC rule definition:

```yaml
spec:
  roleKind: Role # Role or ClusterRole
  rules:         # rbac.authorization.k8s.io/v1 PolicyRule values
    - apiGroups: [""]
      resources: ["pods"]
      verbs: ["get", "list", "watch"]
```

- `Role` definitions are materialized in every namespace where a valid grant
  references them. A role is removed when no grant needs it.
- A `ClusterRole` definition is materialized once. A mapping can bind it in
  named namespaces with `RoleBinding`, or cluster-wide with
  `ClusterRoleBinding`.
- Rules are normalized for stable comparison but retain Kubernetes RBAC
  semantics. Wildcards, secret access, workload creation, `bind`, `escalate`,
  `impersonate`, and non-resource URLs are allowed only for policy administrators
  and are highlighted as high risk in the UI/audit output. A later policy guard
  may prohibit selected rules; the controller cannot make inherently dangerous
  permissions safe.
- Semantic validation uses API discovery to reject namespaced `Role` rules that
  can never be effective, such as non-resource URLs or known cluster-scoped
  resources. Unknown API groups are reported distinctly so installing a CRD can
  make them resolvable on a later reconciliation.
- Status contains `observedGeneration`, `Ready`/`Degraded` conditions, rendered
  object references, and a rules digest.

#### `AccessMapping.rbac.pliu.dev/v1alpha1`

An additive identity selector and its grants:

```yaml
spec:
  subjects: # any subject may match
    - kind: User
      name: alice@example.com # exact canonical X-Remote-User value
    - kind: LDAPGroup
      id: 6f9619ff-8b86-d011-b42d-00c04fc964ff # configured stable AD ID
  grants:
    - permissionSetRef:
        name: namespace-reader
      namespaces: [team-a, team-b]
    - permissionSetRef:
        name: cluster-observer
      cluster: true
```

- At least one subject and grant is required. Subject matching is OR; combining
  properties with AND is deferred until its semantics are justified.
- A `Role` grant requires non-empty exact namespace names and cannot set
  `cluster`. A `ClusterRole` grant chooses either exact namespaces or `cluster`,
  never both. Namespace selectors are deferred to avoid access changing merely
  because an editable Namespace label changes.
- Missing references, scope mismatch, or a terminating target Namespace make
  the affected grant invalid and fail closed; unaffected grants can still
  reconcile. Status explains every invalid reference and records a policy digest.
- Multiple matching mappings are unioned and de-duplicated. There is no ordering
  and no deny/override behavior.

#### `CredentialLease.rbac.pliu.dev/v1alpha1`

An internal, declarative record of one canonical principal's managed identity:

```yaml
spec:
  principal:
    username: alice@example.com
    directoryID: 2b6c... # optional stable identity when LDAP resolved
  disabled: false
  renewUntil: "2026-07-13T21:00:00Z"
status:
  serviceAccountRef: {namespace: rbac-controller-credentials, name: user-...}
  effectiveGrants: []
  matchedMappings: []
  lastDirectorySyncTime: "..."
  observedGeneration: 1
  conditions: []
```

- The API, not an end user-provided manifest, creates or renews the lease for the
  authenticated principal. The principal fields are immutable. One active lease
  and ServiceAccount per stable directory ID, or per exact username when no
  directory identity is available, is the initial model; multiple bound session
  handles allow independent kubeconfig revocation.
- `renewUntil` caps the period during which new tokens may be issued. Session
  token lifetime is separately capped by server configuration and the API
  server. Defaults assumed here are a one-hour token and an eight-hour renewable
  lease.
- The controller validates the exact header-derived username and, when group
  policies are relevant, resolves LDAP itself. It writes computed grants only to
  status; the API cannot inject entitlements through lease spec. On every renewal
  and at a configured interval (assumed five minutes), active leases are
  re-evaluated.
- `disabled`, expiry, directory deletion, or lease deletion removes bindings
  first, deletes session handles, then deletes the ServiceAccount. Group-derived
  grants are removed when fresh membership no longer matches.
- During LDAP failure, no newly discovered group-derived access is granted.
  Previously verified group-derived bindings survive only to a bounded
  membership-staleness deadline (assumed fifteen minutes) and are then removed;
  a token issued during that interval has only whatever bindings remain current.
  Exact-user grants and issuance do not depend on LDAP. Status distinguishes
  `DirectoryUnavailable` from a confirmed absence of membership and records the
  freshness deadline used by the issuer.

### Ownership, labels, names, and annotations

Every object created or mutated by the system, including CRs created through the
API and generated Roles, bindings, ServiceAccounts, session Secrets, and
installation resources, carries at least:

```text
app.kubernetes.io/name=rbac-controller
app.kubernetes.io/managed-by=rbac-controller
rbac.pliu.dev/owner-kind=<lowercase-kind>
rbac.pliu.dev/owner-uid=<source-object-uid>
```

The controller also ensures these base labels on source CRs submitted directly
to Kubernetes, preserving unrelated user labels and annotations.

Credential resources also receive `rbac.pliu.dev/principal-hash`, a non-reversible
hash used for indexing without putting usernames or group names in labels.
ServiceAccounts mirror non-secret operational lifecycle data in annotations:

```text
rbac.pliu.dev/credential-lease=<name>
rbac.pliu.dev/renew-until=<RFC3339 timestamp>
rbac.pliu.dev/last-directory-sync=<RFC3339 timestamp>
rbac.pliu.dev/policy-digest=<digest>
```

The `CredentialLease` remains authoritative; annotations are for inspection and
recovery, not user-controlled policy. Usernames are never labels because of
privacy, length, and character-set concerns.

Generated names combine a readable prefix with a digest of the source UID and
scope, stay within Kubernetes length limits, and never depend on mutable display
names. Before touching an object, a reconciler verifies its ownership labels and
controller reference. A same-named unowned object produces a `NameCollision`
condition; it is not adopted or overwritten.

Owner references are used where Kubernetes scope rules allow them. Finalizers
cover cross-resource and cross-namespace cleanup, with deletion ordered to
revoke access before removing bookkeeping. Finalizers tolerate already-deleted
Namespaces and other `NotFound` results so namespace teardown cannot wedge.

### Reconciliation and data flow

#### Policy administration

1. An administrator submits a permission set or mapping through the UI/API (or
   `kubectl`). Authentication and admin-group authorization occur before writes.
2. CRD schema/CEL validation rejects invalid field combinations. The reconciler
   resolves references and publishes semantic conditions.
3. The permission reconciler applies the required Roles/ClusterRoles. Mapping
   changes enqueue affected leases through indexed references.
4. Each affected lease is re-evaluated. The lease reconciler applies one binding
   per lease, permission set, and binding scope, and deletes obsolete bindings.
   Per-lease bindings avoid a single large, contended subjects list and make
   revocation/ownership simple; cardinality is measured before beta.
5. Status, Events, metrics, and audit logs show convergence or a precise error.

#### Credential issuance

1. A hardened reverse proxy authenticates the browser and replaces (rather than
   appends to) inbound identity headers. Only the proxy can reach the API, using
   mTLS or an equivalent network trust boundary.
2. The API reads and validates `X-Remote-User`; it never accepts an identity
   override. It queries LDAP for the stable identity and current transitive groups
   when group policies are relevant, then creates or renews that principal's
   `CredentialLease`. An LDAP outage does not prevent exact-user-only access.
3. The lease controller independently resolves/evaluates policy, reconciles the
   ServiceAccount (`automountServiceAccountToken: false`) and bindings, and marks
   the observed generation `Ready`. The API waits for this state with a bounded
   timeout; it never issues against stale or partially applied status.
4. The API creates a session-handle Secret containing no token data, requests a
   token bound to its UID, constructs the kubeconfig in memory, and returns it
   once. The server URL and CA come from trusted deployment configuration, never
   forwarded request headers.
5. `kubectl` presents the ServiceAccount token to kube-apiserver. Kubernetes
   authenticates it and evaluates the current bindings on every request.
6. A session is revoked by deleting its handle; all sessions are revoked by
   disabling/deleting the lease or ServiceAccount. Expiry reconciliation is a
   cleanup mechanism in addition to the token's cryptographic expiration.

#### Effective-access search

Given a username, the admin-only search endpoint applies the same explicit header
validation rules, optionally resolves its directory identity and current groups,
evaluates all matching mappings, and returns a namespace/permission-set matrix
with mapping provenance. It presents separately:

- **Desired** access computed from current policy and directory state.
- **Observed** managed Roles, bindings, ServiceAccount, and controller conditions.
- **Unmanaged** access only if an optional diagnostic scan finds direct external
  bindings for the exact Kubernetes username; this is informational because the
  system does not claim ownership of it.

When LDAP cannot provide sufficiently fresh membership, search returns exact-user
results as known and group-derived results as `unknown`; stale group data is
never presented as a current authorization answer.

### Correctness, reliability, and availability

- **Desired-state source:** Kubernetes CRs are the source of truth. There is no
  mutable in-memory-only policy or separate application database.
- **Idempotence and drift repair:** generated objects are applied from normalized
  desired state with a dedicated field manager. Watches plus a periodic full
  resync repair deletion or edits. Fields owned by this controller are restored;
  foreign fields are left alone unless they make the object unsafe, in which case
  reconciliation reports a conflict and stops issuing credentials for it.
- **Fail-closed issuance:** unresolved references and expired directory evidence
  remove the affected bindings. Stale lease status or incomplete reconciliation
  prevents all token issuance until the resulting reduced state has converged.
  LDAP uncertainty never adds grants: exact-user access remains available, while
  group access has the explicit bounded-staleness window.
- **HA:** run at least three controller replicas (one elected writer) and three
  active-active API replicas across failure domains. Use readiness probes that
  include required local configuration but do not eject every API pod during a
  shared LDAP outage; dependency failure is reported per request. Liveness checks
  only process health. PodDisruptionBudgets and graceful shutdown preserve
  availability during rollout.
- **No split policy decisions:** one evaluator library and content digests are
  used everywhere. A token is issued only for the exact lease generation and
  policy digest observed by the controller. Changes racing issuance cause retry,
  not issuance from mixed generations.
- **Deletion and revocation:** finalizers ensure generated access is removed
  before source records disappear. Token TTL limits the damage from a missed
  cleanup; binding removal limits authorization immediately after reconciliation.
- **Backpressure:** rate-limited work queues, bounded LDAP concurrency, API
  request limits, server timeouts, and pagination prevent a directory or policy
  spike from exhausting the process. Work is keyed and de-duplicated.
- **Recovery:** all durable state needed to reconstruct generated resources is in
  the Kubernetes API. Backups follow the cluster's etcd/CR backup policy. Session
  tokens do not need recovery; users request replacements.

### Security and privacy

- Treat proxy-header configuration as part of the authentication system. Strip
  identity headers at the edge, deny direct API access, use TLS end to end, and
  test spoofed/multiple-header cases.
- Store the LDAP bind password in an externally managed Kubernetes Secret and
  mount it only into processes that query LDAP. Require certificate validation;
  plain LDAP is rejected by default.
- Separate controller and API Kubernetes identities. Neither application
  identity is granted `cluster-admin`. The controller receives only the explicit
  role/binding/ServiceAccount and CRD permissions it needs, including narrowly
  understood `bind`/`escalate` capability. The API's sensitive
  `serviceaccounts/token` permission is documented and isolated.
- Restrict CRD writes to trusted policy administrators: permission to define a
  role is permission to grant everything that role contains. UI warnings are not
  an authorization boundary.
- Use CSRF protection and same-site secure cookies for browser mutations, a
  restrictive Content Security Policy, request-size limits, dependency scanning,
  and container/image signing. Never expose LDAP bind credentials or bearer
  tokens to the browser except the one-time kubeconfig response requested by that
  same principal.
- Audit policy mutations, effective-grant changes, issuance metadata, renewal,
  and revocation with actor, source UID, request ID, and timestamps, but never a
  token or kubeconfig. Make username logging configurable and hash it in metrics.
- ServiceAccounts live in a dedicated namespace with ResourceQuota and no user
  workload permissions. Granting workload creation in a target namespace still
  has Kubernetes's normal privilege-escalation implications and is visibly
  flagged.

### Observability

Expose Prometheus metrics for reconciliation duration/error/requeue counts,
ready/degraded resources, policy convergence lag, active/expired leases,
issuance/renewal/revocation outcomes, LDAP latency/cache/failure, and leader
status. Avoid identity-bearing metric labels. Emit structured JSON logs with
request and resource correlation IDs and Kubernetes Events for actionable
resource failures. Provide `/livez`, `/readyz`, and a dependency-status endpoint.
Alert on sustained reconciliation errors, policy lag, LDAP staleness approaching
the revocation bound, token-request failures, and absence of an elected leader.

### Technology choices

- **Go, client-go, and controller-runtime/Kubebuilder:** native Kubernetes API
  types, established watch/reconcile patterns, leader election, caches, field
  indexes, metrics, and envtest support.
- **Kubernetes CRDs:** durable, auditable desired state with existing RBAC,
  backup, watch, and optimistic-concurrency behavior.
- **TokenRequest with Secret-bound tokens:** expiring credentials and per-session
  early revocation without persisted token material. Legacy non-expiring
  ServiceAccount token Secrets are intentionally unsupported.
- **go-ldap/ldap:** direct, limited LDAP/AD integration behind a small interface
  that can be faked in tests; directory schema and nested-group strategy remain
  configurable.
- **Go HTTP API plus React/TypeScript/Vite UI:** typed backend integration and a
  maintainable form/search experience; compiled assets are embedded into the API
  image so no separate UI runtime is required.
- **Prometheus-format metrics and structured logs:** align with common Kubernetes
  operations without requiring a specific monitoring vendor.
- **kind plus envtest:** envtest gives fast reconciliation tests; kind supplies a
  real API server, RBAC authorizer, ServiceAccount issuer, TokenRequest behavior,
  leader-election tests, and upgrade/install testing that envtest alone cannot.

## Proposed project layout

```text
.
├── api/
│   └── v1alpha1/                 # CRD Go types and generated deepcopy code
├── cmd/
│   ├── controller/               # controller manager entry point
│   ├── server/                   # API and embedded UI entry point
│   └── rbacctl/                  # optional exec-credential/admin CLI
├── config/
│   ├── crd/                      # generated CRD schemas and CEL validation
│   ├── default/                  # kustomize composition
│   ├── manager/                  # Deployments, Services, probes, security
│   ├── rbac/                     # least-privilege installation RBAC
│   ├── network-policy/
│   ├── samples/                  # safe example PermissionSets/Mappings
│   └── kind/                     # development and integration overlays
├── internal/
│   ├── controller/               # permission, mapping, and lease reconcilers
│   ├── credentials/              # sessions, TokenRequest, revocation
│   ├── directory/                # LDAP interface, AD implementation, cache
│   ├── kubeconfig/               # pure kubeconfig construction/redaction
│   ├── managed/                  # labels, ownership, naming, SSA helpers
│   ├── policy/                   # deterministic evaluator and explanations
│   ├── server/                   # routes, authn/authz, API models, audit
│   └── status/                   # common conditions and Events
├── web/                          # React/TypeScript UI source and tests
├── test/
│   ├── envtest/                  # controller integration tests
│   ├── kind/                     # real-cluster and HA/failure tests
│   ├── ldap/                     # disposable directory fixtures
│   └── e2e/                      # proxy → API → kubeconfig → kube-apiserver
├── charts/rbac-controller/       # Helm packaging after manifests stabilize
├── docs/                         # API, operations, security, and threat model
├── hack/                         # generation, verification, kind helpers
├── DESIGN.md
├── LICENSE                       # GNU AGPL v3 text
├── Makefile
├── go.mod
└── README.md
```

Generated files are reproducible and checked by CI. Go source files use
`SPDX-License-Identifier: AGPL-3.0-only`; compatible notices are added to web and
generated artifacts as appropriate. Dependency licenses are audited in CI. The
repository is distributed under the GNU Affero General Public License version 3
only (`AGPL-3.0-only`).

## Testing and release strategy

### Test layers

- Unit-test rule normalization, exact identity matching, additive/de-duplicated
  evaluation, naming, lifecycle clocks, LDAP escaping, kubeconfig construction,
  and token redaction. Use a fake clock and property/fuzz tests for policy input.
- Use controller-runtime envtest to verify create/update/delete, owner collision,
  finalizers, status generations, dependency indexing, drift repair, conflicts,
  namespace deletion, and restart idempotence.
- Run kind tests against supported Kubernetes minor versions. Verify actual RBAC
  with `SelfSubjectAccessReview`/API calls using an issued kubeconfig, bound-token
  expiry and Secret/ServiceAccount revocation, leader failover, controller restart,
  concurrent policy edits, and clean uninstall.
- Run an LDAP fixture for basic schema behavior and a separate AD-compatible
  integration lane for nested groups and stable IDs; do not claim AD parity from
  an OpenLDAP-only test.
- Exercise the end-to-end proxy trust boundary, including stripped/spoofed
  headers, admin authorization, CSRF, LDAP outage/staleness, policy-change races,
  one-time response caching controls, and log scanning for token leakage.
- Add scale tests for users × mappings × namespaces, API object counts, reconcile
  latency, LDAP query load, and kube-apiserver throttling before beta.

### Compatibility and rollout

Publish a supported Kubernetes version matrix based on versions exercised in CI;
do not silently depend on the development cluster version. Upgrade CRDs before
Deployments, use conversion only when a second stored API version is introduced,
and keep rollback instructions. Alpha releases make no storage-version stability
promise, but reconciliation and deletion safety are required from the first
release.

## Milestones

### 0. Validate the design and threat model

- Resolve the clarifying questions below.
- Write the threat model, identity-header deployment contract, lifecycle state
  machine, API conventions, and initial SLOs.
- Define the Kubernetes compatibility matrix and AGPL-3.0-only licensing policy.

Exit criterion: reviewers agree on identity semantics, authorization scope,
credential lifetime/revocation, and ownership boundaries.

### 1. Scaffold APIs and deterministic policy evaluation

- Initialize Go/Kubebuilder, license files, generation, linting, and CI.
- Define `PermissionSet`, `AccessMapping`, and `CredentialLease` schemas with
  validation, conditions, samples, and API documentation.
- Implement and thoroughly test the pure evaluator, normalization, provenance,
  naming, labels, and ownership checks.

Exit criterion: CRDs install cleanly and fixture policies produce stable,
explainable effective grants without creating credentials.

### 2. Reconcile permission and mapping resources

- Reconcile Roles/ClusterRoles and dependency-driven materialization.
- Add finalizers, server-side apply, drift repair, collision handling, indexes,
  Events, metrics, and status.
- Reconcile bindings initially against test ServiceAccounts; exercise lifecycle
  and namespace deletion in envtest/kind.

Exit criterion: desired RBAC converges after create/update/delete, drift, restart,
and leader failover, with no orphaned access in failure tests.

### 3. LDAP resolution and credential leases

- Add TLS-only AD lookup, stable identity/group handling, nested group support,
  timeouts/cache, and bounded-staleness behavior.
- Reconcile leases, ServiceAccounts, per-lease bindings, periodic re-evaluation,
  disable/expiry, and ordered revocation.
- Add fake-directory, OpenLDAP, and AD-compatible test lanes.

Exit criterion: exact-user and group changes converge within their documented
bounds, and LDAP failures follow the specified fail-closed behavior.

### 4. Secure issuance API and kubeconfig flow

- Implement trusted-header middleware, route authorization, audit records,
  session-handle Secrets, bound TokenRequests, in-memory kubeconfig generation,
  renewal/revocation, and response/log redaction.
- Split API/controller permissions and add hardened manifests/NetworkPolicies.
- Complete proxy-to-kube-apiserver kind end-to-end tests.

Exit criterion: an authenticated user can obtain and use only their current
permissions; session, lease, group, and policy revocation tests all pass.

### 5. Administration UI and access search

- Implement policy list/detail/edit flows, validation and risk warnings.
- Add desired-versus-observed user search with grant provenance and degraded
  directory states.
- Add self-service credential/session views and revocation.
- Complete accessibility, browser security, optimistic-concurrency, and UI tests.

Exit criterion: routine policy and credential operations require neither direct
CR editing nor hidden state, and every effective grant can be explained.

### 6. Production hardening and beta

- Establish SLO dashboards/alerts, load limits, disruption/failover tests,
  backup/restore and upgrade/rollback procedures, and security review.
- Test the supported Kubernetes matrix, LDAP/AD variants, scale targets, and
  dependency/image/license scanning.
- Add Helm packaging after raw manifests and APIs stabilize. Implement the exec
  credential helper only after interactive authentication is settled.

Exit criterion: operational runbooks, compatibility guarantees, scale evidence,
and a reviewed threat model support a beta release.

## Clarifying questions and working assumptions

These are ordered roughly by how much the answers could change the architecture.
Implementation can proceed using the stated assumptions if no answer is given.

1. **What authenticates users before `X-Remote-User` reaches this service, and
   how can a CLI renew credentials?**

   Assumption: a highly available enterprise reverse proxy performs interactive
   authentication, strips client-supplied identity headers, and is the only
   network peer allowed to call the service. Browser download is sufficient for
   the first release. An exec helper is deferred until there is an approved CLI
   flow such as OIDC device authorization; it will not synthesize a trusted
   header locally.

2. **Should mappings grant the external Kubernetes `User`/`Group` directly, or
   grant a controller-created ServiceAccount used by kubeconfigs?**

   Assumption: v1 mappings determine permissions for a per-principal managed
   ServiceAccount. The external username identifies the broker caller but is not
   bound directly. Optional discovery of direct unmanaged user bindings is
   read-only. Choosing native user/group bindings instead would remove much of
   the credential-lease design and require the cluster authenticator to supply
   matching user/group identities.

3. **Is this a single-cluster controller or a central service for many target
   clusters?**

   Assumption: one installation manages and issues credentials for its local
   cluster. Multi-cluster support would require per-cluster trust/configuration,
   remote clients, partial-failure semantics, and a different credential store
   and is intentionally not hidden in the first API.

4. **What AD schema and group-membership semantics are authoritative?**

   Assumption: `X-Remote-User` is validated and matched exactly, with no implicit
   case folding or aliasing. When LDAP is needed, that value is looked up through
   an explicitly configured attribute; groups are matched transitively using a
   stable object GUID (with canonical DN as a configurable alternative), over
   LDAPS using a read-only service account. Any username normalization, forest/
   domain boundaries, disabled-user handling, and nested-group query strategy
   are explicit configuration documented for the target AD deployment.

5. **What are the required token lifetime, renewal window, inactivity policy,
   revocation bound, and availability SLO?**

   Assumption: tokens last at most one hour, leases may renew for eight hours
   after interactive authentication, active LDAP membership is refreshed every
   five minutes, and group information may be stale for at most fifteen minutes.
   Three replicas per component target 99.9% service availability. Security can
   shorten these values; kube-apiserver configuration may impose a lower token
   maximum.

6. **Can several matching mappings combine, and are deny or precedence rules
   required?**

   Assumption: all matching exact-user and LDAP-group mappings are unioned and
   de-duplicated. There is no deny, priority, or override. If deny semantics are
   required, Kubernetes RBAC alone cannot represent them and the design needs an
   authorization webhook or a constrained admission layer.

7. **May mappings target namespaces by selector, and may administrators grant
   cluster-wide/high-risk rules?**

   Assumption: v1 names exact namespaces only. Trusted policy administrators may
   define cluster-wide and high-risk rules after UI warnings and audit; ordinary
   users cannot write policy CRs. Namespace selectors and policy guardrails can
   be added after their ownership and escalation consequences are agreed.

8. **May the controller adopt or modify existing Roles, bindings, and
   ServiceAccounts, and what should uninstall do?**

   Assumption: it exclusively owns only deterministically named, correctly
   labeled objects that it created. It never adopts a collision. Deleting source
   CRs revokes and deletes generated objects; uninstall documentation requires
   deleting managed CRs and waiting for finalizers before removing the controller
   and CRDs. An explicit orphan/adoption policy, if desired, needs separate API
   design and safeguards.

9. **Are users allowed multiple independently permissioned identities or client
   profiles?**

   Assumption: one ServiceAccount and effective permission union per stable user
   identity, with multiple independently revocable token sessions. If separate
   privilege profiles (for example, normal versus production emergency access)
   are required, `CredentialLease` needs a profile/approval dimension and the UI
   must prevent silent privilege union.
