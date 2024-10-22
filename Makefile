# Default bundle image tag
BUNDLE_IMG ?= controller-bundle:$(VERSION)
# Options for 'bundle-build'
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Image URL to use all building/pushing image targets
IMG ?= buraksekili-gwapi:latest

TAG = $(lastword $(subst :, ,$(IMG)))

#The name of the kind cluster used for development
CLUSTER_NAME ?= kind

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

all: manager

# Run tests
#test: generate fmt vet manifests
#	go test ./... -coverprofile cover.out
# Run tests

# skip bdd when doing unit testing
UNIT_TEST=$(shell go list ./... | grep -v bdd)

test: generate fmt vet manifests
	go test ${UNIT_TEST}  -coverprofile test_coverage.out --timeout 30m

manager: generate fmt vet	## build manager binary
	go build -o bin/manager cmd/main.go

run: generate fmt vet manifests ## Run against the configured Kubernetes cluster in ~/.kube/config
	ENABLE_WEBHOOKS=${ENABLE_WEBHOOKS} go run cmd/main.go

install: manifests kustomize	## Install CRDs into a cluster
	$(KUSTOMIZE) build config/crd | kubectl apply -f -


uninstall: manifests kustomize	## Uninstall CRDs from a cluster
	$(KUSTOMIZE) build config/crd | kubectl delete -f -


deploy: manifests kustomize ## Deploy controller in the configured Kubernetes cluster in ~/.kube/config
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

helm: kustomize ## Update helm charts
	$(KUSTOMIZE) version
	$(KUSTOMIZE) build config/crd > ./helm/crds/crds.yaml
	$(KUSTOMIZE) build config/helm |go run hack/helm/pre_helm.go > ./helm/templates/all.yaml

manifests: controller-gen ## Generate manifests
	$(CONTROLLER_GEN) --version
	$(CONTROLLER_GEN) crd rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases


fmt: ## Run go fmt against code
	go fmt ./...
	gofmt -s -w .


vet: ## Run go vet against code
	go vet ./...

golangci-lint: ## Run golangci-lint linter
	golangci-lint run

linters: fmt vet golangci-lint ## Run all linters once

generate: controller-gen ## Generate code
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."


docker-build: test ## Build the docker image
	docker build . -t ${IMG}

docker-build-notest: ## Build the docker image
	docker build . -t ${IMG}


docker-push: ## Push the docker image
	docker push ${IMG}


##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

## Tool Versions
KUSTOMIZE_VERSION ?= v5.3.0
CONTROLLER_TOOLS_VERSION ?= v0.14.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: bundle
bundle: manifests kustomize	## Generate bundle manifests and metadata, then validate generated files.
	operator-sdk generate kustomize manifests -q
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/manifests | operator-sdk generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	operator-sdk bundle validate ./bundle

.PHONY: test-all
test-all: test ## Run tests

.PHONY: install-venom
install-venom:
ifeq (, $(venom version))
	@echo "Installing venom"
	sudo curl https://github.com/ovh/venom/releases/download/v1.0.1/venom.linux-amd64 -L -o /usr/local/bin/venom && sudo chmod +x /usr/local/bin/venom
else
	@echo "Venom is already installed"
endif
	
.PHONY: run-venom-tests
run-venom-tests: install-venom ## Run Venom integration tests
	cd venom-tests && IS_TTY=true venom run

help:
	@fgrep -h "##" Makefile | fgrep -v fgrep |sed -e 's/\\$$//' |sed -e 's/:/-:/'| sed -e 's/:.*##//'
