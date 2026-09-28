#!/usr/bin/env bash
# Adds many ConfigMaps to the dev cluster, to test st8ks with large lists.
#
#   hack/dev-cluster/load.sh          6000 ConfigMaps in namespace load
#   hack/dev-cluster/load.sh 20000
#   hack/dev-cluster/load.sh --churn  relabel all of them, to test live updates
#   hack/dev-cluster/load.sh --remove
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=hack/dev-cluster/lib.sh
source ./lib.sh
need_docker

case "${1:-}" in
  --remove)
    kubectl delete namespace load --ignore-not-found
    exit 0
    ;;
  --churn)
    say "Relabeling every ConfigMap in namespace load"
    kubectl label cm --all -n load "churn=$(date +%s)" --overwrite >/dev/null
    exit 0
    ;;
esac

COUNT="${1:-6000}"
say "Creating $COUNT ConfigMaps in namespace load"
{
  printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: load\n'
  for ((i = 0; i < COUNT; i++)); do
    printf -- '---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg-%05d\n  namespace: load\n  labels:\n    app: svc-%d\ndata:\n  key: value-%d\n' "$i" "$((i % 40))" "$i"
  done
} | kubectl apply --server-side -f - >/dev/null
say "Done. Open Config > Config Maps in st8ks."
