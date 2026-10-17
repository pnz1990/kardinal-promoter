# Signed promotion receipts

!!! note "v0.13, off by default"
    Receipts are new in v0.13 and off until you set `receipts.enabled`. Enabling them creates
    receipts for promotions that reach Verified from then on; earlier promotions get none.

A promotion receipt is a signed record of one promotion. It says what version was promoted to
which environment, who made it and for whom, which gates it passed, who approved it, what the
health check said, and which pull request carried it. The controller signs it with Sigstore and
keeps it in the cluster. It can also push it next to the image in your registry and log it in
Rekor, so anyone can check it without access to the cluster.

```text
$ kardinal verify-receipt checkout prod --key receipt.pub --require HEALTHY,!OVERRIDDEN
receipt checkout-1-5-0-prod: every check below that was made passed
  ✓ signature:        key sha256:6f1e9c2a4b8d0e37
  – transparency log: no entry
  ✓ statement:        the cluster's spec.statement, sha256:9d3a...
  – subject digest:   not pinned (give --image or --subject)
  ✓ resource:         kardinal://1f0c.../namespaces/payments/pipelines/checkout/environments/prod (namespace, pipeline, environment pinned)
  ✓ levels:           HEALTHY, !OVERRIDDEN as required
  ✓ bundle:           checkout-1-5-0 is the Bundle the receipt was made for (UID 1b2c...)
record:
  environment: prod (pipeline checkout)
  version:     checkout-1-5-0 (1.5.0)
  actor:       agent fixer for alice@example.com; asserted: model claude-opus-5
  signed by:   key
  change:      https://github.com/acme/deploy/pull/412
  gate checkout-1-5-0-prod-approve: pass (approved: 1 of 1 (version 1.5.0))
  approved by: carol@example.com
  levels:      KARDINAL_GATES_PASSED, KARDINAL_APPROVED, KARDINAL_HEALTHY
  policy:      sha256:4be1...
```

The design, with the options weighed for each choice, is
[docs/design/21-receipts.md](design/21-receipts.md).

## What a receipt records

kardinal creates one receipt per PromotionStep that reaches **Verified**: a promotion or a
rollback. A step that failed or was superseded changed nothing and gets none; its AuditEvents
record it.

