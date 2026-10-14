# Design: Compliance mapping and the evidence pack

> Status: Accepted (spec, v0.13.0). Implementation follows in a separate PR.
> Created: 2026-10-14
> Depends on: [17-actors.md](17-actors.md) (the actor and `kardinal audit export`),
> [18-actor-policy.md](18-actor-policy.md) (party rules, freezes),
> [19-verdicts.md](19-verdicts.md) (Verdicts), and the v0.13 specs `20-attestations.md` and
> `21-receipts.md` (in review when this was written; the rows that rely on them are marked
> *planned*).
> Verified against: this repository at `417c943` (main). Control texts are paraphrased from the
> standards. A row marked "verify against the standard" is one where the clause numbering, not
> the control's substance, must be checked before the user page ships.

---

## 1. Problem

An auditor asks a team running kardinal two things: "show me that every production change
was authorised, reviewed by someone other than its author, and recorded", and "show me the
records". kardinal already keeps those records in the cluster: AuditEvents, Approvals, gate
instances with their overrides, Bundles with their verified creator and provenance, and from
v0.12 the actor stamp, AgentIdentities and freezes. But nothing says which record answers which
control, and nothing collects the records for a period into one package an auditor can keep.

This spec defines:

- the **control mapping**: for each control in scope, the exact kardinal record that evidences
  it, what the customer still has to do, and whether kardinal ships it today;
- **`kardinal audit evidence-pack`**: one command that writes every record of a period, with a
  manifest of hashes and the mapping, as a single archive;
- the **user page** that publishes the mapping, limited to what is shipped.

kardinal is not a GRC tool and does not certify anything. It produces evidence for the change
part of the controls; the customer's policies, reviews and other systems cover the rest.

## 2. Where the evidence lives today

| Record | Kind / field | What it proves | Written by | Kept for |
|---|---|---|---|---|
| Promotion lifecycle | `AuditEvent` actions `PromotionStarted`, `PromotionSucceeded`, `PromotionFailed`, `PromotionSuperseded`, `PromotionRejected`, `RollbackStarted`, `RollbackSucceeded` | what changed where, when, and the outcome | PromotionStep reconciler (outbox) | until AuditEvent retention (`--audit-retention-max-age`, `--audit-retention-max-per-pipeline`) deletes it; immutable (`self == oldSelf`) |
| Gate results | `AuditEvent` `GateEvaluated` (every flip), `PolicyGate.status.{ready,reason,lastEvaluatedAt}` | which rule held or passed the change | PolicyGate reconciler | AuditEvent: as above; gate instance: while its Bundle exists |
| Approvals | `Approval.spec.{user,groups,decision,comment,bundleUID}`; `PolicyGate.status.approvals[].{counted,reason}`; `AuditEvent` `ApprovalRecorded`, `ApprovalRevoked` | who approved, and whether it counted | the approver (admission-checked), PolicyGate reconciler | Approval: owned by its Bundle; AuditEvent: as above |
| Overrides | `PolicyGate.spec.overrides[].{createdBy,reason,expiresAt}`, `status.overrides[].verified`; `AuditEvent` `GateOverridden` | emergency changes, by whom, why, for how long | the overrider (admission-checked), PolicyGate reconciler | as above |
| Rejections | `Bundle.spec.rejected.by`; `AuditEvent` `BundleRejected` | who stopped a version | the rejector (admission-checked), Bundle reconciler | as above |
| Holds | `Pipeline.spec.holds[]`; `AuditEvent` `HoldCreated`, `HoldReleased` | an environment kept on a rollback | Pipeline reconciler | as above |
| Creator | annotation `kardinal.io/created-by` on the Bundle; `Bundle.spec.provenance.{author,commitSHA,ciRunURL}` | who created the version and from what | `<release>-bundle-creator` policy pins the creator | while the Bundle exists (Pipeline `historyLimit`, default 50) |
| Identity checks | `ValidatingAdmissionPolicy` `<release>-approvals`, `-gate-overrides`, `-bundle-rejection`, `-bundle-creator`, `-graph-objects`, `-scoped-writes`, `-hold-writes` | names in records cannot be forged | the chart | as installed |
| PR evidence | the PR body (gates, results, provenance) and the `kardinal/gates` status | review in the SCM | PromotionStep reconciler | in the SCM |

From v0.12 (accepted specs, implementation in review):

