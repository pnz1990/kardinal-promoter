# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# The kardinal-render image: the render Job of a layout: branch environment
# (docs/rendered-manifests.md). It runs only as a RenderRun's Job, never as
# part of the controller. Every build stage runs on the build platform and
# cross-compiles, as in the controller Dockerfile.

# The Go is go.mod's toolchain, pinned by digest, as in the controller
# Dockerfile (TestDockerfilesBuildWithTheToolchain).
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -buildvcs=false -ldflags="-s -w" -o /out/kardinal-render ./cmd/kardinal-render

FROM --platform=$BUILDPLATFORM alpine:3.24 AS runtime-files

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 65532 nonroot

# kustomize and Helm run in process (sigs.k8s.io/kustomize/api,
# helm.sh/helm/v4) and git through go-git: no binaries, no shell needed.
FROM alpine:3.24

COPY --from=runtime-files /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=builder /out/kardinal-render /bin/kardinal-render

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-render"]
