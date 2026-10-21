# Map kardinal's records to compliance controls

!!! info "New in v0.13.0"
    v0.12.0 and earlier have no `kardinal audit evidence-pack`, auditor roles, promotion receipts
    or change requests; the other records this page names exist there too. The
    [changelog](changelog.md) lists every change since v0.12.0.

An auditor asks a team that promotes with kardinal two things: show that every production
change was authorised, reviewed by someone other than its author, and recorded; and show the
records. This page says which kardinal record answers which control of SOC 2, ISO/IEC 27001,
NIST SP 800-53, DORA, the EU AI Act and ISO/IEC 42001, and what you still have to do yourself.

kardinal is not a GRC tool and does not certify anything. It produces evidence for the change
part of these controls; your policies, reviews and other systems cover the rest. Each control
below ends with **Your part**.

## Keep the records

The records live in the cluster: AuditEvents, Approvals, gate instances with their overrides,
versions (Bundles) with their verified creator and provenance, Pipelines, ChangeWindows and
freezes, AgentIdentities, Verdicts and change request tickets (ChangeRequests). The cluster is a working store, not an archive:

- AuditEvents are deleted by retention, after 90 days or past the newest 1,000 of a Pipeline
  by default (`--audit-retention-max-age`, `--audit-retention-max-per-pipeline`;
  [Retention](guides/security.md#retention)).
- A version is deleted with its Approvals and gate instances once its Pipeline keeps more
  than `spec.historyLimit` (50 by default) finished versions. Its AuditEvents stay.
- Anyone a role grants `delete` on `auditevents` can remove a record
  ([RBAC: read-only access to audit records](guides/security.md#rbac-read-only-access-to-audit-records)).

So archive the AuditEvents outside the cluster, more often than retention deletes them: for
example a daily `kardinal audit export -A --since 25h` into your SIEM, deduplicated by `uid`
([SIEM integration](guides/security.md#siem-integration)). The SIEM is then the archive of
record.

## Produce an evidence pack

`kardinal audit evidence-pack` writes every record of a period into one gzipped tar for the
auditor:

```bash
kardinal audit evidence-pack -A --since 2026-07-01 --until 2026-10-01 -o q3.tar.gz
# sha256:4f1c...  q3.tar.gz (17 files, 18342 records)
```

The period is the versions created in `[--since, --until)` and the AuditEvents timestamped in
it. Each version brings its Approvals, gate instances (with their overrides and results),
Verdicts, ChangeRequest instances (the [change request](change-requests.md) ticket, its state
and approvers, and whether a linked ticket was verified) and signed [promotion receipts](receipts.md)
(`receipts.ndjson`, with receipts on). The Pipelines, gate and ChangeRequest templates,
ChangeWindows, AgentIdentities and the chart's
admission policies are the ones in force at export. `controls.json` maps each control below
to the pack's files, and `manifest.json` records the exporter, the period, the filters
(`-n`, `--pipeline`, `--environment`) and each file's record count and SHA-256
([kardinal audit evidence-pack](reference/cli/kardinal-audit-evidence-pack.md)).

Producing a pack needs read on every kind it holds, and the chart has two ClusterRoles that grant
exactly that and no write ([User roles](guides/security.md#user-roles)):
`<release>-auditor` reads the namespaced records (AuditEvents, versions, Approvals, gates,
Verdicts, ChangeRequests, Pipelines), and `<release>-auditor-cluster` the cluster-scoped ones (ChangeWindows,
AgentIdentities) and the admission policies. An auditor of some namespaces gets the first in each
of them with a RoleBinding, and the second cluster-wide; they then export each namespace with
`-n`:

```bash
kubectl create rolebinding auditors -n payments --clusterrole=kardinal-promoter-auditor --group=auditors
kubectl create clusterrolebinding auditors --clusterrole=kardinal-promoter-auditor-cluster --group=auditors
```

An auditor of the whole cluster gets both with ClusterRoleBindings and exports with `-A`.

The pack leaves the cluster, so no credential goes with it. In every record:

- the user name, token or password of each URL is removed, and so is the value of a secret
  query parameter (`token=`, `access_token=` and the like), as kardinal does for its status, CLI
  output and logs: a Pipeline whose `spec.git.url` carries a token is exported without it;
- each container environment variable keeps its name and its `valueFrom`, and a literal
  `value` becomes `<redacted>`, in hook Job specs too;
- container commands and arguments (of containers, init containers, probes and lifecycle
  handlers) are not exported: each list becomes `{"sha256": "…", "length": N}`, the SHA-256 of
  its arguments, each followed by a NUL byte, and how many there are. A secret can hide in any
  form in a command, so the pack holds none. The digest is unkeyed, so that anyone can check it:
  a short or guessable secret in a command could be found from it by trying candidates. Check a
  command against the one you expect:

  ```bash
  printf '%s\0' sh -c 'run --env prod' | sha256sum
  ```

- the `kubectl.kubernetes.io/last-applied-configuration` annotation is removed, since it
  repeats the spec as applied.

To read the commands themselves, add `--include-commands`. The values of flags, of
`NAME=value` assignments (with `export` or not) and of HTTP headers (`-H 'Name: value'`) whose
name contains `token`, `password`, `passwd`, `passphrase`, `pwd`, `secret`, `apikey`, `api-key`,
`api_key`, `key`, `credential` or `authorization`, or has the part `auth`, `bearer`, `pat` or
`pass`, then become `<redacted>`, and so do a `Bearer` token and the password of `-u` or
`--user user:password`, also inside one shell string (`sh -c "…"`). This is best-effort, and the command warns: a secret passed as a
positional argument, after a short flag such as `-p`, or under another name is exported as
written. Keep secrets in Secrets and pass them with `valueFrom`.

`manifest.json`'s `redactions` records the mode (`commands`: `digest` or `redacted`), the rules
and how often each changed something.

Check the files against their checksums after extracting:

```bash
tar -xzf q3.tar.gz && cd evidence-pack && sha256sum -c SHA256SUMS
```

The checksums show that no file changed after export; they are not a signature.

### Sign the pack

Sign the pack under your own identity before you hand it over: kardinal holds no signing key for
packs, and the signature then names who exported it. With a key pair:

```bash
cosign generate-key-pair                     # once; keep cosign.key safe
cosign sign-blob --yes --key cosign.key --bundle q3.tar.gz.sigstore.json q3.tar.gz
# the auditor, with cosign.pub:
cosign verify-blob --key cosign.pub --bundle q3.tar.gz.sigstore.json q3.tar.gz
```

Signing records the pack's digest in the public Rekor log. Without network access, sign with
`--tlog-upload=false` and verify with `--insecure-ignore-tlog`. Keyless (Sigstore's OIDC sign-in),
sign without `--key`, and the auditor verifies with `--certificate-identity <your email>` and
`--certificate-oidc-issuer <your identity provider>`. These are cosign 2.4's flags; see
[cosign's blob signing](https://docs.sigstore.dev/cosign/signing/signing_with_blobs/).

### Verify a receipt from the pack

Each receipt in `receipts.ndjson` keeps its signed statement and Sigstore bundle as the
controller signed them when the promotion reached Verified, so it verifies offline from the pack,
with the receipts' public key, the image digest the receipt is about and the cluster's ID
([Verify a receipt](receipts.md#verify-a-receipt)):

```bash
jq -r 'select(.metadata.name == "checkout-1-5-0-prod") | .status.bundle' receipts.ndjson > receipt.sigstore.json
kardinal verify-receipt --file receipt.sigstore.json --key receipt.pub \
  -n payments --pipeline checkout --env prod --subject "$IMAGE_DIGEST" --cluster "$CLUSTER_ID"
```

A receipt whose signed statement holds a credential (a token in a CI run URL, for example) cannot
be redacted without breaking its signature, so the pack never exports it: it checks every key
and value of the decoded statement and bundle, and leaves both out when one holds a credential,
when either does not decode, or when the bundle does not sign exactly the statement. The
receipt is annotated `kardinal.io/evidence-pack-withheld` with the reason, the command warns,
and `redactions.receiptsWithheld` counts them. Verify such a receipt from the cluster or its OCI
or Rekor sink.

`manifest.json`'s `coverage` says what the pack cannot hold:

- `oldest`: the oldest record of each kind the cluster still holds. When the period starts
  before the oldest AuditEvent, the command warns that retention may have deleted records of
  the period; when the period ends before it, the command fails: use your SIEM's archive.
- `missing`: kinds the cluster does not serve (an older chart).
- `forbidden`: kinds you may not read. Their files are left out, not written empty. A reader
  limited to some namespaces exports each with `-n <namespace>`.

Set `--until` for a pack you can produce again: two exports of the same records are the same
bytes.

## What kardinal cannot show

- **Changes that bypass it.** A direct push to the GitOps branch or a `kubectl apply` on the
  workload is invisible to kardinal. Protect the branches and require the `kardinal/gates`
  commit status.
- **A record lost before it was written.** Writing audit records is best-effort under overload:
  - a reconciler's audit outbox (32 records) that is still full after merging a version's unwritten
    `GateChanged` records into one drops its oldest entry;
  - a record that fails validation, or that the API server rejects as invalid, is dropped;
  - an `APIWrite` record over the per-second budget, with its queue full, or whose create fails is
    dropped.

  Each is counted in `kardinal_audit_events_dropped_total`; alert when it rises
  ([Audit outbox](guides/security.md#audit-outbox)). Merges are counted in
  `kardinal_audit_events_coalesced_total`, and the merged record's `spec.mergedRecords` says how many
  it replaces. A record for a namespace that is being deleted is not written, and not counted: that
  namespace's AuditEvents are deleted with it.
- **Changes kardinal does not record.** A direct write with kubectl, or by an agent through
  `kardinal mcp` with its own kubeconfig, to a Pipeline, a PolicyGate or a ChangeWindow is in the
  Kubernetes API server's audit log only.
- **That a record was not deleted** before you exported it, by someone allowed to delete it.
  Export continuously, and keep `delete` on `kardinal.io` resources to the cluster's
  administrators. With receipts stored in an OCI registry or a Rekor log, each promotion also
  has a signed record outside the cluster ([Where receipts are stored](receipts.md#where-receipts-are-stored)).
- **The AI Act and ISO/IEC 42001 rows are analogies.** They apply the records those texts ask
  of AI systems to the changes your agents make; they do not make an agent compliant.

## The controls

<!-- BEGIN GENERATED from pkg/compliance: KARDINAL_UPDATE_COMPLIANCE=1 go test ./pkg/compliance -run TestCompliancePage -->

### SOC 2 (2017 Trust Services Criteria)

#### CC8.1: Change management

*Requirement:* Changes to infrastructure and software are authorised, designed, tested, approved and implemented under control; points of focus include tracking changes, approving them, segregating the duties of developers and approvers, and handling emergency changes.

| Element | kardinal record |
|---|---|
| Changes are tracked | `AuditEvent` promotion actions per version and environment, with the actor that caused them; the version's `spec.provenance` (author, commit, CI run) and its verified creator (`spec.actor`, annotation `kardinal.io/created-by`) |
| Approvals, where production requires them | approval gates (`spec.approval` on a PolicyGate), the `Approval` objects and `status.approvals[]` (who approved, and whether it counted), and `pr-review` environments with the `kardinal/gates` commit status; an `auto` environment records none. A gate that reads `changeRequests` waits for the change request in ServiceNow or Jira: the `ChangeRequest` instance records the ticket, its state and approvers, and whether a linked ticket was verified as the promotion's |
| The version's creator does not approve it (when `excludeAuthor` is set) | `approval.excludeAuthor` (off by default, except on the approval gates kardinal adds for agents' versions in `agents: approve` environments) does not count an approval by the user who created the version (`kardinal.io/created-by`): for a version CI creates, that is the CI identity, not the commit's author, whom kardinal does not compare. With an actor stamp, an agent and its sponsor count as one party. `status.approvals[].reason` says why an approval did not count. A verify gate's `verify.excludeAuthor` is on by default: a verdict from the version's author's party does not count |
| Emergency changes are controlled | gate overrides (`spec.overrides[]`: who, why, an expiry the policy caps) and freeze exemptions (`ChangeWindow.spec.freeze.exemptions[]`: who and why) |
| Changes are tested before release | the health check and bake of each environment (a PromotionStep reaches `Verified` only after them), MetricChecks, upstream soak gates, and `Verdict`s from the verifiers a gate requires |
| Evidence is tamper-evident | a signed `PromotionReceipt` per promotion per environment (v0.13, off by default: `receipts.enabled`), written when the step reaches Verified and verifiable offline with `kardinal verify-receipt`; `receipts.ndjson` in the evidence pack keeps each one with its signed envelope |

**Your part:** The change policy itself, the review of the evidence, changes that bypass kardinal (direct pushes to the GitOps branch: protect it and require the `kardinal/gates` status), and access reviews of who holds the approver role. Requiring an approval on production: `approval: pr-review`, or a PolicyGate with `spec.approval`, on each production environment. Setting `approval.excludeAuthor: true` on those gates; and, to keep a change's commit author from approving it, the SCM's branch protection (required reviewers or code owners, no self-approval), which kardinal does not check.

### ISO/IEC 27001:2022 Annex A

#### A.8.32: Change management

*Requirement:* Changes to information processing facilities and information systems are subject to change management procedures.

| Element | kardinal record |
|---|---|
| The procedure is enforced, not advisory | a change reaches an environment only through a PromotionStep its Pipeline and gates allow; the identity ValidatingAdmissionPolicies keep the names in the records from being forged (`admission.yaml` in the evidence pack) |
| Changes are planned and recorded | `AuditEvent` promotion actions per version and environment, with the actor that caused them; the version's `spec.provenance` (author, commit, CI run) and its verified creator (`spec.actor`, annotation `kardinal.io/created-by`) |
| Approvals, where production requires them | approval gates (`spec.approval` on a PolicyGate), the `Approval` objects and `status.approvals[]` (who approved, and whether it counted), and `pr-review` environments with the `kardinal/gates` commit status; an `auto` environment records none. A gate that reads `changeRequests` waits for the change request in ServiceNow or Jira: the `ChangeRequest` instance records the ticket, its state and approvers, and whether a linked ticket was verified as the promotion's |
| Rollback | rollback versions (`spec.provenance.rollbackOf`) and their promotions; holds that keep an environment on a version (`Pipeline.spec.holds[]`) |
| Change windows | `ChangeWindow` blackout and recurring windows, and freezes (who froze, who lifted, the exemptions) |

**Your part:** The documented procedure, the risk assessment of a change, and communication to affected parties. Requiring an approval on production: `approval: pr-review`, or a PolicyGate with `spec.approval`, on each production environment.

#### A.5.3: Segregation of duties

*Requirement:* Conflicting duties and areas of responsibility are segregated.

| Element | kardinal record |
|---|---|
| The version's creator does not approve it (when `excludeAuthor` is set) | `approval.excludeAuthor` (off by default, except on the approval gates kardinal adds for agents' versions in `agents: approve` environments) does not count an approval by the user who created the version (`kardinal.io/created-by`): for a version CI creates, that is the CI identity, not the commit's author, whom kardinal does not compare. With an actor stamp, an agent and its sponsor count as one party. `status.approvals[].reason` says why an approval did not count. A verify gate's `verify.excludeAuthor` is on by default: a verdict from the version's author's party does not count |
| An agent and the person accountable for it count as one | each agent's sponsor (`AgentIdentity.spec.sponsor`) and the approver's verified actor (`Approval.spec.actor`) |
| Distinct roles | the chart's user roles (`<release>-viewer`, `-promoter`, `-approver`, `-admin`); approvers named by `approval.allowedUsers` and `allowedGroups` |
| Quorum | `approval.required: N` distinct parties |

**Your part:** Who is granted which role, and the access review. Setting `approval.excludeAuthor: true` on those gates; and, to keep a change's commit author from approving it, the SCM's branch protection (required reviewers or code owners, no self-approval), which kardinal does not check.

### NIST SP 800-53 Rev. 5

#### CM-3: Configuration change control

*Requirement:* Determine the types of controlled changes; review and approve them with their security impact in mind; document the decisions; implement approved changes; retain the records; monitor and review change activity.

| Element | kardinal record |
|---|---|
| Controlled changes are defined and implemented as approved | a change reaches an environment only through a PromotionStep its Pipeline and gates allow; the identity ValidatingAdmissionPolicies keep the names in the records from being forged (`admission.yaml` in the evidence pack) |
| Approvals, where production requires them | approval gates (`spec.approval` on a PolicyGate), the `Approval` objects and `status.approvals[]` (who approved, and whether it counted), and `pr-review` environments with the `kardinal/gates` commit status; an `auto` environment records none. A gate that reads `changeRequests` waits for the change request in ServiceNow or Jira: the `ChangeRequest` instance records the ticket, its state and approvers, and whether a linked ticket was verified as the promotion's |
| Records are retained and change activity is reviewed | AuditEvents, kept as `--audit-retention-max-age` and `--audit-retention-max-per-pipeline` allow; `kardinal audit summary`, `kardinal audit export` and `kardinal audit evidence-pack` |

**Your part:** The configuration management plan, the change control board's charter, and the system's other components. Requiring an approval on production: `approval: pr-review`, or a PolicyGate with `spec.approval`, on each production environment.

#### CM-3(2): Testing, validation and documentation of changes

*Requirement:* Test, validate and document changes before implementing them.

| Element | kardinal record |
|---|---|
| Changes are tested before release | the health check and bake of each environment (a PromotionStep reaches `Verified` only after them), MetricChecks, upstream soak gates, and `Verdict`s from the verifiers a gate requires |

**Your part:** The test plans themselves, and the tests that run outside kardinal (CI).

#### CM-5: Access restrictions for change

*Requirement:* Define, document, approve and enforce access restrictions associated with changes.

| Element | kardinal record |
|---|---|
| Who may change what | Kubernetes RBAC with the chart's roles (`<release>-viewer`, `-promoter`, `-approver`, `-freezer`, `-verifier`, `-admin`) and virtual subresources (`pipelines/pause`, `pipelines/hold`, `policygates/override`, `bundles/reject`, `changewindows/freeze`); the `<release>-scoped-writes` and `-graph-objects` policies; each agent's scope (`AgentIdentity.spec.namespaces`, `environments`) |

**Your part:** Who is bound to which role, and the access review.

#### CM-5(1): Automated access enforcement and audit records

*Requirement:* Enforce access restrictions automatically and keep audit records of the enforcement actions.

| Element | kardinal record |
|---|---|
| Enforcement and its records | ValidatingAdmissionPolicies that deny, not warn; `APIWrite` AuditEvents for the UI's and the Bundle API's writes, refusals included. The records are best-effort: one dropped under overload is counted in `kardinal_audit_events_dropped_total` |

**Your part:** The Kubernetes API server's own audit log, for writes made with kubectl.

#### AC-5: Separation of duties

*Requirement:* Identify the duties that need separation and define access authorisations to support it.

| Element | kardinal record |
|---|---|
| The version's creator does not approve it (when `excludeAuthor` is set) | `approval.excludeAuthor` (off by default, except on the approval gates kardinal adds for agents' versions in `agents: approve` environments) does not count an approval by the user who created the version (`kardinal.io/created-by`): for a version CI creates, that is the CI identity, not the commit's author, whom kardinal does not compare. With an actor stamp, an agent and its sponsor count as one party. `status.approvals[].reason` says why an approval did not count. A verify gate's `verify.excludeAuthor` is on by default: a verdict from the version's author's party does not count |

**Your part:** Which duties need separating in the organisation, and who holds each role. Setting `approval.excludeAuthor: true` on those gates; and, to keep a change's commit author from approving it, the SCM's branch protection (required reviewers or code owners, no self-approval), which kardinal does not check.

### DORA, Regulation (EU) 2022/2554

Change management is Article 9(4)(e) of the Regulation, detailed in the RTS on the ICT risk management framework (Commission Delegated Regulation (EU) 2024/1774). Article 17 is the ICT-related incident management process, which kardinal contributes records to but does not run.

#### Art. 9(4)(e): ICT change management

*Requirement:* Documented policies, procedures and controls for ICT change management: changes are recorded, tested, assessed and approved in a controlled way, and emergency changes are handled and documented.

| Element | kardinal record |
|---|---|
| Changes are recorded | `AuditEvent` promotion actions per version and environment, with the actor that caused them; the version's `spec.provenance` (author, commit, CI run) and its verified creator (`spec.actor`, annotation `kardinal.io/created-by`) |
| Changes are tested | the health check and bake of each environment (a PromotionStep reaches `Verified` only after them), MetricChecks, upstream soak gates, and `Verdict`s from the verifiers a gate requires |
| Approvals, where production requires them | approval gates (`spec.approval` on a PolicyGate), the `Approval` objects and `status.approvals[]` (who approved, and whether it counted), and `pr-review` environments with the `kardinal/gates` commit status; an `auto` environment records none. A gate that reads `changeRequests` waits for the change request in ServiceNow or Jira: the `ChangeRequest` instance records the ticket, its state and approvers, and whether a linked ticket was verified as the promotion's |
| Emergency changes are handled and documented | gate overrides (`spec.overrides[]`: who, why, an expiry the policy caps) and freeze exemptions (`ChangeWindow.spec.freeze.exemptions[]`: who and why) |

**Your part:** The ICT risk management framework and the change policy it requires. Requiring an approval on production: `approval: pr-review`, or a PolicyGate with `spec.approval`, on each production environment.

#### Art. 17: ICT-related incident management process

*Requirement:* Incidents are detected, managed, logged, classified and followed up.

| Element | kardinal record |
|---|---|
| The change record an investigation needs | what changed where, by whom and when (the promotion AuditEvents and the version's actor), and the rollback that restored it |
| Stopping changes during an incident | `kardinal freeze --everyone` (who froze, why, who lifted it) |

**Your part:** Incident detection, classification and reporting to the authority (Articles 18 and 19).

### EU AI Act, Regulation (EU) 2024/1689

These articles bind the providers and deployers of the AI systems the Act names (Articles 12 and 14: high-risk systems; Article 50: systems that interact with people or generate content). A coding agent that changes software is usually none of these. kardinal does not make anyone compliant with the AI Act: it gives an organisation that runs agents the records these articles ask of AI systems, applied to the agents' changes.

#### Art. 12: Record-keeping

*Requirement:* The system allows the automatic recording of events (logs) over its lifetime, to identify risks and support monitoring.

| Element | kardinal record |
|---|---|
| The agents' promotions, approvals, verdicts and freezes are logged | AuditEvents with `actor.kind: agent`, the agent's sponsor and what it asserted about itself (model, session, client), for what an agent does through kardinal: its versions and their promotions, approvals, verdicts, freezes and holds, and its UI and Bundle API writes (`APIWrite`); `kardinal audit export` |

**Your part:** Deciding whether the Act applies to your systems, and their own documentation. The Kubernetes API server's audit log, for an agent's direct writes (kubectl, or `kardinal mcp` with the agent's kubeconfig) to a Pipeline, a PolicyGate or a ChangeWindow, which kardinal does not record.

#### Art. 14: Human oversight

*Requirement:* People can oversee, understand, override and stop the system (Article 14(4)(e): a stop button or a similar procedure).

| Element | kardinal record |
|---|---|
| A person approves, overrides or stops | `agents: approve` environments (a person approves an agent's change), each agent's autonomy, suspending an AgentIdentity, `kardinal freeze --agents`, and stop-the-line (`--everyone`) |

**Your part:** Who is trained and assigned to oversee the agents.

#### Art. 50: Transparency

*Requirement:* People are told when they interact with AI, and AI-generated content is marked in a machine-readable way.

| Element | kardinal record |
|---|---|
| Agent-made changes are marked | the actor stamp marks each version and decision an agent made (`spec.actor.kind: agent`), shown in the PR evidence and the UI |

**Your part:** Telling the people who read the agents' output.

### ISO/IEC 42001:2023 (AI management system), Annex A

Listed by the Annex A control's title; check the clause numbers against your copy of the standard. As for the AI Act, these records apply to the agents' changes, not to the agents themselves.

#### AI roles and responsibilities

*Requirement:* Roles and responsibilities for AI are defined and allocated.

| Element | kardinal record |
|---|---|
| An accountable person per agent | `AgentIdentity.spec.sponsor`, and the agent's scope |

**Your part:** The AI management system itself: policy, objectives, and risk and impact assessments.

#### AI system recording of event logs

*Requirement:* The organisation determines at which phases of the AI system life cycle event logs are recorded.

| Element | kardinal record |
|---|---|
| The agents' promotions, approvals, verdicts and freezes are logged | AuditEvents with the agent's verified actor and sponsor, for what an agent does through kardinal |

**Your part:** Logging of the AI systems themselves, outside kardinal, and the Kubernetes API server's audit log for an agent's direct writes.

#### Use of AI systems

*Requirement:* AI systems are used responsibly, as intended, with human oversight in operation.

| Element | kardinal record |
|---|---|
| Oversight in operation | autonomy per environment (`agents: auto \| approve \| deny`), opt-in agent approvals, freezes and suspension |

**Your part:** The intended use of each agent and the responsible-use policy.

<!-- END GENERATED -->
