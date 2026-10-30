# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Kubernetes engineers, SREs and platform engineers on call. They must find a broken workload, understand why it fails, and fix it without a mistake in production. They already know kubectl, Helm and kubeconfig files. (Inferred from the README and confirmed by the user on 2026-10-30.)

## Product Purpose

st8ks is a desktop Kubernetes client for Linux, macOS and Windows. It watches clusters live, shows every resource, finds workloads that fail, collects the evidence, and proposes a fix that the user reviews as a YAML diff. Every change goes through a confirmation with an optional server dry run. Success means the engineer goes from "something is broken" to "the fix is applied and verified" in one tool.

## Positioning

st8ks goes from broken to fixed: it detects the failure, shows the evidence, proposes the change as a diff, and the API server dry-runs it before apply. It is free and open source under the MIT license, runs locally with the user's own kubeconfig, and needs no account, login or subscription. Lens, the main competitor, is commercial and moves toward governed AI agents for enterprises.

## Operating Context

- Reads the same kubeconfig files as kubectl: `KUBECONFIG`, `~/.kube/config` and other files in `~/.kube`. Flags: `--kubeconfig`, `--context`.
- Command palette on `Ctrl K` or `⌘K`.
- The assistant is read-only and uses the user's own Anthropic API key.
- The IDE mode edits Kubernetes manifests in a Git repository, checks them against the cluster schema and live pods, applies with server-side apply, and commits and pushes.
- Releases come from GitHub: `st8ks-darwin-universal.dmg`, `st8ks-windows-amd64-setup.exe`, `st8ks-windows-arm64-setup.exe`, `st8ks-linux-amd64.tar.gz`, `st8ks-linux-arm64.tar.gz`, plus `checksums.txt`.

## Capabilities and Constraints

- Live lists for all built-in kinds and every CRD, with filters such as `ns:prod status:crash app=web`, saved views and bulk actions.
- Issue detection: crash loops, OOM kills, missing images, unschedulable pods, stuck rollouts, failed jobs, claims and certificates.
- YAML diff, server dry run, protected namespaces that need the name typed first.
- Logs, shells, debug containers, port-forwards.
- RBAC explorer, Helm releases (history, rollback, upgrade, uninstall), topology graph.
- Built with Wails v2: a Go backend with client-go and the Helm SDK, and a React and TypeScript frontend.
- Linux needs WebKitGTK 4.1 and GTK 3.

## Brand Commitments

- Name: `st8ks`, always lower case.
- App icon: `build/appicon.svg` and `build/appicon.png`, a red stone with an "8" on a dark rounded square.
- All prose follows ASD-STE100 Simplified Technical English: short sentences, active voice, no hype, no superlatives.
- No AI attribution and no personal names in any file. The copyright holder is "st8ks contributors".

## Evidence on Hand

- Screenshots in `docs/images/`: `overview.png`, `overview-light.png`, `fix-diff.png`, `pods.png`, `pod-detail.png`, `logs.png`, `rbac.png`, `helm.png`, `topology.png`, `clusters.png`, `palette.png`, `launch-poster.jpg`.
- Measured performance in `docs/architecture.md`: 6,019 ConfigMaps in one list, 226 ms to open the list, 1 to 2 frames from filter keystroke to paint, 37 ms sort, 60 fps scroll with a 95th percentile frame of 17.1 ms, 330 ms to connect and load all pods of a three-node cluster.
- No testimonials, customer logos, user counts, certifications or press exist. Do not invent them.

## Product Principles

1. Prove the fix before it reaches the cluster: diff, dry run, confirmation.
2. Speed is a feature. Stay at 60 frames per second with thousands of changing objects.
3. Local first. The user's kubeconfig, credentials and API key never leave the machine except to their own cluster and to the Anthropic API when they ask.
4. Show the evidence, not a guess. Every issue carries the events, logs and YAML behind it.
5. Plain words. Developers read the text once and act on it.
