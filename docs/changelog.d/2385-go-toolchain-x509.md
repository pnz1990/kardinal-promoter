---
section: Security
---

- **Built with Go 1.26.10** (#2385), for GO-2026-6809 in the standard library's `crypto/x509`. TLS clients verify the server's certificate with `x509.Certificate.Verify`, which that release fixes, so every outbound HTTPS connection was affected: the CLI's calls to the Kubernetes API, and the controller's to the SCM, registries, sigstore, ticketing and webhooks. v0.12.3's CLI and its `kardinal-controller-*` binaries were built with go1.26.9. Its controller and render images happened to be built with go1.27.3, which has the fix, because their Dockerfiles used a floating `golang:1.27-alpine`. They are now built with go.mod's toolchain, pinned by digest.