| Record | Kind / field | Spec |
|---|---|---|
| Actor and sponsor | `spec.actor` on Bundle, Approval, override; `AuditEvent.spec.actor`; `APIWrite` AuditEvents | 17-actors §4, §7 |
| Agent registry | `AgentIdentity` (sponsor, scope, autonomy, suspended), `AgentIndex` | 17-actors §3, §5 |
| SoD for agents | the one-party rule in `excludeAuthor`; `kardinal-agents-*` gates; agent approvals opt-in | 18-actor-policy §4–§7 |
| Freezes | `ChangeWindow.spec.freeze` (who froze, exemptions with who and why); `AuditEvent` `FreezeExempted` | 18-actor-policy §9 |
| Verdicts | `Verdict` (pass/fail by a verifier, with evidence) | 19-verdicts |

From v0.13 (*planned*, specs in review): signed `PromotionReceipt` per promotion per
environment (21-receipts), verified attestations and commit signatures on `ImageVerification`
(20-attestations).

Two facts shape the evidence pack:

- **Records are deleted.** AuditEvents by retention, Bundles by `historyLimit` (and their
  Approvals and gate instances with them, through owner references). The cluster is a working
  store, not an archive. The AuditEvent is the record meant to outlive the Bundle.
- **Records are mutable by cluster admins.** Admission pins who wrote them and the CRDs freeze
  them after creation, but someone with `delete` on `auditevents` can remove one. Tamper
  evidence for a period needs either a copy taken out of the cluster in time (SIEM export) or
  signed receipts (21-receipts).

## 3. The control mapping

Status: **shipped** (on main), **v0.12** (accepted spec, in review), **planned** (v0.13 spec).
"Customer" is what kardinal cannot do for you.

### 3.1 SOC 2 — CC8.1 (change management)

*Requirement:* changes to infrastructure and software are authorised, designed, tested,
approved and implemented under control; points of focus include tracking changes, approving
them, segregating the duties of developers and approvers, and handling emergency changes.

| Point of focus | kardinal record | Status |
|---|---|---|
| Changes are tracked | `AuditEvent` promotion actions per Bundle and environment; Bundle `provenance` | shipped |
| Changes are approved before production | approval gates (`spec.approval`), `Approval` objects, `status.approvals`, `ApprovalRecorded`; `pr-review` environments with the `kardinal/gates` status | shipped |
| Developer and approver are different people | `approval.excludeAuthor` against the admission-pinned `kardinal.io/created-by`; for agents, the one-party rule | shipped; agents: v0.12 |
| Emergency changes are controlled | `GateOverridden` (who, why, capped expiry), freeze exemptions `FreezeExempted` | shipped; freezes: v0.12 |
| Changes are tested before release | health checks and verification (PromotionStep `Verified`), MetricChecks, upstream soak gates; Verdicts | shipped; Verdicts: v0.12 |
| Evidence is tamper-evident | signed receipts | planned |

*Customer:* the change policy itself, the review of the evidence, changes that bypass kardinal
(direct git pushes: protect the branches, require `kardinal/gates`), access reviews of who holds
the approver role.

### 3.2 ISO/IEC 27001:2022 — Annex A 8.32 (change management)

*Requirement:* changes to information processing facilities and information systems are subject
to change management procedures.

| Element | kardinal record | Status |
|---|---|---|
| The procedure is enforced, not advisory | Pipeline + PolicyGates: a change reaches an environment only through a PromotionStep the gates allow | shipped |
| Planned, approved, recorded | as CC8.1 above | shipped |
| Rollback | `RollbackStarted`/`RollbackSucceeded`, holds | shipped |
| Change windows | `ChangeWindow` blackout/recurring; freezes | shipped; freezes: v0.12 |

*Customer:* the documented procedure, the risk assessment of a change, communication to
affected parties.

### 3.3 ISO/IEC 27001:2022 — Annex A 5.3 (segregation of duties)

*Requirement:* conflicting duties and areas of responsibility are segregated.

| Element | kardinal record | Status |
|---|---|---|
| Author is not approver | `excludeAuthor`; `status.approvals[].reason` names "the version's author" | shipped |
| An agent and the person accountable for it count as one | the one-party rule; `Approval.spec.actor`; `AgentIdentity.spec.sponsor` | v0.12 |
| Distinct roles | the chart's user roles (`<release>-viewer`, `-promoter`, `-approver`, `-admin`); approvers by `allowedUsers`/`allowedGroups` | shipped |
| Quorum | `approval.required: N` distinct parties | shipped (parties: v0.12) |

*Customer:* who is granted which role, and the access review.

