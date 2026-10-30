#!/usr/bin/env bash
# Adds Argo CD and Flux objects to the local kind cluster, so st8ks shows its
# GitOps issues. The script installs only the CRDs, at pinned versions from
# the official release manifests. No controller runs, so the script sets the
# status of each object with kubectl patch. The objects go to namespace
# gitops: one healthy object and one failing object for each issue rule.
#
#   hack/dev-cluster/gitops.sh            install the CRDs and the objects
#   hack/dev-cluster/gitops.sh --remove   delete namespace gitops and the CRDs
#
# Run it again to reset the objects and their status.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=hack/dev-cluster/lib.sh
source ./lib.sh

ARGOCD_VERSION="v3.5.6"
# The controller versions of Flux v2.9.6.
FLUX_SOURCE_VERSION="v1.9.6"
FLUX_KUSTOMIZE_VERSION="v1.9.6"
FLUX_HELM_VERSION="v1.6.5"

CRDS=(
  applications.argoproj.io
  gitrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io
  helmrepositories.source.toolkit.fluxcd.io helmcharts.source.toolkit.fluxcd.io
  buckets.source.toolkit.fluxcd.io externalartifacts.source.toolkit.fluxcd.io
  kustomizations.kustomize.toolkit.fluxcd.io helmreleases.helm.toolkit.fluxcd.io
)

need_docker
docker inspect "$CLUSTER-control-plane" >/dev/null 2>&1 || die "Cluster $CLUSTER does not run. Run 'make cluster-up' first."

if [ "${1:-}" = "--remove" ]; then
  say "Deleting namespace gitops and the Argo CD and Flux CRDs"
  kubectl delete namespace gitops --ignore-not-found >/dev/null
  for crd in "${CRDS[@]}"; do
    kubectl delete crd "$crd" --ignore-not-found >/dev/null
  done
  exit 0
fi

# crd URL installs a CRD manifest. Server-side apply skips the
# last-applied annotation, which is too large for these CRDs.
crd() { curl -fsSL "$1" | kubectl apply --server-side --force-conflicts -f - >/dev/null; }

say "Installing the Argo CD Application CRD of Argo CD $ARGOCD_VERSION"
crd "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/crds/application-crd.yaml"
say "Installing the Flux CRDs of source-controller $FLUX_SOURCE_VERSION, kustomize-controller $FLUX_KUSTOMIZE_VERSION and helm-controller $FLUX_HELM_VERSION"
crd "https://github.com/fluxcd/source-controller/releases/download/$FLUX_SOURCE_VERSION/source-controller.crds.yaml"
crd "https://github.com/fluxcd/kustomize-controller/releases/download/$FLUX_KUSTOMIZE_VERSION/kustomize-controller.crds.yaml"
crd "https://github.com/fluxcd/helm-controller/releases/download/$FLUX_HELM_VERSION/helm-controller.crds.yaml"
kubectl wait --for condition=Established --timeout 60s "${CRDS[@]/#/crd/}" >/dev/null

say "Creating the sample objects in namespace gitops"
kubectl apply --server-side --force-conflicts --field-manager st8ks-dev -f - < gitops.yaml >/dev/null

# The node has GNU date, so the times work on every host.
ts() { docker exec "$CLUSTER-control-plane" date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ; }
NOW="$(ts now)"
T5M="$(ts '-5 minutes')"
T20M="$(ts '-20 minutes')"
T1H="$(ts '-1 hour')"
T3H="$(ts '-3 hours')"
SHA="0f5c7d2b9e4a1c3f8d6b2a7e5c9f1d4b3a8e6c2f"

# cond TYPE STATUS REASON MESSAGE TIME prints one Flux condition. The
# message must not contain double quotes.
cond() {
  printf '{"type":"%s","status":"%s","reason":"%s","message":"%s","lastTransitionTime":"%s","observedGeneration":1}' "$1" "$2" "$3" "$4" "$5"
}
# artifact PATH REVISION prints the artifact of a Flux source.
artifact() {
  printf '{"digest":"sha256:%s","lastUpdateTime":"%s","path":"%s","revision":"%s","url":"http://source-controller.flux-system.svc.cluster.local./%s"}' \
    "${SHA}${SHA:0:24}" "$T1H" "$1" "$2" "$1"
}
# flux KIND NAME STATUS sets the status of a Flux object through the status
# subresource.
flux() { kubectl -n gitops patch "$1" "$2" --subresource=status --type=merge -p "{\"status\":$3}" >/dev/null; }
# app NAME STATUS sets the status of an Application. Its CRD has no status
# subresource. The patch also removes an operation that st8ks started, but
# not on inventory, whose sync runs on purpose.
app() {
  local op=',"operation":null'
  [ "$1" = inventory ] && op=""
  kubectl -n gitops patch application.argoproj.io "$1" --type=merge -p "{\"status\":$2$op}" >/dev/null
}
# res KIND NAMESPACE NAME SYNC HEALTH [MESSAGE] prints one resource of an
# Application.
res() {
  local h="{\"status\":\"$5\"}"
  [ -n "${6:-}" ] && h="{\"status\":\"$5\",\"message\":\"$6\"}"
  printf '{"group":"apps","version":"v1","kind":"%s","namespace":"%s","name":"%s","status":"%s","health":%s}' "$1" "$2" "$3" "$4" "$h"
}
done_op() {
  printf '{"phase":"%s","message":"%s","startedAt":"%s","finishedAt":"%s","operation":{"sync":{"revision":"%s"}},"syncResult":{"revision":"%s","resources":[%s]}}' \
    "$1" "$2" "$3" "$3" "$SHA" "$SHA" "${4:-}"
}

