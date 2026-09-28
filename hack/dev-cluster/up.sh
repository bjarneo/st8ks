#!/usr/bin/env bash
# Creates a local kind cluster with workloads that fail in the ways st8ks
# detects: an OOM crash loop, a missing image, a pod that cannot be
# scheduled, a failed job, and a Helm release with a failed upgrade.
#
#   hack/dev-cluster/up.sh              create the cluster, or keep it if it exists
#   hack/dev-cluster/up.sh --recreate   delete it first, then create it again
#
# Requires Docker. kind and helm come from PATH, or the script downloads
# pinned versions into hack/dev-cluster/.cache/bin.
#
# The kubeconfig goes to ~/.kube/st8ks-dev.yaml. st8ks finds it there
# without changes to ~/.kube/config. Set ST8KS_DEV_KUBECONFIG to use another
# path.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=hack/dev-cluster/lib.sh
source ./lib.sh

if [ "${1:-}" = "--recreate" ]; then
  ./down.sh
fi

need_docker
ensure_tools

if "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  "$KIND" export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG_OUT" >/dev/null 2>&1
  say "Cluster $CLUSTER already runs. Use --recreate or 'make cluster-reset' for a fresh one."
  print_usage
  exit 0
fi

say "Creating kind cluster $CLUSTER with three nodes"
mkdir -p "$(dirname "$KUBECONFIG_OUT")"
"$KIND" create cluster --name "$CLUSTER" --config kind-config.yaml --kubeconfig "$KUBECONFIG_OUT" --wait 180s
chmod 600 "$KUBECONFIG_OUT"
export KUBECONFIG="$KUBECONFIG_OUT"

say "Applying the seed workloads"
kubectl apply -f - < seed.yaml >/dev/null

say "Installing metrics-server"
curl -fsSL https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml \
  | sed 's/        - --metric-resolution=15s/        - --metric-resolution=15s\n        - --kubelet-insecure-tls/' \
  | kubectl apply -f - >/dev/null

say "Cordoning $CLUSTER-worker2, so a pending pod has a node to uncordon"
kubectl cordon "$CLUSTER-worker2" >/dev/null

say "Installing Helm release prod/web, then upgrading it to an image that does not exist"
"$HELM" install web ./chart -n prod >/dev/null
"$HELM" upgrade web ./chart -n prod --set image.tag=1.99-missing --wait --timeout 25s >/dev/null 2>&1 || true

say "Done. The failures show in st8ks within a minute, while the pods start and crash."
print_usage
