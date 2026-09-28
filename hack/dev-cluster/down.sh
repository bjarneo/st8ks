#!/usr/bin/env bash
# Deletes the local kind cluster that up.sh created and its kubeconfig.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=hack/dev-cluster/lib.sh
source ./lib.sh

need_docker
ensure_tools

if "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  say "Deleting kind cluster $CLUSTER"
  "$KIND" delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG_OUT"
else
  say "Cluster $CLUSTER does not exist"
fi
rm -f "$KUBECONFIG_OUT"
