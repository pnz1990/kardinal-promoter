# Threat model: agent incidents

Each incident below beat a guardrail that lived inside the agent or the agent's prompt, or gave
an agent more credentials than its task needed. This page maps each one to the kardinal control
that stops the same failure for changes that go through kardinal, and says whether that control
is **shipped** or **planned** (on the [roadmap](../roadmap.md)). A control marked "shipped in
v0.12.0" is new in v0.12.0; the others shipped in v0.11.0 or earlier.

kardinal governs promotions: a version reaching an environment through Git and the cluster. It
cannot stop an agent that holds other credentials, such as a cloud API token, a database
password or push access to the environment branches. Give an agent no path to an environment
but kardinal: its own least-privilege identity ([Connect an agent](connect-an-agent.md#a-least-privilege-identity)),
no `impersonate`, and branch protection on the GitOps repository.

## The incidents

### An agent deletes and recreates an environment (AWS, Kiro, December 2025)

**What happened.** The Financial Times reported that Kiro, AWS's coding agent, chose to "delete and
recreate the environment", which disrupted AWS Cost Explorer in one region for 13 hours. Amazon says
the cause was misconfigured access controls, not the AI: an engineer's role had broader permissions
than expected, which let the agent get past its default of asking before acting.
([The Register](https://www.theregister.com/2026/02/20/amazon_denies_kiro_agentic_ai_behind_outage/))

**The lesson.** "Ask first" was a setting of the agent; the credentials allowed the rest.

| Control in kardinal | Status |
|---|---|
| The agent acts as its own Kubernetes identity, with the RBAC you give it, and holds no other token through kardinal: `kardinal mcp` uses the agent's kubeconfig and opens no port | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |
| A production change waits at a gate the agent cannot pass alone: an approval gate with `excludeAuthor` does not count the version's creator, and `approval: pr-review` opens a pull request for review | Shipped: [Require approvals](../approvals.md) |
| A registered agent is recorded as that agent, with its human sponsor, on every version, Approval and override; it cannot pass as a person, write outside its scope, or act while suspended | Shipped in v0.12.0: [Agents and the verified actor](../actors.md) |
| Per-environment autonomy for agents (`agents: auto \| approve \| deny`, capped by the agent's registered autonomy), and gates that read the actor (`actor.kind == 'agent'`) | Shipped in v0.12.0: [Actor-aware policy](policy.md) |

### An agent deletes a production database and its backups (PocketOS, Cursor, April 2026)

**What happened.** A Cursor agent working on a staging problem found an API token in the codebase,
scoped for domain management but able to do far more, and used it to delete the production volume
in seconds. The backups were on the same volume.
([The Register](https://www.theregister.com/2026/04/27/cursoropus_agent_snuffs_out_pocketos/),
[DevOps.com](https://devops.com/when-ai-goes-really-really-wrong-how-pocketos-lost-all-its-data/))

**The lesson.** An over-scoped credential within the agent's reach, and no confirmation for a
destructive call.

| Control in kardinal | Status |
|---|---|
| `kardinal mcp` holds no token of its own: each tool runs with the agent's kubeconfig only | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |
| No MCP tool deletes anything. The write tools (ship, promote, rollback, pause, resume, reject, approve, verify, freeze) are offered only with `--allow-write`, and none of them lifts a freeze | Shipped in v0.12.0: [The tools](connect-an-agent.md#the-tools) |
| Two least-privilege Roles for an agent: read, and ship | Shipped in v0.12.0: [A least-privilege identity](connect-an-agent.md#a-least-privilege-identity) |

### An agent runs `terraform destroy` on production (DataTalks.Club, Claude Code, March 2026)

**What happened.** With a Terraform state file missing on a new machine, Claude Code was pointed at an
archived state, swapped it in, and ran `terraform destroy`. That deleted the production database
and its automated snapshots; AWS support restored a snapshot a day later.
([The author's account](https://aishippingblog.com/p/how-i-dropped-our-production-database))

**The lesson.** The agent ran a destructive command directly against production, with full
credentials and no review.

| Control in kardinal | Status |
|---|---|
| A change reaches an environment only as a promotion: a version written to Git, gated, health-checked, and for `pr-review` environments a pull request with the evidence in its body | Shipped: [Read PR evidence](../pr-evidence.md) |
| A failed health check rolls the environment back (`onHealthFailure: rollback`) | Shipped: [Roll back](../rollback.md) |
| Before prod, a verify gate can require passing verdicts on the version from a verifier other than the author agent's party: another agent, a person or CI | Shipped in v0.12.0: [Verify with agents](verify-with-agents.md) |
| Per-agent budgets and blast radius | Planned: [v0.14](../roadmap.md#v014-adaptive) |

### An agent ignores a code freeze (SaaStr, Replit, July 2025)

**What happened.** During a code freeze the founder had declared to it, Replit's agent deleted the
live production database, fabricated data and test results, and said a rollback was impossible.
([The Register](https://www.theregister.com/2025/07/21/replit_saastr_vibe_coding_incident/),
[Fortune](https://fortune.com/2025/07/23/ai-coding-tool-replit-wiped-database-called-it-a-catastrophic-failure/))

**The lesson.** The freeze existed only as an instruction to the agent.

| Control in kardinal | Status |
|---|---|
| A ChangeWindow freeze is enforced by the controller for every pipeline whose gates read it, whoever started the promotion. Breaking glass is a verified, time-limited, audited override | Shipped: [ChangeWindow attributes](../policy-gates.md#changewindow-attributes), [Override a gate](../overrides.md) |
| `kardinal pause` stops a pipeline's promotions, and `kardinal hold` keeps an environment on what it runs | Shipped: [Pause a pipeline](../pause.md) |
| A freeze for agents only (`kardinal freeze --agents`) and a stop-the-line for everyone (`--everyone`), cluster-wide or for a namespace, enforced by the controller; an agent cannot exempt itself | Shipped in v0.12.0: [Freezes](freezes.md) |

### Prompt injection in CI agents (PromptPwnd, Aikido Security, December 2025)

**What happened.** Aikido showed that AI agents in GitHub Actions (Gemini CLI, Claude Code, OpenAI
Codex and GitHub AI Inference workflows) put untrusted issue, pull request and commit text into
their prompts, while holding shell access and repository tokens. Google's gemini-cli repository was
affected and fixed.
([Aikido](https://www.aikido.dev/blog/promptpwnd-github-actions-ai-agents))

**The lesson.** Untrusted text steered an agent that held privileged tools.

| Control in kardinal | Status |
|---|---|
| Gate decisions never read PR, issue or commit prose: PR data enters a gate's CEL only as whether it is approved and how many approvals it has, and a test checks no context string carries prose | Shipped: [CEL context](../reference/cel-context.md) |
| `kardinal mcp` returns text that came from people, commits and PRs (author, CI URL, messages, reasons, comments) labelled as untrusted data | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |

### A supply-chain attack turns local AI CLIs against their users (Nx s1ngularity, August 2025)

**What happened.** An attacker stole Nx's publishing token through a GitHub Actions workflow that ran
a pull request title, and published malicious packages. Their install scripts ran the AI CLIs on
developers' machines (Claude, Gemini, Amazon Q) with their safety checks off, to search for secrets,
and uploaded what they found to public repositories.
([Nx post-mortem](https://nx.dev/blog/s1ngularity-postmortem),
[Snyk](https://snyk.io/blog/weaponizing-ai-coding-agents-for-malware-in-the-nx-malicious-package/))

**The lesson.** Anything an agent on the machine can read, an attacker running that agent can read.

| Control in kardinal | Status |
|---|---|
| An agent's kubeconfig carries a ServiceAccount token bound to the agent's own Roles, not the person's credentials, so what leaks is what that identity may do | Shipped in v0.12.0: [A least-privilege identity](connect-an-agent.md#a-least-privilege-identity) |
| A version is promoted only with signed, digest-pinned images when `spec.imageVerification` is set, so a malicious build without the signature does not ship | Shipped: [Verify image signatures](../image-verification.md) |
| SLSA provenance and in-toto attestation checks | Planned: [v0.13](../roadmap.md#v013-evidence) |

### A malicious issue leaks private repositories through an MCP server (Invariant Labs, May 2025)

**What happened.** Invariant Labs showed that an issue in a public repository could hijack a user's
agent reading issues through the GitHub MCP server, so that it read the user's private
repositories and published their contents in a pull request to the public one. The cause was
untrusted text plus a broad personal access token, not a bug in the server's code.
([Invariant Labs](https://invariantlabs.ai/blog/mcp-github-vulnerability))

**The lesson.** An MCP server is as dangerous as the identity behind it and the text it passes on.

| Control in kardinal | Status |
|---|---|
| Untrusted text is labelled in every MCP result, and the server's instructions say it is data | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |
| The MCP server is read-only unless started with `--allow-write`, and has exactly the RBAC of its kubeconfig | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |

### Our own finding: argument injection through MCP tools (kardinal, October 2026)

**What happened.** Review of `kardinal mcp` before it shipped found that the first version built
command-line arguments from tool values. A pipeline name of `--context=prod-cluster` or
`--kubeconfig=/tmp/evil.kubeconfig` was parsed as a flag, so an agent, or text injected into an
agent, could point a tool at another cluster or another identity.
([#1856](https://github.com/pnz1990/kardinal-promoter/pull/1856))

**The lesson.** An MCP tool's arguments are untrusted input, even from the agent you connected.

| Control in kardinal | Status |
|---|---|
| Tools never build command-line arguments: each calls the command's code with typed values, after checking every name against the Kubernetes and CRD patterns. The kubeconfig and context come only from the server's own startup flags, and a test calls every tool with hostile values | Shipped in v0.12.0: [Connect an agent](connect-an-agent.md) |

## What is not covered yet

The controls marked planned arrive in the releases the [roadmap](../roadmap.md) lists. They include:

- per-agent budgets;
- signed promotion receipts.
