---
section: Security
---

- **A promotion receipt never signs a credential** (#2158). Every string of the receipt's record and every subject name is redacted before signing: a URL's user info, and the value of a credential parameter such as `?token=` (a percent-encoded name too). `kardinalPromotion.redacted` lists what was changed. Before, a token in a version's `provenance.ciRunURL` was signed into the receipt and, with the Rekor sink, published for good. The Bundle API and `kardinal create bundle` now refuse a `ciRunURL` with a credential parameter, and the Bundle CRD refuses one, and user info, for a version created with kubectl ([Receipts](receipts.md#credentials-are-redacted), [Provenance](ci-integration.md#provenance))
