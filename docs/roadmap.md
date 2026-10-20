# Roadmap

This page lists what is planned for kardinal-promoter. For what ships today, see the
[home page](index.md) and the [comparison](comparison.md); the [changelog](changelog.md) lists
what changed in each release.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub issue](https://github.com/pnz1990/kardinal-promoter/issues) if a feature matters to your use case.

---

## Currently available: v0.12.1

v0.12.1 is the latest release: v0.12.0 with one fix (#2132). Besides everything earlier releases
shipped (see the [changelog](changelog.md)), v0.12.0 makes kardinal agent-ready:

- **Actors.** An `AgentIdentity` object registers each agent with a required human sponsor and
  the namespaces, pipelines and environments it may act in. Versions, approvals and overrides
  record the verified actor and sponsor, and AuditEvents name them:
  [Agents and the verified actor](actors.md).
- **Policy that knows the actor.** Gates read who created a version (`actor.*`, `agent.*`), and an
  environment can say `agents: auto | approve | deny`. Approvals from agents count only where a
  gate opts in, never for the agent's own work or its sponsor's: [Actor-aware policy](agentic/policy.md).
- **Agents as verifiers.** A `Verdict` object records an agent's pass, fail or inconclusive result
  for a version in an environment, and a verification gate waits for N verdicts from eligible
  agents: [Verify with agents](agentic/verify-with-agents.md).
- **An agent freeze.** `kardinal freeze --agents` stops promotions by agents, and
  `kardinal freeze --everyone` stops the line for everyone: [Freezes](agentic/freezes.md).
- **Local MCP.** `kardinal mcp` over stdio, with the caller's kubeconfig: read tools by default,
  write tools only with `--allow-write`, and a policy pre-flight that says whether a version
  would be allowed into an environment now: [Connect an agent](agentic/connect-an-agent.md).
- **Fleets.** Pause and roll back a whole fleet; `maxUnavailable` as a percentage:
  [Pause one fleet](pause.md#pause-one-fleet), [Roll back a fleet](rollback.md#roll-back-a-fleet).
- **Who did it, in the audit trail.** AuditEvents name who paused, resumed, froze, lifted a
  freeze, released a hold and rejected a version, and who merged each `pr-review` pull request:
  [Audit logging](guides/security.md#audit-logging).
- **Sync on promotion, scoped.** The controller asks Argo CD or Flux to sync only the Pipeline's
  own objects (`health.refresh.namespaces` admits more), and `health.refreshTarget` names the
  object to sync for Rollouts, Canaries and Deployments: [Sync on promotion](health-adapters.md#sync-on-promotion).

---

## Planned

Plans change with feedback; the [changelog](changelog.md) says what shipped.

### v0.13: evidence

v0.13.0 is not released yet; v0.12.1 is the latest release.

**Merged, ships in v0.13.0.** These are on main, and this site already documents them:

- **Signed promotion receipts.** With `receipts.enabled` (off by default), each promotion into
  each environment gets a signed in-toto receipt: the version, the actor and sponsor, every gate
  with its result, the approvals and verdicts, the checks and the PR. Keyless or key signing,
  OCI referrer and Rekor sinks, and `kardinal verify-receipt` to check one offline:
  [Signed promotion receipts](receipts.md).
- **Attestations.** Verified SLSA provenance (with the build level you grant each builder),
  vulnerability scans with OpenVEX, and any in-toto predicate, read by gates as
  `attestations.*`: [Attestations](image-verification.md#attestations).
- **A compliance mapping and an evidence pack.** Which kardinal record evidences which control
  of SOC 2, ISO/IEC 27001, NIST SP 800-53, DORA, the EU AI Act and ISO/IEC 42001, and
  `kardinal audit evidence-pack`, a period's records in one archive for an auditor:
  [Map to compliance controls](compliance.md).
- **Change requests.** A gate waits for an approved ServiceNow change or Jira issue, which
  kardinal opens for the promotion or links by its key: [Change Requests](change-requests.md).
- **Approval fields.** An approval gate can require what an approve carries, such as a change
  ticket ID or an acknowledgement: [Approval fields](approvals.md#approval-fields).

The changelog's [Unreleased](changelog.md#unreleased) section lists every change since v0.12.0,
including those to rollbacks, gates and the UI.

**Still planned for v0.13:**

- Keyless (gitsign) commit signatures, mapped to the agent that signed. Commit signatures the
  SCM verified (`commits.requireSigned`) already ship:
  [Signed commits](image-verification.md#signed-commits).
- Receipts signed with AWS KMS or Google Cloud KMS.
- A maximum age for attestations, so a gate can refuse a stale scan.

### v0.14: adaptive

- Per-agent budgets and blast radius.
- A change risk score that gates and soak times can read.
- Earned autonomy: each agent's change failure rate and rollbacks in policy.
- An error-budget gate on SLOs.
- Custom promotion steps registered by admins.
- DORA metrics split by human and agent.

### Also planned

- Move to kro v0.10.0 once it is released (kardinal targets v0.10.0-rc.0).

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| Running SAST and vulnerability scanners | Trivy, Grype, govulncheck in CI. From v0.13.0 (not released yet) kardinal can require a signed Trivy or Grype scan of each image, and a gate can judge its findings after VEX: [Attestations](image-verification.md#attestations) |
| Code coverage gating | Codecov, SonarQube in CI |
