# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# The kardinal-render image: the render Job of a layout: branch environment
# (docs/rendered-manifests.md). It runs only as a RenderRun's Job, never as
# part of the controller. Every build stage runs on the build platform and
# cross-compiles, as in the controller Dockerfile.

# The Go of go.mod's toolchain line, by digest (hack/hygiene checks it): the
# image builds with the Go the release binaries and govulncheck use.
FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder

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
