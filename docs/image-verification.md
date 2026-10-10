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
kubectl label secret cosign-pub -n my-app kardinal.io/referenceable=true
kardinal create bundle my-app --image ghcr.io/myorg/my-app@sha256:4c3b...   # by digest
```

Every Secret a policy names (key, trusted root, registry credentials) must carry the label
`kardinal.io/referenceable: "true"`, as for every Secret a kardinal resource names: the Secret's
owner opts in. Without it the controller does not read the Secret, and the verification waits
with `status.reason: SecretNotReferenceable` until the label is added or the timeout passes.

## Fields

`spec.imageVerification`:

| Field | Default | Description |
|---|---|---|
| `images[]` | all | Repository patterns of the Bundle images to verify; `*` matches any characters. Patterns and Bundle images are compared in normalized form: the registry host lowercased and without `:443`, `docker.io` for Docker Hub (`index.docker.io`, `registry-1.docker.io` or no host) and `library/` for official images (`nginx` is `docker.io/library/nginx`). A Bundle image that does not parse as a reference fails the Bundle. |
| `authorities[]` | | Accepted signers, up to 10. An image is verified when one of its signatures verifies against any one authority. Each has a `name` and exactly one of `key` and `keyless`. |
| `authorities[].key.secretRef` | | `{name, key}`: a Secret in the Pipeline namespace with the PEM public key (`cosign.pub`). |
| `authorities[].key.requireTransparencyLog` | `false` | Require a Rekor entry, checked against the Sigstore public-good log. Without it a key signature is checked against the key alone (`cosign verify --insecure-ignore-tlog`). |
| `authorities[].keyless.issuer` | | OIDC issuer of the signing certificate. |
| `authorities[].keyless.subject` / `subjectRegExp` | | The certificate's subject (exactly one of the two). `subjectRegExp` must match the whole subject: it is anchored as `^(?:...)$`. |
| `authorities[].keyless.trustedRootRef` | public good | `{name, key}`: a Secret with a Sigstore `trusted_root.json`, for a private Sigstore. Without it the controller fetches the public-good trusted root through TUF (from `tuf-repo-cdn.sigstore.dev`) on first use and again once a day; a failed refresh keeps the previous root and is retried after 5 minutes. Each TUF request times out after 20s. |
| `commits.requireSigned` | `false` | A config or mixed Bundle's `configRef.commitSHA` must be signed with a signature the SCM verified (GitHub, GitLab, Forgejo and Gitea report it; Bitbucket and Azure DevOps do not, and such a Bundle fails). See [Signed commits](#signed-commits). |
| `commits.allowedSigners[]` | any person | Also require the signer to be one of these SCM logins or emails. Platform signatures (`web-flow`, `gitlab-system`, `forgejo-instance`) are accepted only when listed. |
| `signatureRepository` | next to the image | Where the signatures are, when they are not next to the images (cosign's `COSIGN_REPOSITORY`). |
| `registrySecretRef.name` | anonymous | A `kubernetes.io/dockerconfigjson` Secret in the Pipeline namespace for private registries. |
| `insecureRegistries[]` | | Registry hosts read over plain HTTP. Every other registry is HTTPS only. |
| `timeout` | `10m` | How long a missing signature is waited for (CI may sign after it pushes). |

## Signature formats

- **Sigstore bundles attached as OCI referrers**: cosign v3's default, and cosign v2 with
  `--new-bundle-format`. Key and keyless. The bundle must be a cosign image signature: a DSSE
  in-toto statement with predicate type `https://sigstore.dev/cosign/sign/v1` whose subject is the
  image digest. Attestations (SBOM, provenance) and plain message signatures attached to the same
  image are not signatures of it, whoever signed them.
