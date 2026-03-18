.PHONY: run build test image helm-lint helm-template helm-package

IMAGE ?= smtp-cloud-relay:dev
IMAGE_BUILDER ?= auto
CHART_DIR ?= deploy/helm/smtp-cloud-relay
CHART_OUTPUT_DIR ?= dist/charts
HELM_TEMPLATE_RELEASE ?= smtp-cloud-relay
HELM_API_VERSION ?= cert-manager.io/v1
IMAGE_REPOSITORY ?=
IMAGE_TAG ?=
BUILDAH_RUNTIME_DIR ?= /tmp/buildah-run-$(shell id -u)
BUILDAH_TMPDIR ?= /tmp/buildah-tmp-$(shell id -u)
BUILDAH_STORAGE_DRIVER ?= vfs
BUILDAH_ISOLATION ?= chroot
CERT_MANAGER_ISSUER_NAME ?=
CERT_MANAGER_DNS_NAME ?=
CHART_VERSION ?=
CHART_APP_VERSION ?=

HELM_TEMPLATE_ARGS := --api-versions $(HELM_API_VERSION)

ifneq ($(strip $(IMAGE_REPOSITORY)),)
HELM_TEMPLATE_ARGS += --set-string image.repository=$(IMAGE_REPOSITORY)
endif

ifneq ($(strip $(IMAGE_TAG)),)
HELM_TEMPLATE_ARGS += --set-string image.tag=$(IMAGE_TAG)
endif

ifneq ($(strip $(CERT_MANAGER_ISSUER_NAME)),)
HELM_TEMPLATE_ARGS += --set-string certManager.issuerRef.name=$(CERT_MANAGER_ISSUER_NAME)
endif

ifneq ($(strip $(CERT_MANAGER_DNS_NAME)),)
HELM_TEMPLATE_ARGS += --set-string certManager.dnsNames[0]=$(CERT_MANAGER_DNS_NAME)
endif

HELM_PACKAGE_ARGS := --destination $(CHART_OUTPUT_DIR)

ifneq ($(strip $(CHART_VERSION)),)
HELM_PACKAGE_ARGS += --version $(CHART_VERSION)
endif

ifneq ($(strip $(CHART_APP_VERSION)),)
HELM_PACKAGE_ARGS += --app-version $(CHART_APP_VERSION)
endif

ifeq ($(IMAGE_BUILDER),auto)
IMAGE_BUILDER := $(shell if command -v docker >/dev/null 2>&1; then echo docker; elif command -v buildah >/dev/null 2>&1; then echo buildah; elif command -v podman >/dev/null 2>&1; then echo podman; else echo none; fi)
endif

run:
	go run ./cmd/relay

build:
	go build ./...

test:
	go test ./...

image:
ifeq ($(IMAGE_BUILDER),docker)
	docker build -t $(IMAGE) .
else ifeq ($(IMAGE_BUILDER),buildah)
	mkdir -p $(BUILDAH_RUNTIME_DIR) $(BUILDAH_TMPDIR)
	XDG_RUNTIME_DIR=$(BUILDAH_RUNTIME_DIR) TMPDIR=$(BUILDAH_TMPDIR) STORAGE_DRIVER=$(BUILDAH_STORAGE_DRIVER) BUILDAH_ISOLATION=$(BUILDAH_ISOLATION) buildah bud --storage-driver $(BUILDAH_STORAGE_DRIVER) --isolation $(BUILDAH_ISOLATION) -t $(IMAGE) .
else ifeq ($(IMAGE_BUILDER),podman)
	podman build -t $(IMAGE) .
else
	@echo "no supported image builder found; install docker, buildah, or podman, or set IMAGE_BUILDER explicitly" >&2
	@exit 1
endif

helm-lint:
	helm lint $(CHART_DIR)

helm-template:
	helm template $(HELM_TEMPLATE_RELEASE) $(CHART_DIR) $(HELM_TEMPLATE_ARGS)

helm-package:
	mkdir -p $(CHART_OUTPUT_DIR)
	helm package $(CHART_DIR) $(HELM_PACKAGE_ARGS)