### 3.4 NIST SP 800-53 Rev. 5

| Control | Requirement (paraphrase) | kardinal record | Status |
|---|---|---|---|
| **CM-3** Configuration change control | determine the types of controlled changes; review and approve them with their security impact in mind; document decisions; implement approved changes; retain records; monitor and review change activity | the Pipeline and gates define controlled changes; approvals and gate results document the decision; PromotionStep implements it; AuditEvents retain it; `kardinal audit summary` / the evidence pack review it | shipped (record retention: as configured) |
| **CM-3(2)** Testing, validation and documentation | test changes before implementing them | health checks, MetricChecks, soak gates, Verdicts | shipped; Verdicts v0.12 |
| **CM-5** Access restrictions for change | define, approve and enforce access restrictions for changes | Kubernetes RBAC with the chart roles; virtual subresources (`pipelines/pause`, `policygates/override`, `bundles/reject`); `<release>-scoped-writes`, `-graph-objects` policies; AgentIdentity scope | shipped; agent scope v0.12 |
| **CM-5(1)** Automated access enforcement and audit records | enforce access restrictions automatically and keep audit records of enforcement | ValidatingAdmissionPolicies (deny, not warn); `APIWrite` AuditEvents for UI and Bundle API writes, refusals included | shipped; `APIWrite` v0.12 |
| **AC-5** Separation of duties | identify duties needing separation and define access to support it | `excludeAuthor`, one-party rule, user roles | shipped; agents v0.12 |

*Customer:* the configuration management plan, the change control board's charter, the
system's other components.

### 3.5 DORA — Regulation (EU) 2022/2554

The request named Article 17. In the Regulation, **Article 17 is the ICT-related incident
management process** (detect, manage, record and notify incidents), not change management.
Change management is in **Article 9(4)(e)** (documented policies, procedures and controls for
ICT change management), detailed in the RTS on the ICT risk management framework (Commission
Delegated Regulation (EU) 2024/1774), whose article on ICT change management is, to our
reading, its Article 17 (verify against the standard). Both are mapped:

| Provision | Requirement (paraphrase) | kardinal record | Status |
|---|---|---|---|
| Reg. **Art. 9(4)(e)**; RTS 2024/1774 change management | changes are recorded, tested, assessed and approved in a controlled way, with emergency changes handled and documented | as CC8.1 and CM-3 | shipped |
| Reg. **Art. 17** incident management | incidents are detected, logged, classified and followed up | not kardinal's job. kardinal contributes: the change record an incident investigation needs (what changed where, by whom, when), the rollback record, the stop-the-line freeze with who froze and why | partly: rollback shipped, freezes v0.12 |

*Customer:* incident classification and reporting to the authority (Art. 18–19), the ICT risk
management framework.

### 3.6 EU AI Act — Regulation (EU) 2024/1689

These articles bind providers and deployers of the AI systems the Act names (Art. 12 and 14:
high-risk systems; Art. 50: systems that interact with people or generate content). A coding
agent that changes software is usually none of these. kardinal does not make anyone compliant
with the AI Act; it gives an organisation that runs agents the records these articles ask of
AI systems, applied to the agents' changes. The user page says so in these words.

| Article | Requirement (paraphrase) | kardinal record | Status |
|---|---|---|---|
| **Art. 12** Record-keeping | high-risk AI systems allow automatic recording of events (logs) over their lifetime, to identify risks and support monitoring | every change an agent makes is an AuditEvent with `actor.kind: agent`, its sponsor and asserted model/session; `kardinal audit export` | v0.12 |
| **Art. 14** Human oversight | people can oversee, understand, override and stop the system ("stop" button, Art. 14(4)(e)) | `agents: approve` (a person approves), agent autonomy, suspension of an AgentIdentity, `kardinal freeze --agents`, stop-the-line | v0.12 |
| **Art. 50** Transparency | people are told when they interact with AI; AI-generated content is marked in a machine-readable way | the actor stamp marks each agent-made version and decision machine-readably (`spec.actor.kind: agent`), shown in the PR evidence and UI; signed commit attribution | v0.12; commit signatures planned (20-attestations) |

*Customer:* deciding whether the Act applies to its systems, its own AI system documentation.

### 3.7 ISO/IEC 42001:2023 (AI management system)

Clause and Annex A numbering below must be verified against the standard before the user page
ships.

