# Image Signature Verification

A Pipeline can require every version's images to be signed, and a config version's commit to be
signed, before the version is promoted anywhere. kardinal checks the signatures in the registry
with [sigstore-go](https://github.com/sigstore/sigstore-go): a key (`cosign sign --key`) or a
Sigstore keyless identity (`cosign sign` from CI with OIDC). A version that does not pass is never
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
| `images[]` | all | Repository patterns of the version images to verify; `*` matches any characters. Patterns and version images are compared in normalized form: the registry host lowercased and without `:443`, `docker.io` for Docker Hub (`index.docker.io`, `registry-1.docker.io` or no host) and `library/` for official images (`nginx` is `docker.io/library/nginx`). A version image that does not parse as a reference fails the version. |
| `authorities[]` | | Accepted signers, up to 10. An image is verified when one of its signatures verifies against any one authority. Each has a `name` and exactly one of `key` and `keyless`. |
| `authorities[].key.secretRef` | | `{name, key}`: a Secret in the Pipeline namespace with the PEM public key (`cosign.pub`). |
| `authorities[].key.requireTransparencyLog` | `false` | Require a Rekor entry, checked against the Sigstore public-good log. Without it a key signature is checked against the key alone (`cosign verify --insecure-ignore-tlog`). |
| `authorities[].keyless.issuer` | | OIDC issuer of the signing certificate. |
| `authorities[].keyless.subject` / `subjectRegExp` | | The certificate's subject (exactly one of the two). `subjectRegExp` must match the whole subject: it is anchored as `^(?:...)$`. |
| `authorities[].keyless.trustedRootRef` | public good | `{name, key}`: a Secret with a Sigstore `trusted_root.json`, for a private Sigstore. Without it the controller fetches the public-good trusted root through TUF (from `tuf-repo-cdn.sigstore.dev`) on first use and again once a day; a failed refresh keeps the previous root and is retried after 5 minutes. Each TUF request times out after 20s. |
| `commits.requireSigned` | `false` | A config or mixed version's `configRef.commitSHA` must be signed with a signature the SCM verified (GitHub, GitLab, Forgejo and Gitea report it; Bitbucket and Azure DevOps do not, and such a version fails). See [Signed commits](#signed-commits). |
| `commits.allowedSigners[]` | any person | Also require the signer to be one of these SCM logins or emails. Platform signatures (`web-flow`, `gitlab-system`, `forgejo-instance`) are accepted only when listed. |
| `signatureRepository` | next to the image | Where the signatures are, when they are not next to the images (cosign's `COSIGN_REPOSITORY`). |
| `registrySecretRef.name` | anonymous | A `kubernetes.io/dockerconfigjson` Secret in the Pipeline namespace for private registries. |
| `insecureRegistries[]` | | Registry hosts read over plain HTTP. Every other registry is HTTPS only. |
| `timeout` | `10m` | How long a missing signature is waited for (CI may sign after it pushes). |
| `attestations[]` | | In-toto attestations each image must or may carry, up to 10: see [Attestations](#attestations). |

## Signature formats

- **Sigstore bundles attached as OCI referrers**: cosign v3's default, and cosign v2 with
  `--new-bundle-format`. Key and keyless. The bundle must be a cosign image signature: a DSSE
  in-toto statement with predicate type `https://sigstore.dev/cosign/sign/v1` whose subject is the
  image digest. Attestations (SBOM, provenance) and plain message signatures attached to the same
  image are not signatures of it, whoever signed them. Up to 32 signature bundles and 64 bundles
  of the [attestations](#attestations) the policy names are read per image, each against its own
  limit. A referrer whose `dev.sigstore.bundle.predicateType` annotation names a predicate the
  policy does not verify is skipped before it counts, and so are kardinal's own
  [promotion receipts](design/21-receipts.md) (annotation `kardinal.io/receipt`). Other attestations on
  the image therefore cannot crowd out its signatures or the attestations the policy reads.
  The referrers index itself, and every manifest, is read up to 4 MiB (tens of thousands of
  referrers); a larger one fails the read, and the image is not verified.
- **Legacy cosign signatures** in the `sha256-<digest>.sig` tag (cosign v2's default): key
  authorities only. A keyless signature must be a Sigstore bundle.

With `requireTransparencyLog`, the Rekor entry is checked offline from the bundle: its signed
entry timestamp and inclusion proof against the Rekor public key in the trusted root. The
controller does not query Rekor, so an entry that the log no longer serves is not noticed.

## How it runs

1. **Digests are required.** Verifying a tag and then promoting the tag would let a different
   image be pushed under the tag in between. When the policy selects an image that the version names
   by tag, the version fails before any environment, with condition `InvalidSpec` (reason
   `GraphBuildFailed`): `image ... is not pinned by digest`. A [Subscription](subscription.md)
   creates versions by digest; from CI, pass `--image repo@sha256:...`.
2. The Bundle's Graph creates one `ImageVerification` (`kubectl get imageverifications`) with the
   selected images and the policy. Its controller reads the signatures from the registry and the
   commit's signature from the SCM, and records a result per image in `status.images`.
3. The first environments (those with no upstream) wait in `Pending` with the message
   `waiting for image verification <name>: ...`, and so do their
   [pre-deploy hooks](hooks.md). Nothing is committed or deployed.
4. **Verified**: the environments start. Later environments need nothing more: their upstream
   already waited.
   **Failed**: the waiting steps fail, and the version with them. A signature that does not verify
   against an authority (another signer's, an attestation, one that is malformed) is not a
   verdict: an image can carry other signatures before CI's arrives, so the verification keeps
   waiting for one that verifies, and fails at the `timeout` (`status.reason:
   SignatureNotVerified`, with every reason in the message). It fails at once when a cosign
   signature made with a policy key names another digest, or when the commit is not signed as
   required. A registry or SCM outage is retried, never treated as verified, until the timeout.
   Each registry fetch times out after 45s and each check after 2 minutes; up to 4
   ImageVerifications are checked at once.
5. Before it promotes, the step compares the version's images with the images the
   ImageVerification verified (both normalized) and refuses to promote when a digest differs or a
   verified image is missing. The version's images cannot change anyway: the Bundle CRD refuses an
   update of `spec.images`.

`status.reason` is one of `SecretNotReferenceable`, `SecretNotFound`, `InvalidPublicKey`,
`InvalidTrustedRoot`, `InvalidRegistryCredentials`, `SignatureNotVerified`, `CommitNotVerified`,
`AttestationNotVerified`, `SLSALevelTooLow` and `Timeout`. A key, trusted root or `.dockerconfigjson` that does not parse is reported by Secret
and key name only; nothing of its content is copied to the status.

**Policy changes.** An ImageVerification's spec is immutable, and its name carries a hash of it.
Editing `spec.imageVerification` gives each version a new ImageVerification; the first
environments that have not started yet wait for the new verdict. Environments that already
started are not checked again.

**`update.strategy: argocd`** writes an image pinned by digest as `<tag>@<digest>` (see
[Argo CD native promotion](argocd-native-promotion.md#multi-image-bundles)), so the digest that
was verified is the one deployed. The kustomize and Helm strategies write the digest too.

## Attestations

A signature says who signed an image. Attestations say how it was built and what is in it: SLSA
provenance, a vulnerability scan with VEX statements, test results, or any other in-toto
predicate. With `attestations`, the ImageVerification also reads each image's attestations from
its OCI referrers, verifies them against the policy's authorities, and records what they say in
`status.attestations`. Gates then decide in CEL, per environment, over
[`attestations.*`](reference/cel-context.md#attestations).

```yaml
spec:
  imageVerification:
    authorities:
      - name: release
        keyless: {issuer: https://token.actions.githubusercontent.com,
                  subjectRegExp: '^https://github\.com/myorg/[^/]+/\.github/workflows/release\.yml@refs/tags/.*$'}
      - name: security
        key: {secretRef: {name: security-pub, key: cosign.pub}}
    attestations:
      - name: provenance                # gates read attestations.provenance
        predicateType: https://slsa.dev/provenance/v1
        authorities: [release]          # who may sign it
        required: true                  # missing at the timeout: Failed
        slsa:
          builders:                     # runDetails.builder.id -> the level you grant it
            - id: https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0
              level: 3
          minLevel: 2                   # below it at the timeout: Failed
      - name: vulns
        predicateType: https://cosign.sigstore.dev/attestation/vuln/v1
        authorities: [release]
        required: true
        vex:
          authorities: [security]       # who may say a finding does not apply
      - name: tests
        predicateType: https://in-toto.io/attestation/test-result/v0.1
        authorities: [release]
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: prod-supply-chain
  namespace: my-app
  labels: {kardinal.io/applies-to: prod}
spec:
  expression: >-
    attestations.provenance.slsa.buildLevel >= 3 &&
    attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA &&
    attestations.vulns.effective.critical == 0
  message: prod needs level-3 provenance from the version's commit and no critical vulnerability
```

Attach them with cosign, as Sigstore bundles in the image's referrers (cosign v3, or v2 with
`--new-bundle-format`):

```bash
cosign attest --new-bundle-format --type slsaprovenance1 --predicate provenance.json "$IMAGE"
cosign attest --new-bundle-format --type vuln --predicate trivy.json "$IMAGE"
cosign attest --new-bundle-format --key security.key --type openvex --predicate vex.json "$IMAGE"
```

`attestations[]`:

| Field | Default | Description |
|---|---|---|
| `name` | | Lower-case letters and digits; the gate reads `attestations.<name>`. |
| `predicateType` | | The in-toto predicate type, exactly. |
| `authorities[]` | | Names of `authorities` that may sign it. An attestation from any other signer is not used. |
| `required` | `false` | Hold the version until every image carries a verified attestation of the type; at the `timeout` it fails (`status.reason: AttestationNotVerified`). Without it the attestation is recorded when it is there, and holds nothing. |
| `slsa.builders[]` | | `{id, level}`: the SLSA build level (1 to 3) you grant a provenance builder (`runDetails.builder.id`, exactly). Provenance from a builder not listed is level 1. |
| `slsa.minLevel` | `0` | Hold the version while an image's level is below it; at the `timeout` it fails (`SLSALevelTooLow`). |
| `vex.authorities[]` | | Names of `authorities` whose OpenVEX attestations count against the scan's findings. The scan's own signer does not suppress its findings unless it is listed. |
| `vex.predicateType` | `https://openvex.dev/ns` | The predicate type of the VEX attestations. |
| `maxAge` | | How old the attestation may be, a Go duration (`24h`): see [Attestation age](#attestation-age). |

**What is checked.** An attestation counts when its bundle verifies against one of its
`authorities` (key, keyless, private trusted root, as for signatures), its in-toto statement's
subject is the image's digest, and its predicate type is the policy's. Of several, the newest by
its verified timestamp (else the last referrer) is used, and `count` says how many verified. A
bundle that does not verify is never used; its reason is in `message`.

**SLSA build level.** A build level is a property of the builder, not a field of the provenance:
the policy grants it in `slsa.builders`. A SLSA verification summary (VSA, predicate type
`https://slsa.dev/verification_summary/v1`) signed by one of the attestation's authorities, with
`verificationResult: PASSED` and a `resourceUri` that names the image's digest, raises the level to
the highest `SLSA_BUILD_LEVEL_n` it lists. `slsa.levelSource` says which decided (`builder` or
`vsa`). The provenance's source, the first `resolvedDependencies` entry with a `gitCommit` digest,
is recorded as `sourceCommit` and `sourceURI`.

**Vulnerabilities and VEX.** The scan formats read are cosign's vuln predicate
(`https://cosign.sigstore.dev/attestation/vuln/v1`, what `cosign attest --type vuln` writes) holding
a Trivy or Grype report, and in-toto vulns v0.2 (`https://in-toto.io/attestation/vulns`). A
finding is **effective** unless the latest OpenVEX statement for its ID (or an alias), and a product
that names the image (`<repository>@<digest>`, the digest, or its `pkg:oci` purl), says
`not_affected` or `fixed`; `affected` and `under_investigation` keep it. The status counts the
findings by severity (critical, high, medium, low, unknown) before and after VEX, and lists the IDs
of the effective critical and high ones (at most 50). To bound how old a scan may be, set its
`maxAge` ([Attestation age](#attestation-age)).

**Several images.** A version with several images gets one value per attestation name in
gates: verified when every image is, the lowest build level, the summed counts and the oldest
scan. The signer, digest, predicate, builder, source and scanner are set there only when every
image has the same one. Otherwise an expression that reads them fails, and the gate blocks.
`attestations.<name>.images` holds each image's own result, for checks one image at a time
([`attestations.*`](reference/cel-context.md#attestations)).

**Bounds.** Each attestation's predicate is kept as canonical JSON (sorted keys) of at most 64 KiB,
and a status holds at most 256 KiB of them; the largest are dropped first. A dropped predicate keeps
its summary, and `predicateOmitted` says why: an expression that reads it then fails, so the gate
blocks rather than passes. The predicate is the build's own text: a gate may compare it, as data.

**Timing.** Verified is terminal, as for signatures: an attestation added later, or a vulnerability
found later, is not seen by that version, except through `maxAge` below. An attestation not
required that is missing when the rest verified is recorded as not verified.

### Attestation age

A version can wait at a gate for days; without a bound, prod promotes it on the scan it was
verified with, however old. `maxAge` bounds it:

```yaml
attestations:
  - name: vulns
    predicateType: https://cosign.sigstore.dev/attestation/vuln/v1
    authorities: [release]
    maxAge: 24h
```

```yaml
# prod: a scan no older than its maxAge, with no critical finding after VEX
expression: '!attestations.vulns.stale && attestations.vulns.effective.critical == 0'
```

- **Age.** A scan's age counts from the earlier of its `scannedAt` and its verified signing time
  (the transparency log's or timestamp authority's time), so a scan cannot claim to be newer than
  its signature, and never from later than when kardinal first recorded it (`datedAt`): a scan
  with no signing time that claims a future `scannedAt` ages from when it was first seen, and
  reading the same attestation again keeps that date. Any other attestation counts from its
  signing time. One with no signing time and
  no scan time (a key signature without a transparency log entry) cannot be dated: it is stale at
  once.
- **Status.** Each result records `verifiedAt`, `datedAt`, `staleAt` (its age base plus `maxAge`) and
  `stale`. The reconciler comes back at `staleAt` and sets `stale`. The phase stays Verified, and a
  gate reads [`attestations.<name>.stale`](reference/cel-context.md#attestations), true when any
  image's result is stale.
- **A newer attestation.** While a result is stale, the image's referrers are read again every
  quarter of `maxAge`, at least every hour and at most every 5 minutes. The newest attestation that
  verifies and is not stale replaces the result, and the gates that read it are evaluated again.
  For a 1h `maxAge` that is at most 4 registry reads per hour per stale version; nothing is read
  before a result goes stale.
- **Required.** A required attestation that is already stale when the version is checked is
  waited for like a missing one, until the `timeout` (`status.reason: AttestationStale`).
- A new vulnerability published after the scan is still not seen until a new scan is attached:
  `maxAge` is what makes the scanner run again.

## Signed commits

With `commits.requireSigned`, the config version's `configRef.commitSHA` must be the full 40- or
64-character SHA (a short one fails the version). The commit is checked with the Pipeline's SCM
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
  versions, and admission-time verification for images in config changes.
- Commit signatures are taken from the SCM's own verification (the keys its users registered),
  not checked against a key in the policy.

## Permissions

The controller reads the key, trusted-root and registry Secrets labelled
`kardinal.io/referenceable: "true"` (it already has `get` on Secrets) and writes
`imageverifications`; the Graph ServiceAccount creates them. See
[Graph coverage](graph-coverage.md).
