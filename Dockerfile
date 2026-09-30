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

# ── Stage 3: runtime files (kustomize, CA certificates, nonroot user) ─────────
FROM --platform=$BUILDPLATFORM alpine:3.24 AS runtime-files

ARG TARGETARCH
ARG KUSTOMIZE_VERSION=v5.4.3
# sha256 of kustomize_<version>_linux_<arch>.tar.gz, from the release's
# checksums.txt. Update both when KUSTOMIZE_VERSION changes.
ARG KUSTOMIZE_SHA256_AMD64=3669470b454d865c8184d6bce78df05e977c9aea31c30df3c669317d43bcc7a7
ARG KUSTOMIZE_SHA256_ARM64=1b515578b0af12c15d9856720066ce2fe66756d63785b2cbccaf2885beb2381c

RUN apk add --no-cache ca-certificates curl && \
    case "${TARGETARCH}" in \
      amd64) sum="${KUSTOMIZE_SHA256_AMD64}" ;; \
      arm64) sum="${KUSTOMIZE_SHA256_ARM64}" ;; \
      *) echo "no kustomize checksum for TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL -o /tmp/kustomize.tar.gz \
      "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_${TARGETARCH}.tar.gz" && \
    echo "${sum}  /tmp/kustomize.tar.gz" | sha256sum -c - && \
    mkdir -p /out && \
    tar -xzf /tmp/kustomize.tar.gz -C /out kustomize && \
    adduser -D -u 65532 nonroot

# ── Stage 4: runtime ──────────────────────────────────────────────────────────
# The controller runs git through go-git and needs only kustomize on PATH.
FROM alpine:3.24

COPY --from=runtime-files /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=runtime-files --chown=65532:65532 /home/nonroot /home/nonroot
COPY --from=runtime-files /out/kustomize /usr/local/bin/kustomize
COPY --from=builder /out/kardinal-controller /bin/kardinal-controller

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-controller"]
