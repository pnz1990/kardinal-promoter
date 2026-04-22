<!--
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
-->

# Image Signature Verification (`verify-image`)

kardinal includes a built-in `verify-image` promotion step that calls
[cosign](https://docs.sigstore.dev/cosign/) to verify container image signatures before
advancing a Bundle to the next environment. This gives platform teams supply-chain
assurance: only images signed by your CI pipeline reach production.

---

## Prerequisites

- `cosign` must be available in `$PATH` inside the controller's container image.
  See [Installing cosign](https://docs.sigstore.dev/cosign/system_config/installation/).
- Images must be signed at build time using `cosign sign` (keyless OIDC or key-based).

---

## Quick start

Add `verify-image` to a Pipeline environment's `steps:` list before the git operations:

```yaml
spec:
  environments:
    - name: prod
      approval: pr-review
      steps:
        - uses: verify-image
          inputs:
            cosign.issuer: "https://token.actions.githubusercontent.com"
            cosign.identityRegexp: "https://github.com/myorg/myapp/.github/workflows/release.yml@refs/heads/main"
        - uses: git-clone
        - uses: kustomize-set-image
        - uses: git-commit
        - uses: git-push
        - uses: open-pr
        - uses: wait-for-merge
        - uses: health-check
```

If the signature is absent or invalid for any image in the Bundle, the `verify-image`
step returns `Failed` and the PromotionStep halts — no code reaches production.

---

## Inputs

| Input | Required | Description |
|---|---|---|
| `cosign.issuer` | No | OIDC certificate issuer URL, e.g. `https://token.actions.githubusercontent.com` |
| `cosign.identityRegexp` | No | Regular expression matching the signing certificate identity, e.g. `https://github.com/myorg/.*` |

When **neither** input is set, `verify-image` runs `cosign verify <image>` without
keyless identity constraints — verifying that at least one valid signature exists in
the registry. This is a weaker check (any signer qualifies) and is not recommended
for production pipelines.

When **both** inputs are set, the step runs:

```
cosign verify \
  --certificate-oidc-issuer <issuer> \
  --certificate-identity-regexp <regex> \
  <image>
```

---

## Digest-pinned images

When a Bundle image has both a `tag` and a `digest`, the step verifies using the
digest-pinned reference (`repo@sha256:...`). Digest pinning is always preferred over
tag references for signature verification — tags are mutable and can be overwritten,
while digests are content-addressed and immutable.

---

## Outputs

`verify-image` produces no step outputs. Downstream steps receive the unmodified
`Outputs` map from earlier steps.

---

## Failure behaviour

When `cosign verify` exits non-zero for any image, `verify-image` returns `StepFailed`
with a message containing the image reference and cosign's output. The PromotionStep
transitions to `Failed` and the Bundle stops advancing.

Example failure message:

```
signature verification failed for ghcr.io/myorg/app:v1.2.0:
Error: no matching signatures:
verifying signature: ...
```

---

## Kargo comparison

Kargo v1.10 introduced a `verify` step that calls `cosign verify` in the same way.
kardinal's `verify-image` step is the equivalent for kardinal-managed pipelines.
Teams evaluating both tools can use the same cosign signing workflow with either
orchestrator.
