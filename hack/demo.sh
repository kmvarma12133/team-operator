#!/usr/bin/env bash
# The recorded demo: one Team becomes a guarded namespace, a manual change is
# reverted, and a foreign namespace is left alone. Run after the chart is
# installed (the demo workflow does that first).
set -euo pipefail

say() { printf '\n\033[1;36m# %s\033[0m\n' "$*"; sleep 1; }
run() { printf '\033[1m$ %s\033[0m\n' "$*"; eval "$@"; sleep 2; }

say "A team asks for a namespace with one small resource"
run cat config/samples/team.yaml
run kubectl apply -f config/samples/team.yaml
run kubectl wait team/payments --for=condition=Ready --timeout=60s
run kubectl get team payments

say "The operator made the namespace and its guardrails"
run kubectl -n team-payments get resourcequota,limitrange,networkpolicy,rolebinding

say "Members can deploy here, and cannot hand out access"
run kubectl auth can-i create deployments -n team-payments --as=dev@example.com --as-group=payments-devs
run "kubectl auth can-i create rolebindings -n team-payments --as=dev@example.com --as-group=payments-devs || true"

say "Someone raises the quota by hand..."
run "kubectl -n team-payments patch resourcequota team-quota --type=merge -p '{\"spec\":{\"hard\":{\"requests.cpu\":\"100\"}}}'"
sleep 3
say "...and the operator puts it back"
run "kubectl -n team-payments get resourcequota team-quota -o jsonpath='{.spec.hard.requests\.cpu}{\"\\n\"}'"

say "A Team never takes over a namespace someone else created"
run kubectl create namespace team-legacy
run "kubectl create -f - <<'EOF'
apiVersion: tenancy.kmvarma12133.github.io/v1alpha1
kind: Team
metadata:
  name: legacy
spec:
  members: [{kind: Group, name: legacy-devs}]
EOF"
sleep 3
run kubectl get teams
