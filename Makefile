VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Linux builds link WebKitGTK 4.1. macOS and Windows need no tag.
TAGS    ?= $(if $(filter Linux,$(shell uname -s)),webkit2_41,)
LDFLAGS := -X main.version=$(VERSION)

STATICCHECK_VERSION := v0.8.1
GOVULNCHECK_VERSION := v1.8.0

DEV_KUBECONFIG ?= $(or $(ST8KS_DEV_KUBECONFIG),$(HOME)/.kube/st8ks-dev.yaml)

.PHONY: dev build frontend typecheck test race vet fmt fmt-check staticcheck security ci \
	integration cluster-up cluster-down cluster-reset cluster-load clean help

## dev: run the app with live reload
dev:
	wails dev -tags "$(TAGS)"

## build: build a production binary into build/bin
build:
	wails build -clean -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)"

## frontend: install the frontend packages and build frontend/dist
frontend:
	cd frontend && npm ci && npm run build

## typecheck: type-check the frontend
typecheck:
	cd frontend && npm run typecheck

## test: run the unit tests
test:
	go test -tags "$(TAGS)" ./...

## race: run the unit tests with the race detector
race:
	go test -count=1 -race -tags "$(TAGS)" ./...

## vet: run go vet, including the integration test
vet:
	go vet -tags "$(TAGS)" ./...
	go vet -tags "integration $(TAGS)" ./...

## fmt: format the Go code
fmt:
	gofmt -w main.go app.go app_ide.go internal

## fmt-check: fail when Go code is not formatted
fmt-check:
	@test -z "$$(gofmt -l main.go app.go app_ide.go internal)" || { gofmt -l main.go app.go app_ide.go internal; exit 1; }

## staticcheck: run staticcheck
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -tags "$(TAGS)" ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -tags "integration $(TAGS)" ./...

## security: check the dependencies for known vulnerabilities
security:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -tags "$(TAGS)" ./...

## ci: everything that CI runs
ci: frontend typecheck fmt-check vet staticcheck security race

## cluster-up: create the local kind cluster with failing workloads
cluster-up:
	hack/dev-cluster/up.sh

## cluster-down: delete the local kind cluster
cluster-down:
	hack/dev-cluster/down.sh

## cluster-reset: delete the local kind cluster and create it again
cluster-reset:
	hack/dev-cluster/up.sh --recreate

## cluster-load: add 6000 ConfigMaps to the local cluster for a load test
cluster-load:
	hack/dev-cluster/load.sh

## integration: run the integration test against the local kind cluster
integration:
	ST8KS_IT_KUBECONFIG=$(DEV_KUBECONFIG) \
		go test -count=1 -race -tags "integration $(TAGS)" -run TestIntegration -v ./internal/kube/

## clean: remove build output
clean:
	rm -rf build/bin frontend/dist/assets frontend/dist/index.html

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
