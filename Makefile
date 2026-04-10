##@ General

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

VERSION = $(shell cat ./VERSION)

.PHONY: version
version: ## Show current version.
	@echo VERSION=$(VERSION)

.PHONY: version-match
version-match: version ## Check if versions are consistent.
	@if [ -z "$$(echo $(VERSION) | grep -Eo "^[[:digit:]]+\.[[:digit:]]+\.[[:digit:]](-[[:alpha:]][[:alnum:]]*(\.[[:digit:]]+)?)?$$")" ]; then \
		echo "VERSION is not semver: $(VERSION)" ;\
		exit 1 ;\
	fi
	$(foreach chart, $(wildcard ./helm/**/Chart.yaml), $(SED) -i -E 's/version:[[:space:]]+.+$$/version: $(VERSION)/g' ${chart} ;)

##@ Build

.PHONY: all
all: build ## Run all build targets

REGISTRY ?= slinky.slurm.net
BUILDER ?= project-v3-builder

.PHONY: build
build: build-images build-chart ## Build OCI packages.

.PHONY: build-images
build-images: ## Build container images.
	- $(CONTAINER_TOOL) buildx create --name $(BUILDER)
	REGISTRY=$(REGISTRY) VERSION=$(VERSION) $(CONTAINER_TOOL) buildx bake --builder=$(BUILDER)

.PHONY: build-chart
build-chart: ## Build charts.
	$(foreach chart, $(wildcard ./helm/**/Chart.yaml), helm package --dependency-update helm/$(shell basename "$(shell dirname "${chart}")") ;)

.PHONY: push
push: push-images push-charts ## Push OCI packages.

.PHONY: push-images
push-images: build-images ## Push container images.
	REGISTRY=$(REGISTRY) VERSION=$(VERSION) $(CONTAINER_TOOL) buildx bake --builder=$(BUILDER) --push

