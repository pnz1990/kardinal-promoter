---
section: Added
---

- **Attestation age: `attestations[].maxAge`** ([Attestation age](image-verification.md#attestation-age), #2044) — a Verified ImageVerification no longer holds a scan forever. With `maxAge: 24h`, each result records `verifiedAt` and `staleAt` (from the earlier of the scan's `scannedAt` and its verified signing time, never later than when kardinal first recorded it, `datedAt`), turns `stale` when that time passes, and the image's referrers are read again (a quarter of `maxAge`, between 5 minutes and an hour) until a newer attestation replaces it. The phase stays Verified; a gate decides with `attestations.<name>.stale`, for example `!attestations.vulns.stale && attestations.vulns.effective.critical == 0`. A required attestation that is already stale is waited for like a missing one (`AttestationStale`).
