---
section: Added
---

- **Attestations: SLSA provenance, vulnerability scans with VEX, any in-toto predicate** (#1963, [Attestations](image-verification.md#attestations)) — `spec.imageVerification.attestations` reads each image's attestations from its OCI referrers (Sigstore bundles, `cosign attest --new-bundle-format`), verifies them against the policy's authorities, and records what they say in the ImageVerification's `status.attestations`: the SLSA build level the policy grants the provenance's builder (or a verified SLSA VSA's), its source commit, a Trivy, Grype or in-toto scan's findings by severity before and after OpenVEX statements from `vex.authorities`, and the predicate as JSON. `required` and `slsa.minLevel` hold the version before its first environments; gates decide the rest per environment in CEL over [`attestations.*`](reference/cel-context.md#attestations), for example `attestations.provenance.slsa.buildLevel >= 3 && attestations.vulns.effective.critical == 0`.
