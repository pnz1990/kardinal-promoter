# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# The controller image from a binary built on the host, for
# KARDINAL_E2E_BUILD=host (hack/e2e/components/kardinal.sh): hosts where the
# repo Dockerfile can't download Go modules still get the image the release
# ships. Keep the runtime stages in step with the repo Dockerfile.

FROM alpine:3.24 AS runtime-files

ARG KUSTOMIZE_VERSION=v5.4.3
ARG KUSTOMIZE_SHA256_AMD64=3669470b454d865c8184d6bce78df05e977c9aea31c30df3c669317d43bcc7a7

RUN apk add --no-cache ca-certificates curl && \
    curl -fsSL -o /tmp/kustomize.tar.gz \
      "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" && \
    echo "${KUSTOMIZE_SHA256_AMD64}  /tmp/kustomize.tar.gz" | sha256sum -c - && \
    mkdir -p /out && \
    tar -xzf /tmp/kustomize.tar.gz -C /out kustomize && \
    adduser -D -u 65532 nonroot

FROM alpine:3.24

COPY --from=runtime-files /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=runtime-files --chown=65532:65532 /home/nonroot /home/nonroot
COPY --from=runtime-files /out/kustomize /usr/local/bin/kustomize
# --chmod: a binary built under umask 077 is 0700, which uid 65532 can't exec.
COPY --chmod=0755 kardinal-controller /bin/kardinal-controller

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-controller"]
