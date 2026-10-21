---
section: Security
---

- **Built with Go 1.26.10** (#2385), for GO-2026-6809 in `crypto/x509`: certificate verification, in every TLS connection the controller and the CLI make (the SCM APIs, image registries, Fulcio, Rekor and the timestamp authority, ServiceNow, Jira, notification webhooks and the git transports) and in receipt verification, now refuses the intermediate certificates Go no longer trusts. The controller and render images now build with the same Go as the release binaries, go.mod's toolchain pinned by digest: they had used a floating `golang:1.27-alpine` (Go 1.27.3 at this release), so the images move from Go 1.27.3 to Go 1.26.10, within go.mod's language version (Go 1.26).
