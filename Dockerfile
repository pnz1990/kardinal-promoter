# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# ── Stage 1: UI builder ───────────────────────────────────────────────────────
FROM node:22-alpine AS ui-builder

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --silent
COPY web/ ./
RUN npm run build

# ── Stage 2: Go builder ───────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# Install git (needed by go modules for VCS stamping)
RUN apk add --no-cache git

WORKDIR /workspace

# Copy dependency manifests first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Copy compiled UI assets from ui-builder stage
COPY --from=ui-builder /web/dist ./web/dist

# Build the controller binary — CGO disabled for a fully static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -ldflags="-s -w" \
      -o /bin/kardinal-controller \
      ./cmd/kardinal-controller

# ── Stage 3: runtime (kustomize only — git binary no longer required) ────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates curl && \
    KUSTOMIZE_VER=v5.4.3 && \
    curl -sL "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VER}/kustomize_${KUSTOMIZE_VER}_linux_amd64.tar.gz" \
      | tar -xz -C /usr/local/bin kustomize && \
    adduser -D -u 65532 nonroot

# Copy the binary from the builder stage
COPY --from=builder /bin/kardinal-controller /bin/kardinal-controller

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-controller"]
