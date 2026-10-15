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
$ kardinal verify-receipt checkout prod --key receipt.pub
VERIFIED receipt checkout-1-5-0-prod
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
| `policy` | The Pipeline generation, and a digest of the policy set: the Pipeline's spec hash and each gate template's UID, generation and spec digest. A template's status writes do not change it. |
| `verificationResult` | Always `PASSED`: a receipt exists only for a promotion that landed. |
| `verifiedLevels` | What the record shows, and nothing more: `KARDINAL_GATES_PASSED` (every required gate passed), `KARDINAL_APPROVED` (at least one counted approval), `KARDINAL_HEALTHY` (the health check passed), `KARDINAL_OVERRIDDEN` (a gate passed by override). |
| `kardinalPromotion.version` | The Bundle: name, UID, version, type, images, config commit, provenance. |
| `kardinalPromotion.actor` | Who made the version and for whom ([the actor](actors.md#the-actor)); asserted fields stay under `asserted`. |
| `kardinalPromotion.gates` | Each required gate **as it was when the step started**: expression, result, reason, when it was evaluated, the override in effect (with its actor and whether it was verified), and its approvals with whether each counted. |
| `kardinalPromotion.checks` | The per-promotion MetricCheck results, the ImageVerification result per image, and the health check. |
| `kardinalPromotion.change` | The environment and the PR (URL, number, merge commit), or the commit pushed for `approval: auto`. |
| `kardinalPromotion.rollbackOf` | The version a rollback restores. |

### Gates are recorded when the step starts

When a step leaves Pending, it records the gates it just checked in `status.decision`, in the
same write that moves it to Promoting. The receipt uses that record, not the gates at Verified.
A schedule gate that passed at 10:00 and reads false at 10:20, when the health check ends, is
recorded as passed: that is what the promotion started on.

A step that started before receipts were enabled has no record. Its receipt reads the gates at
Verified and says so: `decisionSnapshot: false`, and `kardinal verify-receipt` prints "gates
read when the step was verified".

### Size

A receipt is at most 256 KiB. Past that, the approvals that did not count are dropped first
(counted in `approvalsOmitted`), then long gate texts are cut, then the gates after the first
and the metrics. `truncated: true` says so. The levels are those of the full record.

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
and `status.pendingReceipt` fields.

No promotion waits for a receipt, and no gate reads one. A receipt that cannot be signed or
stored is retried with backoff and counted in `kardinal_receipts_failed_total{sink}`.

### Signing modes

| Mode | What a verifier pins | When to use it |
|---|---|---|
| `keyless` (default) | The certificate identity `https://kubernetes.io/namespaces/<ns>/serviceaccounts/<sa>` **and** the cluster's OIDC issuer | EKS, GKE and AKS, whose issuers the public-good Fulcio accepts. No key to keep. The controller gets a short-lived certificate from Fulcio with a projected ServiceAccount token of audience `sigstore`, separate from its API server token. kind and on-prem clusters need a private Fulcio that trusts their issuer (`receipts.signing.keyless.fulcioURL`). Keyless needs proof of the signing time: set `receipts.sinks.rekor.url` or `receipts.signing.tsaURL`. |
| `key` | The public key | Air-gapped clusters. Anyone who can read the Secret can sign receipts; rotate it by replacing the Secret (the next receipt uses the new key). |
| `kms` | The public key | Key custody outside the cluster. The controller signs only with KMS providers its image was built with; v0.13's image has none yet (#1952), so a `kms` key reference is refused at start with the provider named. The sigstore CLI-plugin fallback, which runs a subprocess, is never used. |

The keyless certificate names the ServiceAccount, not the cluster: always pin the issuer too.

### Where receipts are stored

| Sink | Values | Notes |
|---|---|---|
| The PromotionReceipt (always) | — | `status.bundle` holds the signed receipt (a Sigstore bundle). Lives and dies with the cluster. |
| OCI referrer | `receipts.sinks.oci.enabled`, `receipts.sinks.oci.secretRef.name` | Pushed as a referrer of each image, artifact type `application/vnd.dev.sigstore.bundle.v0.3+json`, annotated `dev.sigstore.bundle.predicateType: https://slsa.dev/verification_summary/v1`. Needs push rights: a `kubernetes.io/dockerconfigjson` Secret in the release namespace, never a tenant's pull Secret. A config version has no image and is not pushed. |
| Rekor | `receipts.sinks.rekor.url` | Every signature is logged. The public-good Rekor (`rekor.sigstore.dev`) publishes each receipt's names, emails and pipeline names, so it needs `receipts.sinks.rekor.public: true` as well. A private Rekor is the enterprise path. |

ImageVerification skips receipt referrers before it counts an image's signatures, so an image
promoted to many environments does not crowd out its own signature
([Image verification](image-verification.md#signature-formats)).

## The PromotionReceipt

```console
$ kubectl get promotionreceipts -n payments
NAME                  PIPELINE   ENV    BUNDLE           PHASE    AGE
checkout-1-5-0-prod   checkout   prod   checkout-1-5-0   Stored   2m
```

- **Name**: `<bundle>-<environment>`, so re-running the Verified transition finds it.
- **Phase**: `Pending` (not signed yet), `Signed` (signed; a sink is not written yet), `Stored`
  (every sink holds it), `Failed` (its statement does not match `spec.statementDigest`: it was
  changed, and is never signed).
- **Conditions**: `Signed`, `Stored` and `PRCommented`, with reasons naming a failing sink.
- **No owner**: a receipt outlives its Bundle, the Graph and the step. Deleting a Bundle does
  not delete its receipts. kardinal never deletes receipts; delete them yourself when you no
  longer need them (the sinks outside the cluster keep their copy).
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

# From the image's OCI referrers, in any cluster or none
kardinal verify-receipt --image ghcr.io/acme/checkout@sha256:9f2c... --env prod --key receipt.pub

# From a file
kubectl get promotionreceipt checkout-1-5-0-prod -o jsonpath='{.status.bundle}' > receipt.sigstore.json
kardinal verify-receipt --file receipt.sigstore.json --key receipt.pub \
  --require GATES_PASSED,APPROVED,!OVERRIDDEN
```

It checks, and exits non-zero when any check fails:

1. **The signature**: with `--key`, or for a keyless receipt the certificate chain against the
   trusted root (`--trusted-root`, default the public-good root) and the exact `--identity` and
   `--issuer`. Both are required: no default trusts any identity.
2. **The log entry or timestamp**, when the receipt has one, against the trusted root. It never
   contacts Rekor: the entry and its inclusion proof are in the receipt.
3. **The subject**: the `--image` digest, when given.
4. **The summary**: `PASSED`, for the environment asked, with each `--require`d level present
   (`!LEVEL` absent).
5. **From the cluster**: the PromotionReceipt's statement is the signed one, byte for byte, and
   the Bundle it names, when it still exists, has the UID it records. A receipt edited after it
   was signed fails.

`-o json` prints the verified statement.

## Where receipts show

- **The PR**: when a receipt is signed, the controller comments once on the promotion's PR:
  "Promotion receipt: `checkout-1-5-0-prod`, signed by …, stored in …. Verify: `kardinal
  verify-receipt checkout prod --bundle checkout-1-5-0`". The receipt records the PR too, so
  the two point at each other. A comment that keeps failing is given up after 10 attempts
  (condition `PRCommented=False`, reason `GaveUp`).
- **`kardinal history`** adds a RECEIPT column once a promotion has one: "verified" when it is
  signed.
- **`kardinal why`** names the receipt of the version an environment runs, with the command to
  verify it.

Environments with `approval: auto` have no PR: their receipts show in `history` and `why`.

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
