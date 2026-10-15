---
section: Fixed
---

- **Attestations no longer crowd out an image's signatures** (#1950) — ImageVerification reads at most 32 Sigstore bundle referrers per image, and counted attestations (SBOM, provenance, promotion receipts) among them, so an image with more than 32 of those could fail verification although it was signed. A referrer whose `dev.sigstore.bundle.predicateType` annotation names another predicate than a cosign image signature is now skipped before it counts ([Image verification](image-verification.md#signature-formats)).
