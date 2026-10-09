# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# Every build stage runs on the build machine's platform (BUILDPLATFORM) and
# cross-compiles or downloads for the target platform (TARGETARCH), so a
# multi-arch build runs no emulated commands. Only the final stage uses the
# target platform's base image, and it only copies files.

# ── Stage 1: UI builder ───────────────────────────────────────────────────────
# The UI bundle is the same on every platform.
FROM --platform=$BUILDPLATFORM node:26-alpine AS ui-builder

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --silent
COPY web/ ./
RUN npm run build

# ── Stage 2: Go builder ───────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

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
COPY --from=ui-builder /web/dist ./web/dist

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
