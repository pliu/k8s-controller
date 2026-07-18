#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail

cluster=k8s-controller-test
image=ghcr.io/pliu/k8s-controller:latest

cleanup() {
  kind delete cluster --name "$cluster"
}
trap cleanup EXIT

kind create cluster --name "$cluster" --wait 60s
docker build -t "$image" .
kind load docker-image "$image" --name "$cluster"
# Dev mode is enabled only in this disposable test cluster so the mutating API
# can be exercised without changing the default installation.
kubectl apply -k hack/kind-test
kubectl -n k8s-controller rollout status deployment/k8s-controller --timeout=120s
kubectl apply -f config/sample.yaml

# The operator creates the namespace, its ResourceQuota, and the RoleBindings for
# each AccessMapping (group or user subjects bound directly). Nothing is created
# up front; the ManagedNamespace drives it all.
for _ in {1..30}; do
  kubectl get namespace team-a >/dev/null 2>&1 && break
  sleep 1
done
kubectl get namespace team-a
for _ in {1..30}; do
  kubectl -n team-a get rolebinding -l "k8s.pliu.dev/owner-name=team-a" -o name | grep -q rolebinding && break
  sleep 1
done
kubectl -n team-a get rolebinding -l "k8s.pliu.dev/owner-name=team-a" -o name | grep -q rolebinding
kubectl -n team-a get resourcequota -l "k8s.pliu.dev/owner-name=team-a" -o name | grep -q resourcequota

# The ClusterAccessMapping reconciles into a cluster-wide ClusterRoleBinding.
for _ in {1..30}; do
  kubectl get clusterrolebinding -l "k8s.pliu.dev/owner-name=cluster-pod-readers" -o name | grep -q clusterrolebinding && break
  sleep 1
done
kubectl get clusterrolebinding -l "k8s.pliu.dev/owner-name=cluster-pod-readers" -o name | grep -q clusterrolebinding

kubectl -n k8s-controller run api-client --image=curlimages/curl:8.12.1 \
  --restart=Never --command -- sleep 300
kubectl -n k8s-controller wait --for=condition=Ready pod/api-client --timeout=120s
api_url=http://k8s-controller
kubectl -n k8s-controller exec api-client -- curl -fsS "$api_url/livez" >/dev/null

# Every replica serves HTTP metrics, while only the leader necessarily has a
# reconcile series. Scrape all replicas so both sides of the shared registry are
# tested without depending on which pod the Service selects.
metrics=""
for server_ip in $(kubectl -n k8s-controller get pod -l app=k8s-controller \
  -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}'); do
  kubectl -n k8s-controller exec api-client -- curl -fsS "http://$server_ip:8080/livez" >/dev/null
  metrics+=$(kubectl -n k8s-controller exec api-client -- curl -fsS "http://$server_ip:8080/metrics")
done
grep -q controller_runtime_reconcile_total <<<"$metrics"
grep -q k8s_controller_http_requests_total <<<"$metrics"

# Dev mode creates a user-specific ManagedNamespace from the trusted proxy
# header and reconciles cluster-admin through a namespace-scoped RoleBinding.
kubectl -n k8s-controller exec api-client -- curl -fsS -X POST \
  -H 'X-Remote-User: alice' "$api_url/api/v1/managednamespaces" >/dev/null
for _ in {1..30}; do
  kubectl get namespace user-alice >/dev/null 2>&1 && break
  sleep 1
done
kubectl get namespace user-alice
for _ in {1..30}; do
  binding=$(kubectl -n user-alice get rolebinding \
    -l 'k8s.pliu.dev/owner-name=user-alice' \
    -o jsonpath='{.items[0].roleRef.name} {.items[0].subjects[0].kind} {.items[0].subjects[0].name}' 2>/dev/null || true)
  [[ "$binding" == "cluster-admin User alice" ]] && break
  sleep 1
done
[[ "$binding" == "cluster-admin User alice" ]]

# Deleting through the dev API removes both the ManagedNamespace and, via its
# opt-in cleanup label, the underlying Namespace.
kubectl -n k8s-controller exec api-client -- curl -fsS -X DELETE \
  -H 'X-Remote-User: alice' "$api_url/api/v1/managednamespaces" >/dev/null
for _ in {1..60}; do
  if ! kubectl get managednamespace user-alice >/dev/null 2>&1 && \
     ! kubectl get namespace user-alice >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
! kubectl get managednamespace user-alice >/dev/null 2>&1
! kubectl get namespace user-alice >/dev/null 2>&1

# An ordinary ManagedNamespace retains its Namespace when it is deleted.
kubectl delete managednamespace team-a --wait=true --timeout=60s
kubectl get namespace team-a >/dev/null
