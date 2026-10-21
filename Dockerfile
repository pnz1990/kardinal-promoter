# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# Every build stage runs on the build machine's platform (BUILDPLATFORM) and
# cross-compiles or downloads for the target platform (TARGETARCH), so a
# multi-arch build runs no emulated commands. Only the final stage uses the
# target platform's base image, and it only copies files.

# ── Stage 1: UI builder ───────────────────────────────────────────────────────
# The UI bundle is the same on every platform.
FROM --platform=$BUILDPLATFORM node:26-alpine AS ui-builder

# The UI's type check reads scripts/gen-tokens.mjs and its type declaration
# (the token tests import them), so the stage mirrors the repo layout:
# /src/web and /src/scripts.
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --silent
COPY scripts/gen-tokens.mjs scripts/gen-tokens.d.mts /src/scripts/
COPY web/ ./
RUN npm run build

# ── Stage 2: Go builder ───────────────────────────────────────────────────────
# The Go that builds the image is go.mod's toolchain, pinned by digest
# (TestDockerfilesBuildWithTheToolchain): the Go govulncheck audits is the Go
# the images ship. A floating golang:1.27-alpine built v0.12.3's images with
# go1.27.3 while its binaries were built with go1.26.9.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS builder

ARG TARGETOS
ARG TARGETARCH
# VERSION is what the controller reports (kardinal version, the kardinal-version
# ConfigMap). release.yml passes the tag.
ARG VERSION=dev

WORKDIR /workspace

# Copy dependency manifests first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Copy compiled UI assets from ui-builder stage
COPY --from=ui-builder /src/web/dist ./web/dist

# CGO disabled for a fully static binary. .dockerignore excludes .git, so there
# is no VCS information to stamp; VERSION carries the version instead.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -buildvcs=false \
      -ldflags="-s -w -X main.ControllerVersion=${VERSION}" \
      -o /out/kardinal-controller \
      ./cmd/kardinal-controller

# ── Stage 3: runtime files (CA certificates, nonroot user) ───────────────────
FROM --platform=$BUILDPLATFORM alpine:3.24 AS runtime-files

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 65532 nonroot

# ── Stage 4: runtime ──────────────────────────────────────────────────────────
# The controller runs git through go-git: it needs no binaries besides its
# own. layout: branch renders run in the kardinal-render image
# (render.Dockerfile), never in the controller.
FROM alpine:3.24

COPY --from=runtime-files /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=runtime-files --chown=65532:65532 /home/nonroot /home/nonroot
COPY --from=builder /out/kardinal-controller /bin/kardinal-controller

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-controller"]
