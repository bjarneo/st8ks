#!/usr/bin/env bash
# Installs Prometheus in the namespace monitoring of the local cluster. Only
# the server of the prometheus-community chart is installed. It scrapes
# cAdvisor on every kubelet, which gives the CPU and memory history for the
# Metrics tab.
#
#   hack/dev-cluster/prometheus.sh            install Prometheus, or upgrade it
#   hack/dev-cluster/prometheus.sh --delete   uninstall Prometheus
#
# Run hack/dev-cluster/up.sh first. helm comes from PATH, or the script
# downloads a pinned version into hack/dev-cluster/.cache/bin.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=hack/dev-cluster/lib.sh
source ./lib.sh

CHART_REPO="https://prometheus-community.github.io/helm-charts"
CHART_VERSION="29.40.0"
NAMESPACE="monitoring"

need_docker
ensure_tools
[ -f "$KUBECONFIG_OUT" ] || die "$KUBECONFIG_OUT does not exist. Run 'make cluster-up' first."
export KUBECONFIG="$KUBECONFIG_OUT"

if [ "${1:-}" = "--delete" ]; then
  say "Uninstalling Prometheus from namespace $NAMESPACE"
  "$HELM" uninstall prometheus -n "$NAMESPACE" --ignore-not-found >/dev/null
  exit 0
fi

say "Installing Prometheus chart $CHART_VERSION in namespace $NAMESPACE"
"$HELM" upgrade --install prometheus prometheus --repo "$CHART_REPO" --version "$CHART_VERSION" \
  -n "$NAMESPACE" --create-namespace \
  --set alertmanager.enabled=false \
  --set kube-state-metrics.enabled=false \
  --set prometheus-node-exporter.enabled=false \
  --set prometheus-pushgateway.enabled=false \
  --set server.persistentVolume.enabled=false \
  --set server.global.scrape_interval=15s \
  --set server.retention=7d \
  --wait --timeout 5m >/dev/null

cat <<EOF

  Service:  $NAMESPACE/prometheus-server, port 80 (http)

  st8ks finds this service by itself. Open a pod and press m for the
  Metrics tab. The first points show after about 30 seconds.

  Uninstall:  hack/dev-cluster/prometheus.sh --delete
EOF