| Area (Annex A, verify numbering) | kardinal record | Status |
|---|---|---|
| A.3.2 AI roles and responsibilities | `AgentIdentity.spec.sponsor` (an accountable person per agent), scope | v0.12 |
| A.6.2.8 recording of event logs of AI systems | agent AuditEvents with actor | v0.12 |
| A.9 use of AI systems (responsible use, human oversight in operation) | autonomy per environment, opt-in agent approvals, freezes, suspension | v0.12 |

*Customer:* the AI management system itself: policy, risk and impact assessments, objectives.

## 4. `kardinal audit evidence-pack`

```
kardinal audit evidence-pack --since 2026-07-01 [--until 2026-10-01] [-A | -n <ns>]
    [--pipeline <name>] [--environment <env>] [-o pack.tar.gz]
```

### 4.1 What it contains

```
evidence-pack/
  manifest.json            # what, when, who exported, filters, per-file sha256, counts, coverage
  controls.json            # the mapping of section 3, each control -> files and record kinds
  auditevents.ndjson       # kardinal audit export --format json, the period
  auditevents.ocsf.ndjson  # the same, OCSF 1.3.0 (17-actors §7)
  approvals.ndjson         # Approvals that still exist
  policygates.ndjson       # gate instances of the period: spec (expression, approval, overrides) and status
  bundles.ndjson           # Bundles: actor, created-by, provenance, rejection, phase, environments
  pipelines.ndjson         # the Pipelines (the procedure: environments, approval modes, agents ceilings)
  policygate-templates.ndjson  # the gate templates in force at export (the policy)
  changewindows.ndjson     # windows and freezes, with exemptions                       (freezes: v0.12)
  agentidentities.ndjson   # the agent registry                                          (v0.12)
  verdicts.ndjson          # Verdicts of the period                                       (v0.12)
  receipts.ndjson          # PromotionReceipts, with their signed envelopes               (planned)
  admission.yaml           # the identity ValidatingAdmissionPolicies and bindings in force
```

Kinds the cluster does not serve (an older chart) are skipped and listed in the manifest's
`coverage.missing`, so a pack never claims evidence it lacks.

### 4.2 Format

| | Format | For | Against |
|---|---|---|---|
| (a) | A directory | Easy to browse | Not one artefact to hand over or hash; a partial copy looks complete |
| (b) | One `.tar.gz` with a manifest | One file, streamable, standard tools; the manifest says what is in it | Needs extraction to browse |
| (c) | A `.zip` | Opens by double-click on every desktop | Random-access format: Go's writer must seek, or keep the directory in memory; no streaming to stdout |
| (d) | An OCI artifact pushed to a registry | Content-addressed, signable with cosign | Needs a registry and push rights; auditors rarely use one |

**Choice: (b)**, written to a file or stdout. The manifest records each file's SHA-256 and the
pack's own digest is printed at the end, so the exporter can note it in the ticket.

### 4.3 Integrity

| | Mechanism | For | Against |
|---|---|---|---|
| (a) | SHA-256 per file in the manifest | Detects corruption and edits after export | Proves nothing about who produced it: whoever edits a file can rewrite the manifest |
| (b) | Sign the manifest (sigstore-go, keyless or a key) | Origin and integrity of the pack | The exporter signs what the cluster held at export; it cannot prove records were not deleted before |
| (c) | Rely on per-promotion signed receipts (21-receipts) inside the pack | Each promotion's record was signed by the controller when it happened; deletion of a record leaves a gap a receipt chain can show | Only promotions, not every AuditEvent; planned |

**Choice: (a) now, (b) and (c) with 21-receipts**, using the same signer the receipt reconciler
uses (`--sign`), so there is one signing configuration. The spec is honest about (a): it is a
checksum, not a signature, and the user page says so.

### 4.4 What period the cluster can still prove

| | Approach | For | Against |
|---|---|---|---|
| (a) | Export what the cluster holds; report coverage | No new dependency | Retention and `historyLimit` may have deleted records of the period |
| (b) | Also read an external archive (a bucket the export wrote to before) | Complete history | A second storage format and credentials in the CLI |
| (c) | Require a SIEM and only export from it | The SIEM is the archive of record | kardinal cannot read every SIEM |

**Choice: (a)**, with coverage stated, not assumed: `manifest.coverage` gives, per kind, the
oldest record found, and the pack warns that retention (`--audit-retention-max-age`,
`--audit-retention-max-per-pipeline`) and Pipeline `historyLimit` may have deleted records
inside the period. A period older than the oldest AuditEvent fails with an error that says to export
continuously with `kardinal audit export` into the SIEM. The user page recommends a daily
`kardinal audit export --since 25h` into the SIEM as the archive of record.

