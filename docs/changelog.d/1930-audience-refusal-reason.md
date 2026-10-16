---
section: Changed
---

- **A 401 from the TokenReview audience rules says which rule refused the token** (#1930). The UI API and the Bundle API answer `unauthorized: token audience is not kardinal-promoter; this API does not accept the API server's audience` when that API has no opt-in, and `unauthorized: a ServiceAccount token for the API server's audience is refused: ...` for a ServiceAccount's token. The access log records the same reason. Any other refusal (an invalid or expired token) still says only `unauthorized`, so the API server's own error is never shown ([TokenReview](guides/security.md#option-2-kubernetes-tokens-tokenreview)).