say "Setting the status of the Argo CD Applications"
app guestbook "{\"health\":{\"status\":\"Healthy\",\"lastTransitionTime\":\"$T3H\"},\"sync\":{\"status\":\"Synced\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",\"sourceType\":\"Directory\",
  \"resources\":[$(res Deployment guestbook guestbook-ui Synced Healthy)],\"operationState\":$(done_op Succeeded 'successfully synced (all tasks run)' "$T3H")}"
app inventory "{\"health\":{\"status\":\"Progressing\"},\"sync\":{\"status\":\"OutOfSync\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",
  \"operationState\":{\"phase\":\"Running\",\"message\":\"waiting for healthy state of apps/Deployment/kustomize-guestbook-ui\",\"startedAt\":\"$T5M\",\"operation\":{\"sync\":{\"revision\":\"HEAD\"}}}}"
app payments "{\"health\":{\"status\":\"Degraded\",\"lastTransitionTime\":\"$T20M\"},\"sync\":{\"status\":\"Synced\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",
  \"resources\":[$(res Deployment payments payments-api Synced Degraded "Deployment payments-api exceeded its progress deadline"),$(res Service payments payments-api Synced Healthy)],
  \"operationState\":$(done_op Succeeded 'successfully synced (all tasks run)' "$T1H")}"
app billing "{\"health\":{\"status\":\"Missing\",\"lastTransitionTime\":\"$T20M\"},\"sync\":{\"status\":\"OutOfSync\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",
  \"resources\":[$(res Deployment billing billing-worker OutOfSync Missing)]}"
app catalog "{\"health\":{\"status\":\"Healthy\",\"lastTransitionTime\":\"$T3H\"},\"sync\":{\"status\":\"OutOfSync\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",
  \"resources\":[$(res Deployment catalog catalog-api OutOfSync Healthy)],\"operationState\":$(done_op Succeeded 'successfully synced (all tasks run)' "$T3H")}"
app orders "{\"health\":{\"status\":\"Healthy\",\"lastTransitionTime\":\"$T3H\"},\"sync\":{\"status\":\"OutOfSync\",\"revision\":\"$SHA\"},\"reconciledAt\":\"$NOW\",
  \"operationState\":$(done_op Failed 'one or more objects failed to apply, reason: Deployment.apps orders-api is invalid' "$T20M" \
  '{"group":"apps","version":"v1","kind":"Deployment","namespace":"orders","name":"orders-api","status":"SyncFailed","message":"Deployment.apps orders-api is invalid: spec.template.spec.containers[0].image: Required value"}')}"
app search "{\"health\":{\"status\":\"Healthy\"},\"sync\":{\"status\":\"Unknown\"},\"reconciledAt\":\"$NOW\",
  \"conditions\":[{\"type\":\"ComparisonError\",\"message\":\"Failed to load target state: failed to generate manifest for source 1 of 1: rpc error: code = Unknown desc = unable to resolve 'feature/does-not-exist' to a commit SHA\",\"lastTransitionTime\":\"$T20M\"}]}"