- **Legacy cosign signatures** in the `sha256-<digest>.sig` tag (cosign v2's default): key
  authorities only. A keyless signature must be a Sigstore bundle.

With `requireTransparencyLog`, the Rekor entry is checked offline from the bundle: its signed
entry timestamp and inclusion proof against the Rekor public key in the trusted root. The
controller does not query Rekor, so an entry that the log no longer serves is not noticed.

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
   **Failed**: the waiting steps fail, and the Bundle with them. A signature that does not verify
   against an authority (another signer's, an attestation, one that is malformed) is not a
   verdict: an image can carry other signatures before CI's arrives, so the verification keeps
   waiting for one that verifies, and fails at the `timeout` (`status.reason:
   SignatureNotVerified`, with every reason in the message). It fails at once when a cosign
   signature made with a policy key names another digest, or when the commit is not signed as
   required. A registry or SCM outage is retried, never treated as verified, until the timeout.
   Each registry fetch times out after 45s and each check after 2 minutes; up to 4
   ImageVerifications are checked at once.
5. Before it promotes, the step compares the Bundle's images with the images the
   ImageVerification verified (both normalized) and refuses to promote when a digest differs or a
   verified image is missing. The Bundle's images cannot change anyway: the Bundle CRD refuses an
   update of `spec.images`.

`status.reason` is one of `SecretNotReferenceable`, `SecretNotFound`, `InvalidPublicKey`,
`InvalidTrustedRoot`, `InvalidRegistryCredentials`, `SignatureNotVerified`, `CommitNotVerified`
and `Timeout`. A key, trusted root or `.dockerconfigjson` that does not parse is reported by Secret
and key name only; nothing of its content is copied to the status.

**Policy changes.** An ImageVerification's spec is immutable, and its name carries a hash of it.
Editing `spec.imageVerification` gives each Bundle a new ImageVerification; the first
environments that have not started yet wait for the new verdict. Environments that already
started are not checked again.

**`update.strategy: argocd`** writes an image pinned by digest as `<tag>@<digest>` (see
[Argo CD native promotion](argocd-native-promotion.md#multi-image-bundles)), so the digest that
was verified is the one deployed. The kustomize and Helm strategies write the digest too.

## Signed commits

With `commits.requireSigned`, the config Bundle's `configRef.commitSHA` must be the full 40- or
64-character SHA (a short one fails the Bundle). The commit is checked with the Pipeline's SCM
provider: the ScmProvider or ClusterScmProvider of its `spec.git.providerRef`
([several SCM providers](scm-providers.md#several-scm-providers-scmprovider-and-clusterscmprovider)),
otherwise the controller's `--scm-provider`. The repository must be on that provider's host
(its `apiURL`, or `--scm-api-url`; `api.github.com` is `github.com`). The SCM is asked about
that exact commit, and the SHA it answers for must be the same.

With a `providerRef`, the provider is resolved as for the Pipeline's PRs: its own token, the
same UID the Bundle's Graph was built with, a ClusterScmProvider's `allowedNamespaces`, and its
`allowedRepositories`, which must allow the config repository (`configRef.gitRepo`, or
`spec.git.url`). A provider that is gone, was created again, or does not allow the namespace or
the repository fails the ImageVerification with the reason; it never falls back to the
controller's provider. So does a provider whose `apiURL` is refused (not a URL, or `http://`
without `scm.providersAllowInsecureHTTP`). A provider that cannot be used yet (its token Secret
missing or not labeled `kardinal.io/referenceable`) is retried until the policy's timeout.

The SCM's verdict is about the key it holds for the signer; `allowedSigners` narrows it to the
people you accept, by login or email as the SCM reports the verified signer. Forgejo reports the
signer's login in `signer.name`, Gitea in `signer.username`; kardinal reads either, and looks
the login up (`GET /api/v1/users/{login}`) to tell a person from the instance key. Give the
controller's token the `read:user` scope; without it the lookup is made anonymously, so a
private user's signature reads as the instance key and is refused. GitLab is matched on
the key owner's verified email (for SSH signatures, whose response names the key only by a title
its owner chose, on the commit's committer email, which GitLab checked against the key's owner);
it reports no full GPG fingerprint, so keys are not matched.

Commits signed by the platform rather than a person are **refused** unless `allowedSigners`
lists the platform identity explicitly:

| Platform | Identity | Which commits |
|---|---|---|
| GitHub | `web-flow` | web UI edits, merges, squash merges and reverts, signed with GitHub's key |
| GitLab | `gitlab-system` | commits GitLab created and signed itself (status `verified_system`: web UI, API) |
| Forgejo / Gitea | `forgejo-instance` | commits signed with the instance key (`repository.signing`): a verified signature whose signer is not a user of the instance (`GET /api/v1/users/{name}` answers 404), or one named in `--scm-instance-signers` (Helm `scm.instanceSigners`: the instance's `SIGNING_NAME` or `SIGNING_EMAIL`), or, for a Pipeline's ScmProvider or ClusterScmProvider, in its `spec.instanceSigners` |

To accept PRs merged in the web UI (the usual GitOps flow), list the identity, for example
`allowedSigners: [web-flow, alice, bob@example.com]`. Anyone who can merge through the web UI then
gets such a commit, so pair it with branch protection that requires reviews.

In a [compact Graph](pipeline-reference.md#large-pipelines) (above `--graph-compact-above`
environments) it works the same: the root environments' PromotionSteps name the
ImageVerification and wait for it, and so do their pre-deploy hooks.

## What it cannot do

- It verifies before promotion, not at deploy time in the target cluster. To enforce signatures
  on every Pod, whoever deploys it, add admission-time verification there:
  [Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/) or
  [Kyverno `verifyImages`](https://kyverno.io/docs/policy-types/cluster-policy/verify-images/).
  A change pushed to the GitOps repository outside kardinal is not verified by kardinal.
- A verified image is not checked again: a key revoked after the verification is not seen.
- Images that arrive through a config commit (a `configRef` whose files change an image) are not
  image-verified: only the Bundle's `spec.images` are. Use `commits.requireSigned` for config
  Bundles, and admission-time verification for images in config changes.
- Commit signatures are taken from the SCM's own verification (the keys its users registered),
  not checked against a key in the policy.

## Permissions

The controller reads the key, trusted-root and registry Secrets labelled
`kardinal.io/referenceable: "true"` (it already has `get` on Secrets) and writes
`imageverifications`; the Graph ServiceAccount creates them. See
[Graph coverage](graph-coverage.md).
