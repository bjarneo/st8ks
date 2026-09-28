#!/usr/bin/env bash
# Shared settings and helpers for the dev cluster scripts.
# shellcheck disable=SC2034

CLUSTER="${ST8KS_DEV_CLUSTER:-st8ks-dev}"
KUBECONFIG_OUT="${ST8KS_DEV_KUBECONFIG:-$HOME/.kube/st8ks-dev.yaml}"
KIND_VERSION="v0.33.0"
HELM_VERSION="v3.22.0"
TOOLS="$PWD/.cache/bin"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

need_docker() {
  command -v docker >/dev/null 2>&1 || die "Docker is required. Install Docker and start it."
  docker info >/dev/null 2>&1 || die "Docker does not answer. Start Docker, or add your user to the docker group."
}

platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported CPU $(uname -m)" ;;
  esac
  echo "$os-$arch"
}

# ensure_tools sets KIND and HELM. It uses the tools on PATH when they
# exist, and downloads pinned versions otherwise.
ensure_tools() {
  local plat
  plat="$(platform)"
  mkdir -p "$TOOLS"
  if command -v kind >/dev/null 2>&1; then
    KIND="$(command -v kind)"
  else
    KIND="$TOOLS/kind-$KIND_VERSION"
    if [ ! -x "$KIND" ]; then
      say "Downloading kind $KIND_VERSION"
      curl -fsSLo "$KIND" "https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-$plat"
      chmod +x "$KIND"
    fi
  fi
  if command -v helm >/dev/null 2>&1; then
    HELM="$(command -v helm)"
  else
    HELM="$TOOLS/helm-$HELM_VERSION"
    if [ ! -x "$HELM" ]; then
      say "Downloading helm $HELM_VERSION"
      curl -fsSL "https://get.helm.sh/helm-$HELM_VERSION-$plat.tar.gz" | tar -xzO "$plat/helm" > "$HELM"
      chmod +x "$HELM"
    fi
  fi
}

# kubectl runs inside the control plane container, so the host needs no
# kubectl.
kubectl() { docker exec -i "$CLUSTER-control-plane" kubectl "$@"; }

print_usage() {
  cat <<EOF

  Kubeconfig:  $KUBECONFIG_OUT
  Context:     kind-$CLUSTER

  st8ks finds this file in ~/.kube and lists the context in the rail.
  Start st8ks against only this cluster:
    make dev                                    (finds it through the ~/.kube scan)
    build/bin/st8ks --kubeconfig $KUBECONFIG_OUT

  Tear down:   make cluster-down
  Start over:  make cluster-reset
  Load test:   hack/dev-cluster/load.sh 6000
EOF
}
