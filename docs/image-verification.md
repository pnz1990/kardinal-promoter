# Image Signature Verification

A Pipeline can require every Bundle's images to be signed, and a config Bundle's commit to be
signed, before the Bundle is promoted anywhere. kardinal checks the signatures in the registry
with [sigstore-go](https://github.com/sigstore/sigstore-go): a key (`cosign sign --key`) or a
Sigstore keyless identity (`cosign sign` from CI with OIDC). A Bundle that does not pass is never
promoted.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
  namespace: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
  imageVerification:
    images: ["ghcr.io/myorg/*"]          # which Bundle images; empty = all
    authorities:
      - name: release-key
        key:
          secretRef: {name: cosign-pub, key: cosign.pub}
      - name: ci
        keyless:
          issuer: https://token.actions.githubusercontent.com
          subject: https://github.com/myorg/my-app/.github/workflows/release.yml@refs/heads/main
    commits:
      requireSigned: true                # config Bundles: the commit must be signed
    timeout: 10m
  environments:
    - name: staging
    - name: prod
```

```bash
kubectl create secret generic cosign-pub -n my-app --from-file=cosign.pub
kardinal create bundle my-app --image ghcr.io/myorg/my-app@sha256:4c3b...   # by digest
```

## Fields

`spec.imageVerification`:

| Field | Default | Description |
|---|---|---|
| `images[]` | all | Repository patterns of the Bundle images to verify; `*` matches any characters. |
| `authorities[]` | | Accepted signers, up to 10. An image is verified when one of its signatures verifies against any one authority. Each has a `name` and exactly one of `key` and `keyless`. |
| `authorities[].key.secretRef` | | `{name, key}`: a Secret in the Pipeline namespace with the PEM public key (`cosign.pub`). |
| `authorities[].key.requireTransparencyLog` | `false` | Require a Rekor entry, checked against the Sigstore public-good log. Without it a key signature is checked against the key alone (`cosign verify --insecure-ignore-tlog`). |
| `authorities[].keyless.issuer` | | OIDC issuer of the signing certificate. |
| `authorities[].keyless.subject` / `subjectRegExp` | | The certificate's subject (exactly one of the two). |
| `authorities[].keyless.trustedRootRef` | public good | `{name, key}`: a Secret with a Sigstore `trusted_root.json`, for a private Sigstore. Without it the controller fetches the public-good trusted root through TUF (from `tuf-repo-cdn.sigstore.dev`) on first use. |
| `commits.requireSigned` | `false` | A config or mixed Bundle's `configRef.commitSHA` must be signed with a signature the SCM verified (GitHub, GitLab, Forgejo and Gitea report it; Bitbucket and Azure DevOps do not, and such a Bundle fails). |
| `signatureRepository` | next to the image | Where the signatures are, when they are not next to the images (cosign's `COSIGN_REPOSITORY`). |
| `registrySecretRef.name` | anonymous | A `kubernetes.io/dockerconfigjson` Secret in the Pipeline namespace for private registries. |
| `insecureRegistries[]` | | Registry hosts read over plain HTTP. Every other registry is HTTPS only. |
| `timeout` | `10m` | How long a missing signature is waited for (CI may sign after it pushes). |

## Signature formats

- **Sigstore bundles attached as OCI referrers**: cosign v3's default, and cosign v2 with
  `--new-bundle-format`. Key and keyless.
- **Legacy cosign signatures** in the `sha256-<digest>.sig` tag (cosign v2's default): key
  authorities only. A keyless signature must be a Sigstore bundle.

## How it runs

1. **Digests are required.** Verifying a tag and then promoting the tag would let a different
   image be pushed under the tag in between. When the policy selects an image that the Bundle names
   by tag, the Bundle fails before any environment, with condition `InvalidSpec` (reason
   `GraphBuildFailed`): `image ... is not pinned by digest`. A [Subscription](subscription.md)
   creates Bundles by digest; from CI, pass `--image repo@sha256:...`.
2. The Bundle's Graph creates one `ImageVerification` (`kubectl get imageverifications`) with the
   selected images and the policy. Its controller reads the signatures from the registry and the
   commit's signature from the SCM, and records a result per image in `status.images`.
3. The first environments (those with no upstream) wait in `Pending` with the message
   `waiting for image verification <name>: ...`, and so do their
   [pre-deploy hooks](hooks.md). Nothing is committed or deployed.
4. **Verified**: the environments start. Later environments need nothing more: their upstream
   already waited.
   **Failed**: the waiting steps fail, and the Bundle with them. A verification fails when a
   signature is there but none verifies against an authority, when the commit is not signed with a
   verified signature, or when no signature arrived within `timeout`. A registry or SCM outage is
   retried, never treated as verified, until the timeout.

```bash
kubectl get imageverification -n my-app my-app-my-app-x7k2p-verify -o jsonpath='{.status}'
```

## What it cannot do

- It verifies before promotion, not at deploy time in the target cluster. To enforce signatures
  on every Pod, whoever deploys it, add admission-time verification there:
  [Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/) or
  [Kyverno `verifyImages`](https://kyverno.io/docs/policy-types/cluster-policy/verify-images/).
  A change pushed to the GitOps repository outside kardinal is not verified by kardinal.
- A verified image is not checked again: a key revoked after the verification is not seen.
- Commit signatures are taken from the SCM's own verification (the keys its users registered),
  not checked against a key in the policy.

## Permissions

The controller reads the key and trusted-root Secrets (it already has `get` on Secrets) and
writes `imageverifications`; the Graph ServiceAccount creates them. See
[Graph coverage](graph-coverage.md).
