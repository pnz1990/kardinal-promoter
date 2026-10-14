# Design: Signed promotion receipts

> Status: Proposed (spec for v0.13.0, AGENTIC-PLAN §4 "Signed promotion receipt").
> Created: 2026-10-14
> Depends on: [17-actors.md](17-actors.md) (the actor stamp), [18-actor-policy.md](18-actor-policy.md)
> (gate inputs, approvals by party), [19-verdicts.md](19-verdicts.md) (Verdicts), and the
> attestation verification of v0.13 (spec 20) for the SLSA level a receipt can claim.
> Verified against: this repository at `417c943`, `github.com/sigstore/sigstore-go` v1.3.0,
> `github.com/sigstore/sigstore` v1.10.8, `github.com/sigstore/fulcio` v1.9.0,
> `github.com/in-toto/attestation` v1.2.0 and `github.com/google/go-containerregistry` v0.22.1,
> all as in the Go module cache. Source references are `path:line` in those trees.

---

## 1. Problem

When a version reaches prod, kardinal knows exactly why it was allowed there:
- who created it, and for an agent, who sponsors the agent;
- which gates held it and what each one read;
- who approved it, which verifiers passed it, and which checks it passed;
- which policy was in force at the time.

That knowledge is spread over objects that change or go away:
- gate instances are re-evaluated;
- a Graph and its PromotionSteps are deleted when a Bundle is retired (ledger G15);
- Bundles are pruned by `historyLimit`;
- AuditEvents expire under retention.

