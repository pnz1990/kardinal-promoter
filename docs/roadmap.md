# Roadmap

This page lists what is planned for kardinal-promoter. For what ships today, see the
[home page](index.md) and the [comparison](comparison.md); the [changelog](changelog.md) lists
what changed in each release.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub issue](https://github.com/pnz1990/kardinal-promoter/issues) if a feature matters to your use case.

---

## Planned

Everything in this section is planned and not in kardinal yet. Plans change with feedback; the
[changelog](changelog.md) says what shipped.

### v0.12: agent-ready

- **Actors.** An `AgentIdentity` object registers each agent with a required human sponsor and
  the namespaces, pipelines and environments it may act in. Versions, approvals and overrides
  record the verified actor and sponsor, and AuditEvents name them. On main, unreleased:
  [Agents and the verified actor](actors.md).
- **Policy that knows the actor.** Gates read who created a version (`actor.*`, `agent.*`), and an
  environment can say `agents: auto | approve | deny`. Approvals from agents count only where a
  gate opts in, never for the agent's own work or its sponsor's.
- **Agents as verifiers.** A `Verdict` object records an agent's pass, fail or inconclusive result
  for a version in an environment, and a verification gate waits for N verdicts from eligible
  agents. On main, unreleased: [Verify with agents](agentic/verify-with-agents.md).
- **An agent freeze.** `kardinal freeze --agents` stops promotions by agents, and a cluster-wide
  stop-the-line stops everyone.
- **Local MCP.** `kardinal mcp` over stdio, with the caller's kubeconfig: read tools by default,
  write tools only with `--allow-write`, and a policy pre-flight that says whether a version
  would be allowed into an environment now. On main, unreleased: [Connect an agent](agentic/connect-an-agent.md).
- **Fleets.** Pause and roll back a whole fleet; `maxUnavailable` as a percentage. On main,
  unreleased: [Pause one fleet](pause.md#pause-one-fleet), [Roll back a fleet](rollback.md#roll-back-a-fleet).
- Move to kro v0.10.0 once it is released (kardinal targets v0.10.0-rc.0).

### v0.13: evidence

- Verify SLSA provenance and in-toto attestations, and keyless (gitsign) commit signatures.
  Commit signatures the SCM verified (`commits.requireSigned`) already ship:
  [Signed commits](image-verification.md#signed-commits).
- A signed promotion receipt for each promotion into each environment: the version, the actor
  and sponsor, every gate with its result, the approvers and verdicts.
- A compliance mapping page and an evidence pack export.
- A change request gate (ServiceNow, Jira), and structured approval fields such as a ticket ID.

### v0.14: adaptive

- Per-agent budgets and blast radius.
- A change risk score that gates and soak times can read.
- Earned autonomy: each agent's change failure rate and rollbacks in policy.
- An error-budget gate on SLOs.
- Custom promotion steps registered by admins.
- DORA metrics split by human and agent.

### Also planned

- `rollback` and `promote --env` without re-running upstream environments

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
