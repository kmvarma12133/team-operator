SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

IMG ?= ghcr.io/kmvarma12133/team-operator:dev
ENVTEST_K8S_VERSION ?= 1.37.x
CHART := charts/team-operator

CONTROLLER_GEN := go tool controller-gen
SETUP_ENVTEST := go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.25

.PHONY: help
help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate deepcopy code, the CRD and the RBAC role, and copy the CRD into the chart
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/v1alpha1
	$(CONTROLLER_GEN) crd rbac:roleName=team-operator paths=./api/v1alpha1 paths=./internal/controller \
		output:crd:dir=config/crd output:rbac:dir=config/rbac
	cp config/crd/*.yaml $(CHART)/crds/
	cp config/rbac/role.yaml $(CHART)/files/role.yaml

.PHONY: verify-generated
verify-generated: generate ## Fail if generated files are out of date
	git diff --exit-code -- api config $(CHART)/crds $(CHART)/files ||{ echo "run 'make generate' and commit the result"; exit 1; }

.PHONY: envtest
envtest: ## Download kube-apiserver and etcd for the integration tests
	$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir ./bin -p path

.PHONY: test
test: envtest ## Run unit and envtest integration tests with coverage
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir ./bin -p path)" \
		go test -race -coverprofile=cover.out ./...
	go tool cover -func=cover.out | tail -1

# controller-runtime v0.25's envtest does not compile on Windows, so Windows
# hosts run the tests in a Linux container.
.PHONY: test-docker
test-docker: ## Run the tests in a Linux container (for Windows hosts)
	docker run --rm -v "$(CURDIR):/src" -w /src -v team-operator-gomod:/go/pkg/mod \
		golang:1.27.3-bookworm bash -c 'git config --global --add safe.directory /src && make test'

.PHONY: lint
lint: ## go vet and gofmt
	go vet ./...
	@test -z "$$(gofmt -l api cmd internal)" || { gofmt -l api cmd internal; echo "run gofmt -w"; exit 1; }

.PHONY: build
build: ## Build the manager binary
	go build -o bin/manager ./cmd

.PHONY: run
run: ## Run the operator against the current kubeconfig
	go run ./cmd --metrics-secure=false --metrics-bind-address=:8080

.PHONY: docker-build
docker-build: ## Build the container image
	docker build -t $(IMG) .

.PHONY: helm-lint
helm-lint: ## Lint the chart and validate its output against the Kubernetes schemas
	helm lint $(CHART)
	helm template team-operator $(CHART) --namespace team-operator-system | \
		kubeconform -strict -summary -skip CustomResourceDefinition

.PHONY: e2e
e2e: ## End to end on the current kind cluster: install the chart and check a Team
	bash hack/e2e.sh
