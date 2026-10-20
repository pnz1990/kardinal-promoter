# Roadmap

This page lists what is planned for kardinal-promoter. For what ships today, see the
[home page](index.md) and the [comparison](comparison.md); the [changelog](changelog.md) lists
what changed in each release.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub issue](https://github.com/pnz1990/kardinal-promoter/issues) if a feature matters to your use case.

---

## Currently available: v0.13.0

v0.13.0 is the latest release. Besides everything earlier releases shipped (see the
[changelog](changelog.md)), among them v0.12.0's agent-ready controls (the verified actor,
actor-aware policy, agents as verifiers, agent freezes and the local MCP server:
[Agentic delivery](agentic/index.md)), v0.13.0 adds evidence:

- **Signed promotion receipts.** With `receipts.enabled` (off by default), each promotion into
  each environment gets a signed in-toto receipt: the version, the actor and sponsor, every gate
  with its result, the approvals and verdicts, the checks, the SLSA build level, any freeze
  exemption and the PR. Keyless, key or KMS (AWS, Google Cloud) signing, OCI referrer and Rekor
  sinks, and `kardinal verify-receipt` to check one offline:
  [Signed promotion receipts](receipts.md).
- **Attestations.** Verified SLSA provenance (with the build level you grant each builder),
  vulnerability scans with OpenVEX, and any in-toto predicate, read by gates as
  `attestations.*`, with a maximum age so a gate can refuse a stale scan:
  [Attestations](image-verification.md#attestations).
- **A compliance mapping and an evidence pack.** Which kardinal record evidences which control
  of SOC 2, ISO/IEC 27001, NIST SP 800-53, DORA, the EU AI Act and ISO/IEC 42001, and
  `kardinal audit evidence-pack`, a period's records in one archive for an auditor:
  [Map to compliance controls](compliance.md).
- **Change requests.** A gate waits for an approved ServiceNow change or Jira issue, which
  kardinal opens for the promotion or links by its key: [Change Requests](change-requests.md).
- **Approval fields.** An approval gate can require what an approve carries, such as a change
  ticket ID or an acknowledgement: [Approval fields](approvals.md#approval-fields).
- **Keyless commit signatures.** kardinal verifies a config commit's gitsign (Sigstore) signature
  itself, and names the agent whose workload signed it, which gates read as `commit.*`:
  [Commit signatures with gitsign](image-verification.md#commit-signatures-with-gitsign).

The changelog's v0.13.0 section lists every change since v0.12.0, including those to rollbacks,
gates and the UI.

---

## Planned

Plans change with feedback; the [changelog](changelog.md) says what shipped.

### v0.14: adaptive

- Per-agent budgets and blast radius.
- A change risk score that gates and soak times can read.
- Earned autonomy: each agent's change failure rate and rollbacks in policy.
- An error-budget gate on SLOs.
- Custom promotion steps registered by admins.
- DORA metrics split by human and agent.
- `spec.policyNamespaces` refuses a namespace that has not agreed to share its gates, which
  v0.13.0 only warns about: [Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces).
- A per-namespace list of the repositories receipt referrers may be pushed to, in place of one
  list for the whole controller: [Where receipts are stored](receipts.md#where-receipts-are-stored).

### Also planned

- Move to kro v0.10.0 once it is released (kardinal targets v0.10.0-rc.0).

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| Running SAST and vulnerability scanners | Trivy, Grype, govulncheck in CI. Since v0.13.0 kardinal can require a signed Trivy or Grype scan of each image, and a gate can judge its findings after VEX: [Attestations](image-verification.md#attestations) |
| Code coverage gating | Codecov, SonarQube in CI |
