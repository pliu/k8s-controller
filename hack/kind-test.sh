#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail

cluster=rbac-controller-test
image=ghcr.io/pliu/rbac-controller:latest

cleanup() {
  if [[ -n "${forward_pid:-}" ]]; then kill "$forward_pid" 2>/dev/null || true; fi
  kind delete cluster --name "$cluster"
}
trap cleanup EXIT

kind create cluster --name "$cluster" --wait 60s
docker build -t "$image" .
kind load docker-image "$image" --name "$cluster"
kubectl apply -k config
kubectl -n rbac-controller rollout status deployment/rbac-controller --timeout=120s
kubectl create namespace team-a
kubectl apply -f config/sample.yaml

# The controller reconciles the AccessMapping into a namespace RoleBinding whose
# subjects are the mapping's users and groups directly; no ServiceAccount is created.
for _ in {1..30}; do
  kubectl -n team-a get rolebinding -l "rbac.pliu.dev/owner-name=team-readers" -o name | grep -q rolebinding && break
  sleep 1
done
kubectl -n team-a get rolebinding -l "rbac.pliu.dev/owner-name=team-readers" -o name | grep -q rolebinding

kubectl -n rbac-controller port-forward service/rbac-controller 18080:80 >"${TMPDIR:-/tmp}/rbac-controller-port-forward.log" 2>&1 &
forward_pid=$!
for _ in {1..30}; do
  curl -fsS http://127.0.0.1:18080/livez >/dev/null && break
  sleep 1
done
curl -fsS http://127.0.0.1:18080/livez >/dev/null
