IMAGE ?= rootpv:dev
PLATFORM ?= linux/amd64
PLATFORMS ?= linux/amd64,linux/arm64
PUSH_SLEEP_SECONDS ?= 10
PROVENANCE ?= false

.PHONY: test build image push push-multiarch deploy helm-lint helm-template

test:
	go test ./...

build:
	go build ./cmd/persistent-rootfs-controller
	go build ./cmd/persistent-rootfsd
	go build ./cmd/rootfs-runtime-wrapper

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
