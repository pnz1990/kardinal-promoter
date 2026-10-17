---
section: Added
---

- **Receipts signed with AWS KMS or Google Cloud KMS** (#1952) — `receipts.signing.mode: kms` with `receipts.signing.kmsKey: awskms://...` or `gcpkms://...`: the controller image is built with both providers, uses the cloud SDK's default credentials (IRSA, EKS Pod Identity, GKE Workload Identity), sends the KMS calls through the egress guard and allowlist, and never runs the sigstore CLI-plugin fallback ([Receipts](receipts.md#kms-keys))
