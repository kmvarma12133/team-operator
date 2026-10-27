#!/usr/bin/env bash
# End to end on a kind cluster: install the chart with a locally built image,
# create a Team, then check what a real cluster does with what the operator made.
# Expects IMG to be loaded into kind already (CI does `kind load docker-image`).
set -euo pipefail

IMG="${IMG:?set IMG to the image loaded into kind}"
NS=team-operator-system
TEAM_NS=team-payments

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

step "Install the chart"
helm upgrade --install team-operator charts/team-operator \
  --namespace "$NS" --create-namespace \
  --set image.repository="${IMG%:*}" --set image.tag="${IMG##*:}" --set image.pullPolicy=Never \
  --wait --timeout 3m
kubectl -n "$NS" get pods

step "Create a Team"
kubectl apply -f config/samples/team.yaml
kubectl wait team/payments --for=condition=Ready --timeout=60s
kubectl get team payments

step "Namespace carries the ownership and Pod Security labels"
kubectl get namespace "$TEAM_NS" --show-labels
[[ "$(kubectl get ns "$TEAM_NS" -o jsonpath='{.metadata.labels.owner}')" == payments ]] || fail "owner label"
[[ "$(kubectl get ns "$TEAM_NS" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')" == restricted ]] || fail "pod security label"

step "Members can deploy in their namespace and nowhere else"
[[ "$(kubectl auth can-i create deployments -n "$TEAM_NS" --as=dev@example.com --as-group=payments-devs)" == yes ]] || fail "member cannot deploy"
[[ "$(kubectl auth can-i create deployments -n default --as=dev@example.com --as-group=payments-devs || true)" == no ]] || fail "member can deploy outside the team"
[[ "$(kubectl auth can-i create rolebindings -n "$TEAM_NS" --as=dev@example.com --as-group=payments-devs || true)" == no ]] || fail "member can grant access"

step "A privileged pod is rejected by Pod Security admission"
err="$(mktemp)"
if kubectl -n "$TEAM_NS" run privileged --image=busybox --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"privileged","image":"busybox","securityContext":{"privileged":true}}]}}' 2>"$err"; then
  fail "privileged pod was admitted"
fi
cat "$err"

step "A container without resources gets the LimitRange defaults"
kubectl -n "$TEAM_NS" apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: defaults
  labels:
    owner: payments
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.10
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
EOF
limits="$(kubectl -n "$TEAM_NS" get pod defaults -o jsonpath='{.spec.containers[0].resources.limits.memory}')"
[[ "$limits" == 512Mi ]] || fail "memory limit is '$limits', want 512Mi"
echo "memory limit defaulted to $limits"

step "Manual changes are reverted"
kubectl -n "$TEAM_NS" patch resourcequota team-quota --type=merge -p '{"spec":{"hard":{"requests.cpu":"100"}}}'
kubectl -n "$TEAM_NS" delete networkpolicy default-deny
for _ in $(seq 30); do
  cpu="$(kubectl -n "$TEAM_NS" get resourcequota team-quota -o jsonpath='{.spec.hard.requests\.cpu}')"
  if [[ "$cpu" == 8 ]] && kubectl -n "$TEAM_NS" get networkpolicy default-deny >/dev/null 2>&1; then
    echo "quota back to $cpu CPUs, default-deny policy recreated"
    break
  fi
  sleep 1
done
[[ "$cpu" == 8 ]] || fail "quota not reverted: $cpu"

step "A namespace the operator did not create is never adopted"
kubectl create namespace team-legacy
kubectl apply -f - <<'EOF'
apiVersion: tenancy.kmvarma12133.github.io/v1alpha1
kind: Team
metadata:
  name: legacy
spec:
  members:
    - kind: Group
      name: legacy-devs
EOF
kubectl wait team/legacy --for=jsonpath='{.status.conditions[?(@.type=="Ready")].reason}'=NamespaceConflict --timeout=60s
kubectl get team legacy

step "Deleting a Retain team keeps its namespace"
kubectl delete team payments --wait
kubectl get namespace "$TEAM_NS" >/dev/null || fail "namespace deleted with a Retain team"
echo "namespace $TEAM_NS kept"

step "All checks passed"