### 4.5 Mapping file

| | Where the mapping lives | For | Against |
|---|---|---|---|
| (a) | The docs page only | Simple | The pack does not say what it evidences |
| (b) | A Go table (`pkg/compliance`) that writes `controls.json` and generates the docs page's tables | One source; the pack and the page cannot disagree; a test checks that every record kind named exists in the scheme | A small package |
| (c) | OSCAL assessment-results | The NIST machine format GRC tools import | Large schema; few kardinal users have an OSCAL pipeline |

**Choice: (b)**; OSCAL output is a follow-up once someone asks for it.

### 4.6 Scale and memory

Each kind is read with paginated List calls (500 per page), written as NDJSON to a temporary
file per kind while its SHA-256 is computed, then appended to the tar stream (a tar entry needs
its size first). Memory is bounded by one page; disk by the largest kind. Gate instances are selected by their Bundle's creation
time inside the period, through the `kardinal.io/bundle` label.

### 4.7 RBAC

The CLI runs with the caller's kubeconfig: read (`get, list`) on the kinds above, in the
namespaces asked for, plus `get` on the admission policies (cluster-scoped). The chart adds a
`<release>-auditor` ClusterRole with exactly those reads and no write, extending the
read-only role in `docs/guides/security.md#rbac-read-only-access-to-audit-records`. Kinds the
caller may not read are listed in `coverage.forbidden`, not silently dropped.

## 5. The user page

`docs/compliance.md`, under the Security section of the nav, published with the
`evidence-pack` command:

- the tables of section 3, generated from `pkg/compliance`, **listing only shipped rows**; a
  row whose feature is not released is left out, not marked "coming";
- what kardinal does not cover, per control (the *Customer* lines);
- how to archive (`kardinal audit export` into a SIEM) and how to produce a pack;
- the honest limits of section 6.

The agentic section links it from "What kardinal enforces today".

## 6. What this does not do

- **It does not certify.** It maps records to controls; an auditor judges them.
- **It cannot prove a record was not deleted** by a cluster admin before export, until signed
  receipts (21-receipts) give each promotion a signed record outside the cluster.
- **Changes that bypass kardinal** (a direct push to the GitOps branch, `kubectl apply` on the
  workload) are invisible to it. Protect the branches and require the `kardinal/gates` status.
- **The AI Act rows are analogies** unless the customer's agent is itself a system the Act
  covers; the page says so.

## 7. Decisions

| # | Question | Options | Choice and why |
|---|---|---|---|
| C1 | Pack format | directory; tar.gz; zip; OCI artifact | **tar.gz + manifest** (4.2): one streamable file, standard tools |
| C2 | Integrity | hashes; signed manifest; signed receipts | **Hashes now, signing with 21-receipts' signer** (4.3); stated as a checksum, not a signature |
| C3 | Period coverage | cluster only, with coverage; external archive; SIEM only | **Cluster, with coverage reported** (4.4); SIEM export recommended as the archive |
| C4 | Mapping source | docs only; Go table; OSCAL | **Go table** (4.5), generating both `controls.json` and the page |
| C5 | DORA "Art. 17" | map Art. 17 as change management; map Art. 9(4)(e) + RTS only; map both, labelled | **Both, labelled** (3.5): Art. 17 is incident management in the Regulation |
| C6 | AI Act framing | claim compliance; omit; map as analogous records with the applicability caveat | **Analogous, with the caveat** (3.6) |
| C7 | Unshipped rows on the user page | show as "coming"; omit; show greyed | **Omit** (5): the docs say only what is shipped |

## 8. Test plan

- **Unit**: `pkg/compliance` table: every record kind and AuditEvent action it names exists in
  the API scheme and the `AuditActions` list; every control has a *Customer* line. The page's
  tables are generated and a test fails when they are stale (as the CLI reference is).
- **CLI**: `evidence-pack` against a fake client: filters, pagination across pages, manifest
  hashes match the files, coverage reports a kind the scheme lacks and a forbidden kind, a
  period older than the oldest AuditEvent fails with the export advice, deterministic order.
- **Live e2e** (core, COUNT=2, coverage rows): a promotion with an approval and an override,
  then a pack: every record of the promotion is in it, hashes verify with `sha256sum -c`, and a
  user bound to `<release>-auditor` only can produce it.
- **Scale**: 100,000 AuditEvents exported with memory bounded by one page (the scale suite's
  profile records the CLI's peak RSS).