say "Setting the status of the Flux objects"
OK_GIT="$(cond Ready True Succeeded "stored artifact for revision 'master@sha1:$SHA'" "$T1H")"
flux gitrepository.source.toolkit.fluxcd.io podinfo "{\"observedGeneration\":1,\"conditions\":[$OK_GIT],\"artifact\":$(artifact gitrepository/gitops/podinfo/$SHA.tar.gz "master@sha1:$SHA")}"
flux gitrepository.source.toolkit.fluxcd.io platform "{\"observedGeneration\":1,\"conditions\":[$(cond Ready False GitOperationFailed "failed to checkout and determine revision: unable to clone 'https://github.com/example/platform-private': authentication required" "$T20M"),$(cond FetchFailed True GitOperationFailed "unable to clone: authentication required" "$T20M")]}"
flux ocirepository.source.toolkit.fluxcd.io podinfo-oci "{\"observedGeneration\":1,\"conditions\":[$(cond Ready True Succeeded "stored artifact for digest 'latest@sha256:${SHA}${SHA:0:24}'" "$T1H")],\"artifact\":$(artifact ocirepository/gitops/podinfo-oci/$SHA.tar.gz "latest@sha256:${SHA}${SHA:0:24}")}"
flux ocirepository.source.toolkit.fluxcd.io policies "{\"observedGeneration\":1,\"conditions\":[$(cond Ready False InvalidProviderConfiguration "failed to configure the aws provider: no region in the URL" "$T1H"),$(cond Stalled True InvalidProviderConfiguration "failed to configure the aws provider: no region in the URL" "$T1H")]}"
flux helmrepository.source.toolkit.fluxcd.io podinfo "{\"observedGeneration\":1,\"conditions\":[$(cond Ready True Succeeded "stored artifact: revision 'sha256:$SHA'" "$T1H")],\"artifact\":$(artifact helmrepository/gitops/podinfo/index.yaml "sha256:${SHA}${SHA:0:24}")}"
flux helmrepository.source.toolkit.fluxcd.io bitnami "{\"observedGeneration\":1,\"conditions\":[$(cond Ready Unknown Progressing "building artifact: fetching index from https://charts.example.com/bitnami/index.yaml" "$T20M"),$(cond Reconciling True Progressing "building artifact" "$T20M")]}"
flux helmchart.source.toolkit.fluxcd.io gitops-podinfo "{\"observedGeneration\":1,\"conditions\":[$(cond Ready True ChartPullSucceeded "pulled 'podinfo' chart with version '6.9.2'" "$T1H")],\"artifact\":$(artifact helmchart/gitops/gitops-podinfo/podinfo-6.9.2.tgz 6.9.2)}"
flux helmchart.source.toolkit.fluxcd.io gitops-redis "{\"observedGeneration\":1,\"conditions\":[$(cond Ready False ChartPullError "invalid chart reference: failed to get chart version for remote reference: no 'redis' chart with version matching '99.0.0' found" "$T20M")]}"
flux bucket.source.toolkit.fluxcd.io assets "{\"observedGeneration\":1,\"conditions\":[$(cond Ready True Succeeded "stored artifact for revision 'sha256:$SHA'" "$T1H")],\"artifact\":$(artifact bucket/gitops/assets/$SHA.tar.gz "sha256:${SHA}${SHA:0:24}")}"
flux bucket.source.toolkit.fluxcd.io backups "{\"observedGeneration\":1,\"conditions\":[$(cond Ready False BucketOperationFailed "bucket 'backups' does not exist" "$T3H")]}"
flux kustomization.kustomize.toolkit.fluxcd.io apps "{\"observedGeneration\":1,\"lastAppliedRevision\":\"master@sha1:$SHA\",\"lastAttemptedRevision\":\"master@sha1:$SHA\",\"conditions\":[$(cond Ready True ReconciliationSucceeded "Applied revision: master@sha1:$SHA" "$T1H")]}"
flux kustomization.kustomize.toolkit.fluxcd.io monitoring "{\"observedGeneration\":1,\"lastAppliedRevision\":\"master@sha1:$SHA\",\"conditions\":[$(cond Ready True ReconciliationSucceeded "Applied revision: master@sha1:$SHA" "$T3H")]}"
flux kustomization.kustomize.toolkit.fluxcd.io platform "{\"observedGeneration\":1,\"conditions\":[$(cond Ready False ArtifactFailed "Source artifact not found, retrying in 30s" "$T20M")]}"
flux kustomization.kustomize.toolkit.fluxcd.io infra "{\"observedGeneration\":1,\"lastAppliedRevision\":\"master@sha1:$SHA\",\"lastAttemptedRevision\":\"master@sha1:$SHA\",\"conditions\":[$(cond Ready False BuildFailed "kustomize build failed: accumulating resources: accumulation err='accumulating resources from 'redis.yaml': open /tmp/kustomization-1/deploy/overlays/broken/redis.yaml: no such file or directory'" "$T20M")]}"
flux helmrelease.helm.toolkit.fluxcd.io podinfo "{\"observedGeneration\":1,\"helmChart\":\"gitops/gitops-podinfo\",\"lastAttemptedRevision\":\"6.9.2\",\"conditions\":[$(cond Ready True UpgradeSucceeded "Helm upgrade succeeded for release gitops/podinfo.v2 with chart podinfo@6.9.2" "$T1H")]}"
flux helmrelease.helm.toolkit.fluxcd.io redis "{\"observedGeneration\":1,\"helmChart\":\"gitops/gitops-redis\",\"conditions\":[$(cond Ready False ArtifactFailed "HelmChart 'gitops/gitops-redis' is not ready: invalid chart reference" "$T20M")]}"
flux helmrelease.helm.toolkit.fluxcd.io nginx "{\"observedGeneration\":1,\"lastAttemptedRevision\":\"6.9.2\",\"upgradeFailures\":4,\"conditions\":[$(cond Ready False UpgradeFailed "Helm upgrade failed for release gitops/nginx with chart podinfo@6.9.2: context deadline exceeded" "$T1H"),$(cond Stalled True RetriesExceeded "Failed to upgrade after 4 attempt(s)" "$T1H")]}"

say "Done. Namespace gitops has 7 Applications and 17 Flux objects. 14 of them fail."
say "OutOfSync on Application catalog shows 2 minutes after st8ks first sees it, when the objects are new."
