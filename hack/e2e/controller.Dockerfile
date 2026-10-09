# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# The controller image from a binary built on the host, for
# KARDINAL_E2E_BUILD=host (hack/e2e/components/kardinal.sh): hosts where the
# repo Dockerfile can't download Go modules still get the image the release
# ships. Keep the runtime stages in step with the repo Dockerfile.
#
# RUNTIME is the final stage's base: a -race build (KARDINAL_E2E_RACE=1) is
# linked against glibc, so it runs on a glibc image instead of alpine.

ARG RUNTIME=alpine:3.24

FROM alpine:3.24 AS runtime-files

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 65532 nonroot

FROM ${RUNTIME}

COPY --from=runtime-files /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=runtime-files --chown=65532:65532 /home/nonroot /home/nonroot
# --chmod: a binary built under umask 077 is 0700, which uid 65532 can't exec.
COPY --chmod=0755 kardinal-controller /bin/kardinal-controller

USER 65532:65532

ENTRYPOINT ["/bin/kardinal-controller"]