The receipt is an [in-toto](https://github.com/in-toto/attestation) statement with a
[SLSA verification summary](https://slsa.dev/spec/v1.0/verification_summary) predicate
(`https://slsa.dev/verification_summary/v1`). Tools that read verification summaries (Kyverno,
the sigstore policy-controller, slsa-verifier) read its core. kardinal adds the full record in
the extension field `kardinalPromotion`, which those tools ignore.

| Part | Content |
|---|---|
| `subject` | Each image digest (`sha256`), and the config commit (`gitCommit`) of a config or mixed version. A version with neither (images by tag only) is named by its Bundle spec's digest. |
| `resourceUri` | `kardinal://<cluster>/namespaces/<ns>/pipelines/<pipeline>/environments/<env>`. The cluster is the `kube-system` Namespace UID (or `receipts.clusterID`), so two clusters' `prod` never match. |
| `policy` | The Pipeline generation, and a digest of the policy set: the Pipeline's spec hash and each gate template's UID, generation and spec digest, as they were when the version's Graph was built (the gate instance carries them, `kardinal.io/gate-template-{uid,generation,digest}`): a template edited later is not what the gate evaluated, so the receipt does not name it. A template's status writes do not change it. |
| `verificationResult` | Always `PASSED`: a receipt exists only for a promotion that landed. |
| `verifiedLevels` | What the record shows, and nothing more: `KARDINAL_GATES_PASSED` (every required gate passed), `KARDINAL_APPROVED` (at least one counted approval), `KARDINAL_VERIFIED_BY_AGENT` (a verify gate counted a passing Verdict), `KARDINAL_HEALTHY` (the health check passed), `KARDINAL_OVERRIDDEN` (a gate passed by override), `KARDINAL_FREEZE_EXEMPTED` (the promotion went on through a freeze under an exemption), and `SLSA_BUILD_LEVEL_<n>` (the version's SLSA build level, 1 to 3: see `kardinalPromotion.checks`). A verifier usually requires the overridden and freeze-exempted levels absent: `--require GATES_PASSED,!OVERRIDDEN,!FREEZE_EXEMPTED`. |
| `kardinalPromotion.version` | The Bundle: name, UID, version, type, images, config commit, provenance. |
| `kardinalPromotion.actor` | Who made the version and for whom ([the actor](actors.md#the-actor)); asserted fields stay under `asserted`. |
| `kardinalPromotion.gates` | Each required gate **as it was when the step started**: expression, result, reason, when it was evaluated, the override in effect (with its actor and whether it was verified), its approvals (with their structured [fields](approvals.md#approval-fields)) and, for a verify gate, its Verdicts, with whether each counted. |
| `kardinalPromotion.changeRequests` | Each [change request](change-requests.md) ticket of the version in the environment, **as it was when the step started**: template, instance, provider, ticket key and URL, state and the provider's state, whether the ticket is the promotion's (`verified`, and `verifiedReason`), approvers, `timedOut`, and when it was read (`observedAt`) and until when that reading counted (`validUntil`). Left out when there is none. |
| `kardinalPromotion.checks` | The per-promotion MetricCheck results, the ImageVerification result per image, and the health check. `checks.slsa` is the version's SLSA build level from its newest Verified ImageVerification ([attestations](image-verification.md#attestations)) when its policy decides one (`slsa`): recorded image by image (`checks.slsa.images`): an image with a verified attestation that decides a level has `status: verified` and its `buildLevel` (the highest of those), and every other image `status: none`, since an attestation that did not verify never counts. The images are every digest-pinned image of the Bundle (each is a subject of the receipt, and a VSA's `verifiedLevels` hold for every subject), including one the Pipeline's `imageVerification.images` does not select: that one is never checked, so it is `none` and the version claims no level. A subject that is not an image, a `mixed` or `config` version's config commit, is listed in `checks.slsa.otherSubjects` as `none`: no SLSA build attestation kardinal verifies covers a commit, so such a version claims no level either. The version's `buildLevel` is the lowest of its images', as `attestations.<name>.slsa.buildLevel` folds them for a gate, and 0 when an image is `none`. `levelSource` (`builder` or `vsa`) is kept only when every image's level was decided the same way. A level of 1 or more is claimed as `SLSA_BUILD_LEVEL_<n>`, so a version with an unchecked image never claims one. |
| `kardinalPromotion.change` | The environment and the PR (URL, number, merge commit), or the commit pushed for `approval: auto`. For a PR the step adopted after a restart rather than opened ([PR evidence](pr-evidence.md#commit-messages)), `pr.adopted` holds its head commit (`headSHA`), the SCM account that opened it (`openedBy`), and each commit kardinal did not push with its author (`unpushedCommits`, at most 20; `unpushedCount` is how many there are). Names have no e-mail address, bidi or zero-width characters and are at most 128 bytes; a commit ID that is not hex is `(invalid)`. A receipt of a PR kardinal opened has no `adopted` key and is byte for byte what it was before. |
| `kardinalPromotion.rollbackOf` | The version a rollback restores. |
| `kardinalPromotion.freezeExemptions` | Each freeze the promotion went on through under an exemption ([`kardinal override --freeze`](agentic/freezes.md#let-one-change-through)): the freeze and whom it held, who made the exemption (with their actor stamp), why, its environment or version when it named one, and until when it counted. The step records them as it goes on (`status.freezeExemptions`). They cover the receipt's own step: a step deleted and created again (a retry) starts a new record. A step let through by v0.12, which did not record them, has `freezeExemptionsUnknown: true` instead (when the freeze is still on at its next git step, the record is written then), and its receipt claims `KARDINAL_FREEZE_EXEMPTED` all the same, so `--require '!FREEZE_EXEMPTED'` refuses it. |

### Gates are recorded when the step starts

When a step leaves Pending, it records the gates it just checked, and its change request
tickets, in `status.decision`, in the same write that moves it to Promoting. The receipt uses that record, not the gates at Verified.
A schedule gate that passed at 10:00 and reads false at 10:20, when the health check ends, is
recorded as passed: that is what the promotion started on.

The record is bounded (128 KiB): it leaves out what a receipt never shows (an approval's
comment, a Verdict's summary), and a step with many heavy gates keeps the approvals and Verdicts
that counted, cuts long texts to 1 KiB and drops the change requests' approvers (counted in
`approversOmitted`), and then leaves out the last gates (`omittedGates`); the
receipt says so (`truncated: true`, the gates left out in `omittedGateNames`, and `truncated` on
each gate it cut), and its policy digest still covers every gate. The receipt's levels are
decided over every gate before any cut: a gate passed by an override and left out still makes
it `KARDINAL_OVERRIDDEN`. A single gate too large for the record even cut (101 counted approvals
and 101 counted Verdicts with long reasons) means the step starts without a record, and gets no
receipt (`status.receiptError`), rather than waiting.

A step that started before receipts were enabled has no record. Its receipt reads the gates at
Verified and says so: `decisionSnapshot: false`, and `kardinal verify-receipt` prints "gates
read when the step was verified".

### Size

A receipt is at most 256 KiB. Past that, the approvals and Verdicts that did not count are dropped
first (counted in `approvalsOmitted` and `verdictsOmitted`), then long gate texts are cut and the change requests' approvers dropped (`approversOmitted`), then the gates after the first
and the metrics. `truncated: true` says so. The levels are those of the full record.

How much receipts take: each PromotionReceipt has its size in `status.bytes`, each Pipeline
environment its receipts' count and total size in `status.environments[].receipts`
(`{count, bytes}`), and the controller counts the receipts it signs, `kardinal_receipts_total{mode}`,
and their bytes, `kardinal_receipt_bytes_total`. Receipts past `receipts.retention.maxAge` are
deleted; without it every receipt is kept, and `kardinal doctor` warns once they pass 1 GiB (they
share etcd's quota, 8 GB by default, with every other object).

### Credentials are redacted

A receipt is signed, and the Rekor sink publishes it for good: a signed record cannot be redacted
afterwards without breaking its signature. So before the statement is signed, every string of
`kardinalPromotion` and every subject name is redacted:

- the user info of a URL: everything up to the last `@` before the first `/` after `://`, so a
  password with a raw `#`, `?` or space goes too;
- the value of a credential query or fragment parameter, up to the next `&`, `;`, `#` or space,
  which becomes `REDACTED`. A credential parameter is one the [Provenance](ci-integration.md#provenance)
  check refuses: its normalized name ends in `token`, `secret`, `key`, `signature`, `password`,
  `passphrase`, `session`, `ticket`, `bearer`, ... (except harmless names such as `monkey` or
  `sortkey`), or is `sig`, `auth`, `authorization`, `jwt`, `code`, `pass`, `pwd` or `sid`. A
  percent-encoded name and an array name (`token[]`) count too;
- a token known by its shape, wherever it is: GitHub (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`,
  `github_pat_`), GitLab (`glpat-`) and Slack (`xox?-`) tokens, and AWS access key IDs (`AKIA`,
  `ASIA`).

The JSON paths of the strings that changed are listed in `kardinalPromotion.redacted`, for example
`kardinalPromotion.version.provenance.ciRunURL`. The Bundle is not changed.

What is not detected: a credential of another shape outside a URL parameter, for example a bare
password in `provenance.author` or in a gate's reason. Don't put credentials in a version's
provenance or in gate texts.

The Bundle API, `kardinal create bundle` and the Bundle CRD refuse a credential in `ciRunURL` in the
first place. Redaction covers what gets past them: a version stored before the check, and text
elsewhere in the record.

## Turn receipts on

```yaml
# values.yaml
receipts:
  enabled: true
  signing:
    mode: key            # keyless (default), key or kms
    key:
      secretRef:
        name: receipt-key
```

```bash
cosign generate-key-pair      # or any ECDSA P-256 key, unencrypted PEM
kubectl -n kardinal-system create secret generic receipt-key --from-file=cosign.key=cosign.key
```

`kubectl apply` the new CRD before you upgrade the chart, as for every release
([Upgrading](upgrade.md)): `promotionreceipts`, and the PromotionStep's new `status.decision`
`status.pendingReceipt` and `status.receiptError` fields.

No promotion waits for a receipt, and no gate reads one. A receipt that cannot be signed or
stored is retried with backoff and counted in `kardinal_receipts_failed_total{sink}`.
A receipt that can never be built (its statement is over the 256 KiB cap after every cut, or
the version's Bundle or Pipeline is gone) does not hold the step either: the step is Verified
without a PromotionReceipt, its `status.receiptError` says why, a `ReceiptFailed` AuditEvent
records it, and `kardinal_receipts_failed_total{sink="build"}` counts it once.

### Signing modes

| Mode | What a verifier pins | When to use it |
|---|---|---|
| `keyless` (default) | The certificate identity `https://kubernetes.io/namespaces/<ns>/serviceaccounts/<sa>` **and** the cluster's OIDC issuer | EKS, GKE and AKS, whose issuers the public-good Fulcio accepts. No key to keep. The controller gets a short-lived certificate from Fulcio with a projected ServiceAccount token of audience `sigstore`, separate from its API server token. kind and on-prem clusters need a private Fulcio that trusts their issuer (`receipts.signing.keyless.fulcioURL`). Keyless needs proof of the signing time: set `receipts.sinks.rekor.url` or `receipts.signing.tsaURL`. |
| `key` | The public key | Air-gapped clusters. Anyone who can read the Secret can sign receipts; rotate it by replacing the Secret (the next receipt uses the new key). |
| `kms` | The public key | Key custody outside the cluster: AWS KMS or Google Cloud KMS (`receipts.signing.kmsKey`, below). The private key never leaves the KMS. |

The keyless certificate names the ServiceAccount, not the cluster: always pin the issuer too.

#### KMS keys

The controller image is built with two KMS providers (Go build tags `kms_aws` and `kms_gcp`). A
key reference for any other provider is refused at start, naming the provider and the ones built
in. kardinal calls the provider itself and never sigstore's CLI-plugin fallback, which runs a
subprocess.

| Provider | `receipts.signing.kmsKey` | Key | Credentials |
|---|---|---|---|
| AWS KMS | `awskms:///alias/kardinal-receipts`, `awskms:///arn:aws:kms:<region>:<account>:key/<id>`, or with an endpoint, `awskms://<host>:<port>/<id>` | Asymmetric, `SIGN_VERIFY`, `ECC_NIST_P256` | The SDK's default chain: IRSA (`serviceAccount.annotations`, `eks.amazonaws.com/role-arn`), EKS Pod Identity, or the node's instance role. The role needs `kms:Sign`, `kms:GetPublicKey` and `kms:DescribeKey` on the key ([policy below](#kms-permissions)). Prefer the key ARN to an alias: whoever may `kms:UpdateAlias` re-points an alias to another key. An endpoint that is a credential address (instance metadata, ECS, Pod Identity) is refused. The region comes from the ARN, or `AWS_REGION`. |
| Google Cloud KMS | `gcpkms://projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>/cryptoKeyVersions/<v>`. The version is required: a reference without one is refused | Asymmetric sign, `EC_SIGN_P256_SHA256` | Application Default Credentials: GKE Workload Identity, or `GOOGLE_APPLICATION_CREDENTIALS`. The identity needs four permissions on the key ([below](#kms-permissions)); `roles/cloudkms.signerVerifier` alone cannot sign. |

<a id="kms-permissions"></a>
- **Permissions.** AWS: a policy on the controller's role, scoped to the key ARN (an alias
  reference still needs the key ARN as the resource; DescribeKey is required, as the signer
  reads the key's spec):

  ```json
  {
    "Version": "2012-10-17",
    "Statement": [{
      "Effect": "Allow",
      "Action": ["kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"],
      "Resource": "arn:aws:kms:<region>:<account>:key/<id>"
    }]
  }
  ```

  Google Cloud: a custom role on the key with exactly `cloudkms.cryptoKeys.get`,
  `cloudkms.cryptoKeyVersions.get`, `cloudkms.cryptoKeyVersions.useToSign` and
  `cloudkms.cryptoKeyVersions.viewPublicKey` (the signer reads the key and its version before
  it signs), or the predefined `roles/cloudkms.signer`, `roles/cloudkms.publicKeyViewer` and
  `roles/cloudkms.viewer` together:

  ```sh
  gcloud iam roles create kardinalReceiptSigner --project <p> \
    --permissions cloudkms.cryptoKeys.get,cloudkms.cryptoKeyVersions.get,cloudkms.cryptoKeyVersions.useToSign,cloudkms.cryptoKeyVersions.viewPublicKey
  gcloud kms keys add-iam-policy-binding <k> --keyring <r> --location <l> --project <p> \
    --member serviceAccount:<gsa> --role projects/<p>/roles/kardinalReceiptSigner
  ```
- **Egress.** The KMS API calls (and AWS STS) go through the controller's egress guard and
  allowlist like every sink: with `egress.allowlist` set, add `kms.<region>.amazonaws.com` and
  `sts.<region>.amazonaws.com`, or `cloudkms.googleapis.com`. The credential requests to the
  instance metadata, ECS and Pod Identity endpoints (and Google's metadata server) are the
  cloud SDK's own, not user input, and are not guarded.
- **Rotation.** A receipt records the key reference it was signed with (`status.signedBy`); a
  verifier pins the public key. A Cloud KMS reference must pin its key version: without one the
  provider would sign with the newest enabled version, so whoever may add a version would change
  the signing key. Rotating is a configuration change: set the new version's reference and give
  verifiers its public key. The key is loaded once, at the first signature, and its client is
  reused until the controller stops. A key disabled in the KMS fails signing (`Signed=False`, `SignFailed`), retried
  with backoff and counted in `kardinal_receipts_failed_total{sink="signing"}`; promotions are
  not held.
- **Timeouts.** Each KMS call is bounded by the signer's timeout (30 s); an endpoint the egress
  guard refuses fails at once, naming the address.

### Where receipts are stored

| Sink | Values | Notes |
|---|---|---|
| The PromotionReceipt (always) | — | `status.bundle` holds the signed receipt (a Sigstore bundle). Lives and dies with the cluster. |
| OCI referrer | `receipts.sinks.oci.enabled`, `receipts.sinks.oci.secretRef.name` | Pushed as a referrer of each image, artifact type `application/vnd.dev.sigstore.bundle.v0.3+json`, annotated `dev.sigstore.bundle.predicateType: https://slsa.dev/verification_summary/v1`, `kardinal.io/receipt: "true"` (a reader of attestations tells a receipt from a builder's own VSA by it; `verify-receipt` reads only those) and `kardinal.io/receipt-resource` (its `resourceUri`, so `verify-receipt --image` reads only the environment it is asked about). Needs push rights: a `kubernetes.io/dockerconfigjson` Secret in the release namespace, never a tenant's pull Secret. A config version has no image and is not pushed. |
| Rekor | `receipts.sinks.rekor.url`, `receipts.sinks.rekor.version` (1 or 2) | Every signature is logged: in Rekor v1 (`rekor.sigstore.dev`), or with version 2 in a Rekor v2 (rekor-tiles) log, such as the public `log20xx` shards or sigstore scaffolding's, where a receipt is a hashedrekord entry. A Rekor v2 entry proves inclusion but not the signing time, so keyless signing with version 2 needs `receipts.signing.tsaURL` too: the chart and the controller refuse it without one (a receipt that could never verify once its short-lived certificate expired is never marked Signed). A key or KMS receipt in Rekor v2 needs no timestamp authority. The public-good Rekor (`rekor.sigstore.dev`) publishes each receipt's names, emails and pipeline names, so it needs `receipts.sinks.rekor.public: true` as well. A private Rekor is the enterprise path. |

The OCI sink pushes with the controller's Secret, but a Bundle's image repositories are chosen
by whoever creates the Bundle. In a shared install, bound where it writes: set
`receipts.sinks.oci.repository` (every referrer goes to one repository you own), or
`receipts.sinks.oci.allowedRepositories` (a registry, or a registry and path prefix:
`ghcr.io/acme/`, `docker.io/acme`). An image's repository is parsed as the registry sees it,
and matched by registry and whole path components: `docker.io/acme` and `acme` are one, a
repository with a `.`, `..` or empty path component (`ghcr.io/acme/../other`) is never
allowed. An image outside the list gets no referrer: its receipt is still signed and kept, and its
`Stored` condition is `False` with reason `RepositoryNotAllowed`, naming the repository. Left
empty, the sink pushes to every image's repository its Secret reaches.

ImageVerification skips receipt referrers before it counts an image's signatures, so an image
promoted to many environments does not crowd out its own signature
([Image verification](image-verification.md#signature-formats)).

## The PromotionReceipt

```console
$ kubectl get promotionreceipts -n payments
NAME                  PIPELINE   ENV    BUNDLE           PHASE    AGE
checkout-1-5-0-prod   checkout   prod   checkout-1-5-0   Stored   2m
```

- **Name**: `<bundle>-<environment>-<hash>`, the hash of the Bundle, the environment and the
  step's UID, so no two promotions share one; the step records it in `status.receipt`, and
  the receipt is labelled with the step's UID (`kardinal.io/promotion-step-uid`). Re-running the
  Verified transition finds it; a receipt of that name that is not the step's is never adopted
  (`status.receiptError`).
- **Phase**: `Pending` (not signed yet), `Signed` (signed; a sink is not written yet), `Stored`
  (every sink holds it), `Failed` (its statement does not match `spec.statementDigest`: it was
  changed, and is never signed).
- **Conditions**: `Signed`, `Stored` and `PRCommented`, with reasons naming a failing sink.
- **Signed once**: a status write that meets a conflict after signing re-reads the receipt and
  records the same signature, rather than signing again (a second certificate and, with Rekor,
  a second log entry). A controller that crashes between signing and its status write signs
  again on restart: both signatures verify.
- **No owner**: a receipt outlives its Bundle, the Graph and the step. Deleting a Bundle does
  not delete its receipts. kardinal keeps every receipt unless you set
  `receipts.retention.maxAge` (for example `8760h`): the leader then deletes the receipts
  created longer ago, at most 2,000 an hour. The sinks outside the cluster keep their copy
  whatever it says.
- **Size**: a receipt is typically a few KB. Its statement is capped at 256 KiB, and the signed
  bundle repeats it in base64, so one receipt is at most about 600 KiB in etcd: 10,000 receipts
  of that worst case would be about 6 GB, against etcd's default 8 GB quota. If your gates
  carry very large expressions or approval reasons, set `receipts.retention.maxAge` and keep
  the long-term copy in the OCI or Rekor sink. The controller caches a receipt it is done with
  (stored and commented, or failed) without its statement and bundle, so its memory does not
  grow with them.
- **Writes**: `spec` is immutable and `status.bundle` cannot change once set (the CRD refuses
  both). The `<release>-receipts` admission policy refuses every create, update and delete of a
  PromotionReceipt by anyone but the controller, a cluster-admin included; the namespace
  controller and the garbage collector may delete them. User roles can read them.

## Verify a receipt

```bash
# From the cluster: the newest receipt of the environment, or --bundle
kardinal verify-receipt checkout prod --key receipt.pub
kardinal verify-receipt checkout prod --bundle checkout-1-5-0 \
  --identity https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter \
  --issuer https://oidc.eks.us-east-2.amazonaws.com/id/EXAMPLE

# From the image's OCI referrers, in any cluster or none: pin the namespace, the Pipeline,
# the environment and the cluster (receipts.clusterID, or its kube-system Namespace UID)
CLUSTER=$(kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')
kardinal verify-receipt --image ghcr.io/acme/checkout@sha256:9f2c... \
  -n payments --pipeline checkout --env prod --cluster "$CLUSTER" --key receipt.pub

# From a file: pin the subject too
kubectl get promotionstep <step> -o jsonpath='{.status.receipt}'   # the receipt's name
kubectl get promotionreceipt <receipt> -o jsonpath='{.status.bundle}' > receipt.sigstore.json
kardinal verify-receipt --file receipt.sigstore.json -n payments --pipeline checkout --env prod \
  --cluster "$CLUSTER" --subject sha256:9f2c... --key receipt.pub \
  --require GATES_PASSED,APPROVED,!OVERRIDDEN
```

It checks, and exits non-zero when any check fails:

1. **The signature**: with `--key`, or for a keyless receipt the certificate chain against the
   trusted root (`--trusted-root`, default the public-good root) and the exact `--identity` and
   `--issuer`. Both are required: no default trusts any identity.
2. **The log entry or timestamp**, when the receipt has one, against the trusted root. It never
   contacts Rekor: the entry and its inclusion proof are in the receipt. A key receipt verified
   without `--trusted-root` does not check its log entry, and says so ("entry not checked";
   `logged: false, logNotChecked: true` with `-o json`): it is in the transparency log only once
   the entry is verified (`✓ transparency log: inclusion verified (logIndex N, log ...)`).
3. **What it is for**: the cluster, namespace, Pipeline and environment of its `resourceUri`,
   exactly, and the subject. From the cluster these are the receipt's own; with `--image` and
   `--file`, `-n`, `--pipeline` and `--env` are required, `--subject` too for a file (the
   `--image` digest is the subject), and `--cluster` whenever the receipt names a cluster (its
   `kube-system` Namespace UID, or `receipts.clusterID`); `--any-cluster` accepts any cluster's,
   with a warning. A receipt of another tenant's Pipeline, or another cluster's, signed with the
   same key for the same image, is refused.
4. **The summary**: `PASSED`, with each `--require`d level present (`!LEVEL` absent). A SLSA level is the highest of its track, as a verification summary lists it: `SLSA_BUILD_LEVEL_3` satisfies `--require SLSA_BUILD_LEVEL_2`, and `!SLSA_BUILD_LEVEL_3` means a build level below 3.
5. **From the cluster**: the PromotionReceipt's statement is the signed one, byte for byte, and
   the Bundle it names, when it still exists, has the UID it records. A receipt edited after it
   was signed fails.

Each check is a line of its own, `✓` when it was made and passed, `–` when it was not made (a
subject not pinned, a log entry not checked, no level required): the output never says just
"verified". `-o json` prints the same list (`checks`, each with `name`, `checked` and `detail`)
and the statement.

**With `--image`**, verify-receipt reads the image's referrers, following the registry's pages
(the Referrers API's `Link: rel="next"`, on the same registry only; or the referrers tag schema),
up to 1024 of them. Each receipt referrer carries its `resourceUri` as an annotation
(`kardinal.io/receipt-resource`, not signed: it only narrows the search, and the receipt read is
verified), so another environment's receipts are skipped before they are read and cannot crowd
out the one asked for. An image with more referrers than are read gets a warning, and
`referrersTruncated: true` with `-o json`; if no receipt of the pinned resource is among those
read, verify-receipt fails: whether one exists is unknown, which is never a pass.

## Where receipts show

- **The PR**: when a receipt is signed, the controller comments once on the promotion's PR:
  "Promotion receipt: `checkout-1-5-0-prod`, signed by …, stored in …. Verify: `kardinal
  verify-receipt checkout prod --bundle checkout-1-5-0`". The receipt records the PR too, so
  the two point at each other. A comment that keeps failing is given up after 10 attempts
  (condition `PRCommented=False`, reason `GaveUp`). The comment ends with a hidden marker,
  `<!-- kardinal-receipt:<namespace>/<receipt> -->`: the controller searches the PR's comments
  for it before posting (GitHub, GitLab, Forgejo/Gitea, Bitbucket Cloud and Data Center,
  Azure DevOps), so a post whose status write was lost (a crash) is not posted again. Only a
  comment by kardinal's own SCM identity counts (the token's user; a GitHub App's comments by the
  App), so anyone who may comment on the PR cannot keep the receipt's comment from being posted
  by posting the marker first. The search reads at most 20 pages of comments, oldest first.
  A token that may not read its own user (a Forgejo/Gitea token with only the
  `write:repository` and `write:issue` scopes, a Bitbucket token without `account`) is not
  searched: the comment is posted, and a crash between the post and its status write can post
  it twice. Add `read:user` (Forgejo/Gitea) or `account` (Bitbucket) to search.
- **`kardinal history`** adds a RECEIPT column once a promotion has one: "signed" once the
  controller signed it. "Verified" is only what `kardinal verify-receipt` says of a receipt it
  checked.
- **`kardinal why`** names the receipt of the version an environment runs, with the command to
  verify it.
- **The UI**: the stage drawer's History says "Receipt <name>: signed ✓" with the command to
  verify it once the step's receipt is signed.

Environments with `approval: auto` have no PR: their receipts show in `history`, `why` and the UI.

## What a receipt does not prove

- **The controller signs what it decides.** A receipt is as true as the controller. An admin
  who can mint the controller's ServiceAccount tokens, or read the key Secret, can sign
  anything. The signature makes the record tamper-evident once it leaves the cluster, and says
  who stands behind it.
- **Asserted fields stay asserted.** The model and session in the actor are what the client
  said about itself.
- **Nothing after Verified.** A revert outside kardinal, or drift, is not in the receipt.
- **A deleted PromotionReceipt with no other sink is gone.** Use the OCI or Rekor sink for
  evidence you must keep.