The PR body is a snapshot, written once when the PR opens ([PR evidence](../pr-evidence.md#pr-body-updates)). Nothing kardinal writes is signed, so nothing can be checked outside the cluster that wrote it.

An auditor, a downstream admission controller, or an incident review needs one record per change that lands in an environment. It must be:
- complete;
- frozen at the moment of the decision;
- signed by kardinal;
- checkable without trusting the cluster.

This spec defines that record:
- **the receipt**: an in-toto attestation for each promotion, one per environment;
- **`PromotionReceipt`**: the CRD that carries the receipt and its signature;
- **who builds it, who signs it, and where it is stored**;
- **`kardinal verify-receipt`** and the PR evidence link.

## 2. What a receipt covers

One receipt per PromotionStep that reaches **Verified**, for a promotion or a rollback. A
Failed or Superseded step has no receipt: it changed nothing, and its AuditEvents record it.

| Part | Content | Source (frozen when) |
|------|---------|----------------------|
| Subject | Each image digest (`sha256`), or the config commit (`gitCommit`) for a config version; both for a mixed one | Bundle `spec.images[].digest`, `spec.configRef.commitSHA` |
| Version | Bundle name, UID, version string, type, provenance (`author`, `commitSHA`, `ciRunURL`) | Bundle spec (immutable) |
| Actor | The Bundle's actor stamp: kind, principal, agent, sponsor, via; asserted fields kept apart | `spec.actor` (17-actors §4) |
| Gates | Each required gate: instance and template, the template's generation and spec digest, expression, message, result, reason, `lastEvaluatedAt`, the override in effect (with its actor and verification), `approvalRequired` | The step's decision snapshot (§4.2), taken when the step left Pending |
| Approvals | Per approval gate: each decision with its actor, whether it counted and why not, and the counted parties | Gate `status.approvals`, same snapshot |
| Verdicts | Per verify gate: each counted Verdict: name, check, result, verifier actor, evidence URL, findings count | Gate `status.verdicts` (19-verdicts §6.5), same snapshot |
| Checks | Per-promotion MetricCheck results (value, result, `validUntil`); the ImageVerification result per image; the health adapter's result | MetricCheck, ImageVerification and step status, at Verified |
| Change | Environment, the PR (URL, number, merge commit, merged at), or the pushed commit for `approval: auto` | PRStatus and step status, at Verified |
| Policy versions | Pipeline generation and spec hash; each gate template's UID, generation and spec digest; controller version; chart version | Pipeline, gate templates, the `kardinal-version` ConfigMap, at the snapshot |
| Freeze | Each freeze exemption the step went on under (`FreezeExempted`, 18-actor-policy §9.2) | Step condition `FreezeExempted` |

Gates are frozen at the moment of decision, not when the receipt is built. A schedule gate that
passed at 10:00 may read false at 10:20 when the health check ends. The receipt must say what the
step started on.

## 3. The attestation

### 3.1 Predicate: options

| | Predicate | For | Against |
|-|-----------|-----|---------|
| (a) | SLSA VSA v1 (`https://slsa.dev/verification_summary/v1`) alone | The predicate policy engines and admission controllers already read: Kyverno, sigstore policy-controller, slsa-verifier. Its fields fit: a verifier, a resource, a policy, input attestations, a result and levels. | Holds a summary, not the record. No place for gates, approvers, actors or checks. |
| (b) | A kardinal predicate (`https://kardinal.io/attestation/promotion/v1`) | Exactly the record | No tool reads it; every consumer needs kardinal's code |
| (c) | VSA v1 with a kardinal extension field holding the record | One statement and one signature. Ecosystem tools read the VSA core, and kardinal's tools read the extension. | The extension makes the statement large |
| (d) | Two statements: a VSA, and a kardinal predicate referenced from the VSA's `inputAttestations` | Each is clean | Two signatures, two log entries and two referrers per promotion |
| (e) | SVR v0.1 (`https://in-toto.io/attestation/svr/v0.1`) | Simplest: a list of properties | Version 0.1. No policy or resource field. Fewer consumers. |

The in-toto framework allows (c):
- "Producers MAY add extension fields to any JSON object", with names that are "unlikely to collide".
- "The presence or absence of the extension field MUST NOT influence the meaning of any other field" (`in-toto/attestation@v1.2.0/spec/v1/README.md:36-46`).
- Consumers "MUST ignore unrecognized fields" (`README.md:23-26`).

A consumer that ignores the record is still correct (the monotonic principle, `README.md:48`).

**Choice: (c)**, one VSA with the extension `kardinalPromotion` (no `.` or `$`, as `README.md:41-42` asks).
- It is the only option that is both interoperable and complete in one signature.
- (d) doubles the network work and the referrers, for the same trust.
- (a) is not an audit record.
- (b) and (e) have no consumers.

### 3.2 The statement

The fields are those of `VerificationSummary` (`go/predicates/vsa/v1/vsa.pb.go:27-37`).

```jsonc
{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [
    {"name": "ghcr.io/acme/checkout", "digest": {"sha256": "9f2c..."}}
    // a config version: {"name": "https://git.example.com/acme/deploy", "digest": {"gitCommit": "3d1e..."}}
  ],
  "predicateType": "https://slsa.dev/verification_summary/v1",
  "predicate": {
    "verifier": {"id": "https://kardinal.io/controller", "version": {"kardinal-promoter": "v0.13.0"}},
    "timeVerified": "2026-10-14T10:24:03Z",
    "resourceUri": "kardinal://9b1e.../namespaces/payments/pipelines/checkout/environments/prod",
    "policy": {
      "uri": "kardinal://9b1e.../namespaces/payments/pipelines/checkout?generation=14",
      "digest": {"sha256": "<sha256 of the canonical policy set, 4.3>"}
    },
    "inputAttestations": [
      {"uri": "oci://ghcr.io/acme/checkout@sha256:9f2c...", "digest": {"sha256": "<signature bundle digest>"}}
    ],
    "verificationResult": "PASSED",
    "verifiedLevels": ["SLSA_BUILD_LEVEL_3", "KARDINAL_GATES_PASSED", "KARDINAL_APPROVED", "KARDINAL_HEALTHY"],
    "slsaVersion": "1.0",
    "kardinalPromotion": {
      "receipt": "checkout-1-5-0-prod",          // the PromotionReceipt name
      "version": {"bundle": "checkout-1-5-0", "bundleUID": "1b2c...", "version": "1.5.0", "type": "image",
                  "provenance": {"author": "...", "commitSHA": "...", "ciRunURL": "..."}},
      "actor": {"kind": "agent", "principal": "system:serviceaccount:ci:fixer", "agent": "fixer",
                "sponsor": "alice@example.com", "asserted": {"model": "claude-opus-5"}},
      "rollbackOf": "",
      "gates": [
        {"name": "kardinal-agents-approve", "template": "payments/kardinal-agents-approve",
         "templateUID": "...", "templateGeneration": 1, "templateDigest": {"sha256": "..."},
         "expression": "...", "result": "pass", "reason": "...", "evaluatedAt": "...",
         "override": null, "approvalRequired": true,
         "approvals": [{"actor": {"kind": "human", "principal": "carol@example.com"}, "decision": "approve", "counted": true}],
         "verdicts": []}
      ],
      "checks": {
        "metrics": [{"name": "error-rate", "value": "0.002", "result": "Pass", "validUntil": "..."}],
        "imageVerification": [{"image": "ghcr.io/acme/checkout@sha256:9f2c...", "verified": true, "by": "..."}],
        "health": {"adapter": "argocd", "result": "Healthy", "at": "..."}
      },
      "change": {"environment": "prod", "pr": {"url": "...", "number": 412, "mergeCommit": "...", "mergedAt": "..."}},
      "freezeExemptions": [],
      "truncated": false
    }
  }
}
```

- **`verificationResult` is always `PASSED`.** A receipt exists only for a promotion that landed (§2). A consumer that wants "this digest was promoted to prod by kardinal" reads the VSA core alone.
- **`verifiedLevels`** carries the SLSA build level that ImageVerification verified (spec 20), and nothing higher than what it verified. kardinal's own properties use the `KARDINAL_` prefix:
  - `GATES_PASSED`;
  - `APPROVED` (at least one counted approval);
  - `VERIFIED_BY_AGENT` (a counted Verdict);
  - `HEALTHY`;
  - `OVERRIDDEN` (a gate passed by override).

  Consumers can therefore require, for example, "approved and not overridden".
- **`resourceUri`** names the environment, not the image. `9b1e...` is the cluster identity: the UID of the `kube-system` Namespace, which is stable for the cluster's life. Two clusters with a `prod` environment never produce the same URI.
- **Size.** A gate copies at most 101 approvals (`pkg/graph/gates.go`), and Verdicts are bounded the same way (19-verdicts). The record is capped at 256 KiB. Past that, the longest lists (approvals that did not count, findings) are cut, counts replace them, and `truncated: true` is set. The cap keeps the whole PromotionReceipt object well under etcd's 1.5 MiB.

### 3.3 Envelope and bundle

The statement is signed as a DSSE envelope (payload type `application/vnd.in-toto+json`) and
stored as a Sigstore bundle, `application/vnd.dev.sigstore.bundle.v0.3+json`. That is the format
ImageVerification already reads (`pkg/reconciler/imageverification/registry.go:29-31`).
`sign.DSSEData` (`sigstore-go@v1.3.0/pkg/sign/content.go:52-62`) and `sign.Bundle`
(`pkg/sign/signer.go:61`) produce it; the code is already in `go.mod` (`sigstore-go v1.3.0`).

## 4. The PromotionReceipt CR and who builds it

### 4.1 Options

| | Who creates the object | For | Against |
|-|------------------------|-----|---------|
| (a) | The Graph: an owned node per environment, made resolvable when its step is Verified | Graph-native creation | A Graph and its children are deleted when the Bundle is retired (G15), and a receipt must outlive them. One more node per environment adds Graph size (G10). Compact Graphs would need another collection. |
| (b) | The PromotionStep reconciler, at the Verified transition, through its status outbox, as it writes AuditEvents (#1552) | The step already holds every input: gates, the PR, health. Creation is in the same transition, so it is exact and idempotent. The object is independent of the Graph. | One more kind the step reconciler creates |
| (c) | A receipt reconciler that watches PromotionSteps and creates receipts for Verified ones | The step reconciler is untouched | It reads the step after the fact: a retire can delete the step first, and the gates are re-evaluated by then. It is a second reader of the same facts. |

**Choice: (b) for creation, and a separate receipt reconciler for signing and storage.**

The **content** of the receipt is decided by the reconciler that made the decision, from CRD
state, in its own status transition. The **network work** happens in the `PromotionReceipt`
reconciler, which writes only its own status. Signing is never done in a gate or in the
PromotionStep reconciler. In AGENTS.md terms:
- creating the receipt is an outbox write, like AuditEvents;
- signing is an external side effect;
- both rows go on the ledger's G8 table (§9).

### 4.2 The decision snapshot

When a step leaves Pending, `checkRequiredGates` has just read every required gate.

The step then writes `status.decision` in the same patch that moves it to Promoting:
- per gate: the fields of §2 "Gates", "Approvals" and "Verdicts";
- the policy versions (§4.3).

This is a status write of its own CRD, from objects it already read. A step that started before the upgrade has no snapshot. Its receipt reads the gates at Verified time and sets `kardinalPromotion.decisionSnapshot: false`.

The snapshot is bounded by the same caps as the record (§3.2). It is cleared when the receipt is created, so the step stays small.

### 4.3 Policy versions

The policy set is canonical JSON of:
- the Pipeline's `generation` and the spec hash it already records (`Bundle.status.pipelineSpecHash`);
- for each gate template, its namespace, name, UID, `generation` and the sha256 of its canonical spec.

Its sha256 is `policy.digest`. A template's `resourceVersion` is not used: it changes on a status write and on an etcd restore.

### 4.4 The object

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PromotionReceipt
metadata:
  name: checkout-1-5-0-prod           # <bundle>-<environment>, bounded like step names
  namespace: payments
  labels: {kardinal.io/pipeline: checkout, kardinal.io/bundle: checkout-1-5-0, kardinal.io/environment: prod}
  # no ownerReferences: it outlives the Bundle, the Graph and the step
spec:
  statement: <the in-toto statement, JSON, ≤ 256 KiB>   # immutable (self == oldSelf)
  statementDigest: sha256:...                            # of the canonical statement
status:
  phase: Signed                     # Pending | Signed | Stored | Failed
  bundle: <Sigstore bundle JSON>    # the signed envelope with its certificate or key hint, log entry and timestamp
  signedBy: {mode: keyless, identity: https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter,
             issuer: https://oidc.eks.us-east-2.amazonaws.com/id/...}
  stored:
    - {sink: crd, at: "..."}
    - {sink: oci, ref: ghcr.io/acme/checkout@sha256:<referrer manifest>, at: "..."}
    - {sink: rekor, logIndex: 123456789, url: https://rekor.sigstore.dev, at: "..."}
  attempts: 2
  nextRetryAt: null
  conditions: [Signed, Stored]      # with reasons naming the failing sink
```

- **Immutable spec, append-only status.** After `Signed`, `status.bundle` never changes. A sink that fails is retried with backoff. The receipt reconciler reaches the network in its own retries, with a per-sink timeout, and never blocks the promotion: the step is already Verified.
- **Idempotent.**
  - The name derives from the Bundle and the environment, so a re-run of the transition hits AlreadyExists.
  - Signing a statement that already has `status.bundle` writes nothing.
  - Pushing a referrer whose digest already exists is a no-op: the push is content-addressed.
- **Retention**:
  - The default is no owner, under the AuditEvent retention: `--audit-retention-max-age`, or the separate `--receipt-retention-max-age`, default unlimited.
  - Receipts are evidence. Deleting a Bundle does not delete its receipts.
  - The sinks outside the cluster keep the signed copy whatever the retention.
- **Access.** Only the controller writes PromotionReceipts. A `<release>-receipts` admission policy refuses CREATE, UPDATE and DELETE by anyone else, except retention and the garbage collector. User roles get read access.

## 5. Signing

### 5.1 Options

| | Mode | Identity a verifier pins | For | Against |
|-|------|--------------------------|-----|---------|
| (a) | Keyless, through Fulcio, with the controller's projected ServiceAccount token (audience `sigstore`) | Fulcio's Kubernetes principal, `https://kubernetes.io/namespaces/<ns>/serviceaccounts/<sa>`, from the token's `kubernetes.io` claims (`fulcio@v1.9.0/pkg/identity/kubernetes/principal.go:33-35, 73-104`), plus the cluster's OIDC issuer | No key to manage. A short-lived certificate. Public-good Fulcio accepts the issuers of EKS, GKE and AKS clusters with client ID `sigstore` (`fulcio@v1.9.0/config/identity/config.yaml:177-208`). | Needs network access to Fulcio and Rekor (or a timestamp authority). kind and on-prem clusters need a private Fulcio that trusts their issuer. The certificate names the SA, not the cluster: pin the issuer too. |
| (b) | A KMS key: `awskms://`, `gcpkms://`, `azurekms://` or `hashivault://` through `kms.Get` (`sigstore@v1.10.8/pkg/signature/kms/kms.go:66`) | The public key | Works offline from Fulcio. Fits enterprise key custody. The key lives outside the cluster. | The providers are separate Go modules that pull in cloud SDKs. Unregistered schemes go to `cliplugin.LoadSignerVerifier`, a subprocess (`kms.go:78`): no subprocess in the controller (AGENTS.md). |
| (c) | A static key in a Secret (PEM, ECDSA P-256) | The public key | Air-gapped. Simple. | Anyone who reads the Secret can sign. Rotation is manual. |
| (d) | No signing: CR only | — | Zero setup | Not checkable outside the cluster; not a receipt |

**Choice: all of (a), (b) and (c), configured per release (`receipts.signing.mode`), default off for an upgrade and (a) for a new install that enables receipts.**
- **(b) ships only for the providers the chart's image is built with**: AWS and GCP in v0.13, each a build tag of the controller binary. The CLI-plugin fallback is disabled by registering no plugin path, and the controller refuses a key reference whose provider it was not built with.
- **(d)** remains as `receipts.enabled: false`.
- **Keyless needs a token audience.** The controller mounts a projected ServiceAccount token with audience `sigstore` and a 10-minute expiry, a separate token from the one it uses for the API server. It passes the token as `CertificateProviderOptions.IDToken` (`sigstore-go@v1.3.0/pkg/sign/certificate.go:36-39, 115-117`).
- **The key is short-lived.** A `sign.EphemeralKeypair` (`pkg/sign/keys.go:52-60`) is made per receipt.
- **Transparency.** Keyless needs proof of signing time:
  - a Rekor entry (`sign.Rekor`, `pkg/sign/transparency.go:73-99`); or
  - an RFC 3161 timestamp (`sign.TimestampAuthority`, `pkg/sign/timestamping.go:44-67`), for clusters that must not publish to a log.

  Key modes may skip both.

### 5.2 What the identity proves

A receipt signed keyless proves "the release's controller ServiceAccount in the cluster with this
issuer". It does not prove that the receipt's content is true: an admin who can mint that SA's
tokens can sign anything. The statement's correctness rests on the controller, as everything else
in kardinal does (17-actors §11). The signature makes the record tamper-evident after it leaves
the cluster, and it pins who stands behind it.

## 6. Storage

### 6.1 Options

| | Sink | For | Against |
|-|------|-----|---------|
| (a) | The CR (`status.bundle`) | Always available. Read with kubectl. No extra credentials. | Lives and dies with the cluster |
| (b) | An OCI referrer on each subject image (`subject` = the image digest; `remote.Write` keeps the referrers fallback tag for registries without the API, `go-containerregistry@v0.22.1/pkg/v1/remote/write.go:514-516, 605-612`) | Travels with the image. Admission controllers in other clusters find it with `remote.Referrers` (`pkg/v1/remote/referrers.go:37`). | Needs push rights to the registry; today the controller only reads. A config version has no image. |
| (c) | Rekor, public or private | Public, append-only, timestamped | Publishes the statement's metadata, including names, emails and pipeline names, if the whole bundle goes to a public log. Needs network access. |
| (d) | The GitOps repository (a file or git note on the environment branch) | Works for config versions and in air gaps | More commits, branch protection, churn; mixes evidence with desired state |

**Choice: (a) always, plus (b) and (c) opt in, each a sink in `receipts.sinks`.**
- **(c) to the public good instance is off by default and needs `receipts.sinks.rekor.public: true`**: the chart's values say what it publishes. A private Rekor URL is the enterprise path.
- **(b)** uses a registry credential the cluster admin grants: a Secret named in `receipts.sinks.oci.secretRef`. It is never the pull Secret of a tenant.
- **(d) is rejected.** Evidence does not belong in the change stream.
- **Config versions** have no image, so only (a) and (c) apply. Their subject is the commit (`gitCommit`, `spec/v1/digest_set.md:103`).
- **Air-gapped clusters** use (a) with (b) to an in-cluster registry, a static or KMS key, and no log.

### 6.2 A fix this needs in ImageVerification

Receipts pushed as referrers use the Sigstore bundle artifact type. ImageVerification reads
referrers with that prefix as candidate image signatures, and stops after `maxSignatures` (32)
(`registry.go:39, 131`). An image promoted to 30 environments would then crowd out its own
signature, and verification would fail. Before (b) ships:
- receipt referrers carry the annotation `dev.sigstore.bundle.predicateType: https://slsa.dev/verification_summary/v1`;
- ImageVerification skips referrers whose predicate type is not one it verifies, before counting.

The test plan covers it (§11).

## 7. `kardinal verify-receipt`

```
kardinal verify-receipt <pipeline> <env> [--bundle <name>]        # from the cluster
kardinal verify-receipt --image <ref@sha256:...> --env <env>        # from the OCI referrer
kardinal verify-receipt --file receipt.sigstore.json                # offline
  [--identity <SAN>] [--issuer <url>] | [--key <pem|kms-ref>]
  [--trusted-root trusted_root.json] [--require GATES_PASSED,APPROVED,!OVERRIDDEN]
  [-o json]
```

It checks:
1. **Signature and certificate chain**, with `verify.NewVerifier` against the trusted root, as ImageVerification does (`pkg/reconciler/imageverification/verify.go:148-165`). Key modes check the key. The certificate identity, `--identity` and `--issuer`, is required in keyless mode: no default trusts any identity.
2. **Transparency or timestamp**, when the receipt has them. `--offline` accepts an embedded log entry with its inclusion proof and does not contact Rekor.
3. **The subject**: the digest given, or the Bundle's images or commit.
4. **The predicate**: VSA, `PASSED`, `resourceUri` for the environment asked, and each `--require`d level present (a `!` means it must be absent).
5. **Against the cluster** (online mode only): the receipt's `statementDigest` equals the CR's, and the Bundle UID matches.

It prints:
- the version;
- the actor sentence (17-actors §8);
- the gates with their results;
- the counted approvers and verifiers;
- the policy digest.

The exit code is 0 only when every check passes.

## 8. PR evidence

The PR body is written once, when the PR opens. The receipt exists only after the merge and the
health check. So:
- **A PR comment** when the receipt is `Signed`: "Promotion receipt: `checkout-1-5-0-prod`, signed by <identity>, stored in <sinks>. Verify: `kardinal verify-receipt checkout prod --bundle checkout-1-5-0`". It uses the SCM provider's `CommentOnPR`, which exists for every provider.
  - The receipt reconciler posts the comment once and records `status.prComment: true`, so a retry does not post it twice.
  - It is an external call in that reconciler, never in a gate. The ledger's existing row on SCM side effects covers it.
- **`approval: auto`** environments have no PR. The receipt is shown by `kardinal history`, `kardinal why` and the UI's stage drawer, each with a "verified ✓" mark when `phase` is at least Signed.
- **The receipt links the PR** (`kardinalPromotion.change.pr`), so the two point at each other.

## 9. Graph-first

| Piece | Mechanism | Ledger |
|-------|-----------|--------|
| Decision snapshot | PromotionStep reconciler, its own status, at the Pending → Promoting transition | none (own status) |
| Receipt creation | PromotionStep reconciler outbox at Verified, like AuditEvents | G8: new row "Promotion receipts". Created outside the Graph because a Graph's children die with it (G15). |
| Signing, OCI push, Rekor, PR comment | PromotionReceipt reconciler, its own status | G8, the same row: external side effects, as for SCM steps |
| Verification | CLI only | — |

No gate reads a receipt, and no promotion waits for one. A failing sink is a condition on the
receipt and a metric, `kardinal_receipts_failed_total{sink}`, never a hold.

## 10. What this does not protect against

- **A compromised controller signs what it is told.** The receipt is as true as the controller.
- **Asserted fields stay asserted.** The model and session in the actor are recorded under `asserted`, as written by the client (17-actors §4.1).
- **Events after Verified** (a later revert outside kardinal, drift) are not in the receipt. It records the promotion, not the state of the cluster afterwards.
- **Public Rekor discloses metadata.** It is off by default.
- **A deleted CR with no external sink is gone.** Use a sink for evidence you must keep.

## 11. Upgrade from v0.12

- Receipts are off by default on upgrade: `receipts.enabled: false`.
- Enabling them creates receipts for steps that become Verified afterwards. Steps already Verified get none.
- A step already Promoting when the controller is upgraded has no decision snapshot (§4.2).
- The new CRD (`promotionreceipts`) and the step's `status.decision` field are applied before the chart, as always.

## 12. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| R1 | Predicate | VSA; kardinal predicate; VSA + extension; VSA + separate predicate; SVR | **VSA v1 + `kardinalPromotion` extension** (3.1): interoperable and complete in one signature; in-toto allows it |
| R2 | Who creates the receipt | Graph node; PromotionStep outbox; watching reconciler | **PromotionStep outbox** (4.1): it holds the facts, and the object outlives the Graph (G15) |
| R3 | When gates are frozen | at Verified; at the decision; from AuditEvents | **At the decision**: the step's `status.decision`, written when it leaves Pending (4.2) |
| R4 | Signing | keyless; KMS; static; none | **All three**, keyless the default for new installs; KMS without the CLI-plugin subprocess (5.1) |
| R5 | Storage | CR; OCI referrer; Rekor; git | **CR always; OCI and Rekor opt-in; public Rekor explicit** (6.1) |
| R6 | Lifetime | owned by the Bundle; AuditEvent retention; own retention | **Own retention, default unlimited, no owner** (4.4) |
| R7 | PR link | edit the body; a comment; none | **A comment when signed** (8): the body is a snapshot, and the receipt comes after the merge |
| R8 | Which promotions | every step; Verified only; Verified plus Failed | **Verified (promotions and rollbacks)**: a receipt is evidence of a change that landed |

## 13. Test plan

- **Unit**:
  - **Statement building** (golden JSON per Bundle type, with the snapshot, without it, truncated): the subject for image, config and mixed versions.
  - **The policy digest** is stable across a template's status writes.
  - **The size cap.**
  - **Signing** with a fake Fulcio and Rekor (httptest), and with a static key. Each signs exactly once, and a re-run writes nothing.
  - **Each sink's retry and backoff.**
  - **The `<release>-receipts` admission policy** (test/helm).
  - **ImageVerification skips receipt referrers** (§6.2), with 40 receipts and one signature.
  - **`verify-receipt`**: valid; tampered statement; wrong identity; wrong environment; `--require` failing; offline.
- **Fake-client journeys**:
  - An agent's version promoted to prod through an approval gate yields a receipt naming:
    - the agent and its sponsor;
    - the approver;
    - the gates as they were at the decision. A schedule gate turning false afterwards does not change the receipt.
  - A rollback yields a receipt with `rollbackOf`.
  - Retiring the Bundle leaves the receipt in place.
- **Live e2e** (CI `e2e-live.yml`, core suite):
  - A static-key receipt for a promotion through test and prod.
  - `kardinal verify-receipt` passes, and it fails after a byte of the CR's statement is changed by an admin who removed the admission policy.
  - A receipt is pushed as a referrer to the suite's OCI registry, and `--image` verification finds it.
  - The PR comment.
  - Each test gets a coverage row.
- **Keyless** is tested against a local Fulcio, Rekor and timestamp authority (the sigstore scaffolding) in a dedicated job, not on every PR.