.PHONY: push-charts
push-charts: build-chart ## Push OCI packages.
	$(foreach chart, $(wildcard ./*.tgz), helm push ${chart} oci://$(REGISTRY)/charts ;)

##@ Demo

# Use a fixed cluster name for the demo; do not run on an existing cluster we did not create.
KIND_CLUSTER_NAME ?= slurm-bridge-demo

.PHONY: demo-cluster-create
demo-cluster-create: ## Spin up a kind cluster (slurm-bridge-demo) and install slurm-bridge using hack/kind.sh.
	./hack/kind.sh --bridge $(KIND_CLUSTER_NAME)

.PHONY: demo-cluster-delete
demo-cluster-delete: ## Delete the kind cluster.
	./hack/kind.sh --delete $(KIND_CLUSTER_NAME)

.PHONY: install-dra
install-dra: ## Add all DRA configs from hack/kind.sh (dra-driver-cpu and dra-example-driver).
	./hack/kind.sh --dra-driver-cpu --dra-example-driver --bridge $(KIND_CLUSTER_NAME)

.PHONY: setup-sysctl
setup-sysctl: ## Set kernel/sysctl values recommended for kind/demo (requires sudo).
	./hack/sysctl.sh

# Exclude LWS (long-running) and DRA examples from main demo; DRA has its own demo-dra target.
HACK_EXAMPLES ?= $(sort $(filter-out hack/examples/lws/lws.yaml $(wildcard hack/examples/dra/*.yaml),$(wildcard hack/examples/*/*.yaml)))
HACK_EXAMPLES_DRA ?= $(sort $(wildcard hack/examples/dra/gpu-example/*.yaml))

.PHONY: install-examples
install-examples: ## run examples only-no cluster setup
	for f in $(HACK_EXAMPLES); do $(KUBECTL) delete -f "$$f" || true; done; \
    for f in $(HACK_EXAMPLES); do $(KUBECTL) apply -f "$$f"; done;

.PHONY: demo-examples
demo-examples: demo-cluster-create install-examples ## Run hack/examples YAMLs (except lws and dra) and watch (Ctrl+C to stop watch).
	if [ "$$(uname -s)" != "Darwin" ]; then ./hack/demo_watch.sh || true; fi

.PHONY: install-examples-dra
install-examples-dra: ## install dra examples only-no cluster setup
	for f in $(HACK_EXAMPLES_DRA); do $(KUBECTL) delete -f "$$f" || true; done; \
	for f in $(HACK_EXAMPLES_DRA); do $(KUBECTL) apply -f "$$f"; done;

.PHONY: demo-examples-dra
demo-examples-dra: install-dra install-examples-dra ## Install DRA drivers and run DRA example pods and watch (Ctrl+C to stop).
	if [ "$$(uname -s)" != "Darwin" ]; then ./hack/demo_watch.sh || true; fi

##@ Deployment

# Get the OS to set platform specific commands
UNAME_S ?= $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
	CP_FLAGS = -v -n
	SED = gsed
else
	CP_FLAGS = -v --update=none
	SED = sed
endif

.PHONY: values-dev
values-dev: ## Safely initialize values-dev.yaml files for Helm charts.
	find "helm/" -type f -name "values.yaml" | $(SED) 'p;s/\.yaml/-dev\.yaml/' | xargs -n2 cp $(CP_FLAGS)

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOVULNCHECK ?= $(LOCALBIN)/govulncheck-$(GOVULNCHECK_VERSION)
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
HELM_DOCS ?= $(LOCALBIN)/helm-docs-$(HELM_DOCS_VERSION)
PANDOC ?= $(LOCALBIN)/pandoc-$(PANDOC_VERSION)
YQ ?= $(LOCALBIN)/yq-$(YQ_VERSION)

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.20.1
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
GOVULNCHECK_VERSION ?= latest
GOLANGCI_LINT_VERSION ?= v2.11.1
HELM_DOCS_VERSION ?= v1.14.2
PANDOC_VERSION ?= 3.9
YQ_VERSION ?= v4.45.1

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: govulncheck-bin
govulncheck-bin: $(GOVULNCHECK) ## Download govulncheck locally if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

.PHONY: golangci-lint-bin
golangci-lint-bin: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(LOCALBIN) $(GOLANGCI_LINT_VERSION)
	mv $(LOCALBIN)/golangci-lint $(GOLANGCI_LINT)

.PHONY: helm-docs-bin
helm-docs-bin: $(HELM_DOCS) ## Download helm-docs locally if necessary.
$(HELM_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(HELM_DOCS),github.com/norwoodj/helm-docs/cmd/helm-docs,$(HELM_DOCS_VERSION))

.PHONY: pandoc-bin
pandoc-bin: $(PANDOC) ## Download pandoc locally if necessary.
$(PANDOC): $(LOCALBIN)
	@if ! [ -f "$(PANDOC)" ]; then \
		if [ "$(shell go env GOOS)" != "darwin" ]; then \
			curl -sSLo $(PANDOC).tar.gz https://github.com/jgm/pandoc/releases/download/$(PANDOC_VERSION)/pandoc-$(PANDOC_VERSION)-$(shell go env GOOS)-$(shell go env GOARCH).tar.gz ;\
			tar xv --directory=$(LOCALBIN) --file=$(PANDOC).tar.gz pandoc-$(PANDOC_VERSION)/bin/pandoc --strip-components=2 ;\
		else \
			curl -sSLo $(PANDOC).zip https://github.com/jgm/pandoc/releases/download/$(PANDOC_VERSION)/pandoc-$(PANDOC_VERSION)-$(shell go env GOARCH)-macOS.zip ;\
			unzip -oqqjd $(LOCALBIN) $(PANDOC).zip ;\
		fi ;\
		mv $(LOCALBIN)/pandoc $(PANDOC) ;\
		rm -f $(PANDOC).tar.gz $(PANDOC).zip ;\
	fi

.PHONY: yq-bin
yq-bin: $(YQ) ## Download yq (mikefarah/v4) locally if necessary.
$(YQ): $(LOCALBIN)
	$(call go-install-tool,$(YQ),github.com/mikefarah/yq/v4,$(YQ_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv "$$(echo "$(1)" | $(SED) "s/-$(3)$$//")" $(1) ;\
}
endef

##@ Development

.PHONY: install-dev
install-dev: ## Install binaries for development environment.
	go install github.com/go-delve/delve/cmd/dlv@latest
	go install sigs.k8s.io/kind@latest
	go install sigs.k8s.io/cloud-provider-kind@latest

.PHONY: helm-validate
helm-validate: helm-dependency-update helm-lint ## Validate Helm charts.

.PHONY: helm-docs
helm-docs: helm-docs-bin ## Run helm-docs.
	$(HELM_DOCS) --chart-search-root=helm

.PHONY: helm-lint
helm-lint: ## Lint Helm charts.
	find "helm/" -depth -mindepth 1 -maxdepth 1 -type d -print0 | xargs -0r -n1 helm lint --strict

.PHONY: helm-dependency-update
helm-dependency-update: ## Update Helm chart dependencies.
	find "helm/" -depth -mindepth 1 -maxdepth 1 -type d -print0 | xargs -0r -n1 helm dependency update

BRIDGE_CHART_DIR ?= helm/slurm-bridge
BRIDGE_HELM_FILES ?= $(BRIDGE_CHART_DIR)/files

.PHONY: manifests
manifests: controller-gen yq-bin ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=scheduler-role paths=./cmd/scheduler/... paths=./internal/scheduler/... output:rbac:dir=config/rbac/scheduler
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths=./cmd/controllers/... paths=./internal/controller/... output:rbac:dir=config/rbac/manager
	$(CONTROLLER_GEN) rbac:roleName=webhook-role webhook paths=./cmd/admission/... paths=./internal/admission/... output:rbac:dir=config/rbac/webhook output:webhook:dir=./config/webhook

	mkdir -p $(BRIDGE_HELM_FILES)
	$(YQ) '{"rules": .rules}' config/rbac/scheduler/role.yaml > $(BRIDGE_HELM_FILES)/scheduler_rbac_rules.yaml
	$(YQ) '{"rules": .rules}' config/rbac/manager/role.yaml > $(BRIDGE_HELM_FILES)/controllers_rbac_rules.yaml
	$(YQ) '{"rules": .rules}' config/rbac/webhook/role.yaml > $(BRIDGE_HELM_FILES)/admission_rbac_rules.yaml

.PHONY: generate-docs
generate-docs: pandoc-bin
	$(PANDOC) --quiet README.md -o docs/index.rst
	cat ./docs/_static/toc.rst >> docs/index.rst
	printf '\n' >> docs/index.rst
	find docs -type f -name "*.md" -exec basename {} \; | awk '{print "    "$$1}' | env LC_ALL=C sort >> docs/index.rst
	$(SED) -i -E '/<.\/docs\/[A-Za-z]*.md/s/.\/docs\///g' docs/index.rst
	$(SED) -i -E '/.\/docs\/.*.svg/s/.\/docs\///g' docs/index.rst
	$(SED) -i -E '/<[A-Za-z]*.md>`/s/.md>/.html>/g' docs/index.rst

DOCS_IMAGE ?= $(REGISTRY)/sphinx

.PHONY: build-docs
build-docs: ## Build the container image used to develop the docs
	$(CONTAINER_TOOL) build -t $(DOCS_IMAGE) ./docs

.PHONY: run-docs
run-docs: build-docs ## Run the container image for docs development
	$(CONTAINER_TOOL) run --rm --network host -v ./docs:/docs:z $(DOCS_IMAGE) sphinx-autobuild --port 8000 /docs /build/html

.PHONY: clean
clean: ## Clean files.
	@ chmod -R -f u+w bin/ || true # make test installs files without write permissions.
	rm -rf bin/
	rm -rf vendor/
	rm -f cover.out cover.html cover.out.tmp
	rm -f *.tgz
	- $(CONTAINER_TOOL) buildx rm $(BUILDER)

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: tidy
tidy: ## Run go mod tidy against code
	go mod tidy

.PHONY: get-u
get-u: ## Run `go get -u`
	go get -u ./...
	$(MAKE) tidy

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: govulncheck
govulncheck: govulncheck-bin ## Run govulncheck
	$(GOVULNCHECK) ./...

# https://github.com/golangci/golangci-lint/blob/main/.pre-commit-hooks.yaml
.PHONY: golangci-lint
golangci-lint: golangci-lint-bin ## Run golangci-lint.
	$(GOLANGCI_LINT) run --fix

# https://github.com/golangci/golangci-lint/blob/main/.pre-commit-hooks.yaml
.PHONY: golangci-lint-fmt
golangci-lint-fmt: golangci-lint-bin ## Run golangci-lint fmt.
	$(GOLANGCI_LINT) fmt

## Location to locally build documentation
LOCALBUILD ?= $(shell pwd)/build-docs
$(LOCALBUILD):
	mkdir -p $(LOCALBUILD)

.PHONY: sphinx-build
sphinx-build: sphinx-install $(LOCALBIN) $(LOCALBUILD)
	source $(LOCALBIN)/sphinx-venv/bin/activate ;\
	sphinx-multiversion docs $(LOCALBUILD) ;\
	deactivate ;\

.PHONY: sphinx-install
sphinx-install: sphinx-venv
	source $(LOCALBIN)/sphinx-venv/bin/activate ;\
	pip install -r docs/requirements.txt ;\
	deactivate ;\

.PHONY: sphinx-venv
sphinx-venv: $(LOCALBIN)
	python3 -m venv $(LOCALBIN)/sphinx-venv

CODECOV_PERCENT ?= 70

.PHONY: test
test: fmt vet envtest ## Run tests.
	rm -f cover.out cover.html
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v /e2e) -v -coverprofile cover.out.tmp
	cat cover.out.tmp | grep -v "_generated." > cover.out
	go tool cover -func cover.out
	go tool cover -html cover.out -o cover.html
	@percentage=$$(go tool cover -func=cover.out | grep ^total | awk '{print $$3}' | tr -d '%'); \
		if (( $$(echo "$$percentage < $(CODECOV_PERCENT)" | bc -l) )); then \
			echo "----------"; \
			echo "Total test coverage ($${percentage}%) is less than the coverage threshold ($(CODECOV_PERCENT)%)."; \
			exit 1; \
		fi
