# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# ── Stage 1: builder ──────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# Install git (needed by go modules for VCS stamping)
RUN apk add --no-cache git

WORKDIR /workspace

# Copy dependency manifests first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build the controller binary — CGO disabled for a fully static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -ldflags="-s -w" \
      -o /bin/kardinal-controller \
      ./cmd/kardinal-controller

# ── Stage 2: final (distroless, nonroot) ──────────────────────────────────────
FROM gcr.io/distroless/static:nonroot

# Copy the binary from the builder stage
COPY --from=builder /bin/kardinal-controller /bin/kardinal-controller

# Distroless nonroot image runs as UID 65532 (nonroot) by default.
# No shell, no package manager, minimal attack surface.

ENTRYPOINT ["/bin/kardinal-controller"]
