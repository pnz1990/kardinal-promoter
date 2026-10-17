---
section: Security
---

- **A promotion receipt never signs a credential it can recognise** (#2158). Every string of the receipt's record and every subject name is redacted before signing:
  - a URL's user info;
  - the value of a credential query or fragment parameter: a name that ends in `token`, `secret`, `key`, `signature`, `password`, `session`, `ticket`, `bearer`, ... (not harmless names such as `monkey`), or is `sig`, `auth`, `authorization`, `jwt`, `code` or `sid`, in any case, with or without `-`, `_` or an array index;
  - GitHub, GitLab and Slack tokens and AWS access key IDs, wherever they appear.

  `kardinalPromotion.redacted` lists what was changed. Before, a token in a version's `provenance.ciRunURL` was signed into the receipt and, with the Rekor sink, published for good. The Bundle API, `kardinal create bundle` and the Bundle CRD now refuse such a `ciRunURL` ([Receipts](receipts.md#credentials-are-redacted), [Provenance](ci-integration.md#provenance))
