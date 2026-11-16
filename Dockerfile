# Copyright (c) 2026 OpenFuyao Contributors.
# RootPV is licensed under Mulan PSL v2.
# You can use this software according to the terms and conditions of the Mulan PSL v2.
# You may obtain a copy of Mulan PSL v2 at:
# http://license.coscl.org.cn/MulanPSL2
# THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
# EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
# MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
# See the Mulan PSL v2 for more details.

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN echo "building persistent-rootfsd for ${TARGETOS}/${TARGETARCH}" && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/persistent-rootfsd ./cmd/persistent-rootfsd
RUN echo "building persistent-rootfs-controller for ${TARGETOS}/${TARGETARCH}" && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/persistent-rootfs-controller ./cmd/persistent-rootfs-controller
RUN echo "building rootfs-runtime-wrapper for ${TARGETOS}/${TARGETARCH}" && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/rootfs-runtime-wrapper ./cmd/rootfs-runtime-wrapper

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/containerd-shim-rootpv-kata-v2 ./cmd/containerd-shim-rootpv-kata-v2

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS crio-storage-build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY tools/crio-storage/go.mod tools/crio-storage/go.sum ./
RUN go mod download
COPY tools/crio-storage/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags 'exclude_graphdriver_btrfs,exclude_graphdriver_zfs' -o /out/rootpv-crio-storage .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates procps python3 util-linux && rm -rf /var/lib/apt/lists/*
COPY --from=crio-storage-build /out/rootpv-crio-storage /usr/local/bin/rootpv-crio-storage
COPY --from=build /out/persistent-rootfsd /usr/local/bin/persistent-rootfsd
COPY --from=build /out/persistent-rootfs-controller /usr/local/bin/persistent-rootfs-controller
COPY --from=build /out/rootfs-runtime-wrapper /usr/local/bin/rootfs-runtime-wrapper
COPY --from=build /out/containerd-shim-rootpv-kata-v2 /usr/local/bin/containerd-shim-rootpv-kata-v2
COPY scripts/configure-containerd-rootpv.sh /usr/local/bin/scripts/configure-containerd-rootpv.sh
ENTRYPOINT ["/usr/local/bin/persistent-rootfsd"]
