IMAGE ?= rootpv:dev
PLATFORM ?= linux/amd64
PLATFORMS ?= linux/amd64,linux/arm64
PUSH_SLEEP_SECONDS ?= 10
PROVENANCE ?= false
E2E_CONFIG ?= tests/e2e/configs/.ebs-cluster.env

.PHONY: test build image push push-multiarch deploy helm-lint helm-template e2e-functional e2e-performance

test:
	go test ./...

build:
	go build ./cmd/persistent-rootfs-controller
	go build ./cmd/persistent-rootfsd
	go build ./cmd/rootfs-runtime-wrapper
	go build ./cmd/containerd-shim-rootpv-kata-v2

image:
	docker build --platform $(PLATFORM) -t $(IMAGE) .

push:
	docker push $(IMAGE)

push-multiarch:
	IMAGE="$(IMAGE)" PLATFORMS="$(PLATFORMS)" PUSH_SLEEP_SECONDS="$(PUSH_SLEEP_SECONDS)" PROVENANCE="$(PROVENANCE)" scripts/build-push-multiarch-image.sh

deploy:
	kubectl apply -f deploy/crds
	kubectl apply -f deploy/manifests

helm-lint:
	helm lint charts/rootpv

helm-template:
	helm template rootpv charts/rootpv --namespace rootpv-system --include-crds

e2e-functional:
	tests/e2e/run-functional.sh --config $(E2E_CONFIG)

e2e-performance:
	tests/e2e/run-performance.sh --config $(E2E_CONFIG)
