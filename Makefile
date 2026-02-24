# Gardener Simulator Makefile

# Image configuration
IMAGE_REGISTRY := pure-artifactory.dev.purestorage.com
IMAGE_REPO := px-docker-dev-virtual/users/yassharma/gardener-sim
IMAGE_TAG ?= latest
IMAGE_NAME := $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)

# Build configuration
BINARY_NAME := gardener-simulator
BIN_DIR := bin
GO := go

.PHONY: all build clean docker-build docker-push docker deploy undeploy helm-package helm-install helm-uninstall help

all: build

## Build the Go binary locally
build:
	@echo "Building $(BIN_DIR)/$(BINARY_NAME)..."
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags="-w -s" -o $(BIN_DIR)/$(BINARY_NAME) .

## Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BIN_DIR)

## Build Docker image
docker-build:
	@echo "Building Docker image $(IMAGE_NAME)..."
	docker build -t $(IMAGE_NAME) .

## Push Docker image to registry
docker-push:
	@echo "Pushing Docker image $(IMAGE_NAME)..."
	docker push $(IMAGE_NAME)

## Build and push Docker image
docker: docker-build docker-push

## Deploy to Kubernetes
deploy:
	@echo "Deploying to Kubernetes..."
	kubectl apply -f deploy/kubernetes/deployment.yaml

## Remove from Kubernetes
undeploy:
	@echo "Removing from Kubernetes..."
	kubectl delete -f deploy/kubernetes/deployment.yaml --ignore-not-found

## Package Helm chart
helm-package:
	@echo "Packaging Helm chart..."
	cd helm && helm package gardener-simulator

## Install via Helm with release name 'gs'
helm-install:
	@echo "Installing Helm chart..."
	helm install gs helm/gardener-simulator

## Uninstall Helm release
helm-uninstall:
	@echo "Uninstalling Helm release..."
	helm uninstall gs

## Run locally
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BIN_DIR)/$(BINARY_NAME) --port 8443 --projects 2 --shoots 10

## Run tests
test:
	@echo "Running tests..."
	$(GO) test -v ./...

## Show help
help:
	@echo "Gardener Simulator Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make build          - Build the Go binary locally"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make docker-build   - Build Docker image"
	@echo "  make docker-push    - Push Docker image to registry"
	@echo "  make docker         - Build and push Docker image"
	@echo "  make deploy         - Deploy to Kubernetes (kubectl)"
	@echo "  make undeploy       - Remove from Kubernetes (kubectl)"
	@echo "  make helm-package   - Package Helm chart"
	@echo "  make helm-install   - Install via Helm (release: gs)"
	@echo "  make helm-uninstall - Uninstall Helm release"
	@echo "  make run            - Run locally"
	@echo "  make test           - Run tests"
	@echo ""
	@echo "Variables:"
	@echo "  IMAGE_TAG           - Docker image tag (default: latest)"
	@echo ""
	@echo "Examples:"
	@echo "  make docker IMAGE_TAG=v1.0.0"
	@echo "  make helm-install"

