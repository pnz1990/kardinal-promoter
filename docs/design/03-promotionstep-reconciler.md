# 03: PromotionStep Reconciler

> Status: Outline
> Depends on: 01-graph-integration, 02-pipeline-to-graph-translator
> Blocks: nothing (leaf node, but the workhorse)

The reconciler that does the actual promotion work. Watches PromotionStep CRs created by the Graph controller.

## Scope

- State machine diagram: every state, every transition, every error path
  - Pending -> GitWriting -> PROpen -> WaitingForMerge -> HealthChecking -> Verified / Failed
- Integration points: which pluggable interface is called at which state
  - GitWriting: scm.GitClient (clone/push) + update.Strategy (kustomize)
  - PROpen: scm.SCMProvider (CreatePR, UpdatePRComment)
  - WaitingForMerge: scm.SCMProvider (GetPRStatus, triggered by webhook)
  - HealthChecking: health.Adapter (Check) + delivery.Delegate (Watch, if configured)
- Evidence collection: what data is captured at each stage
  - PR URL, merge timestamp, verification timestamp, gate results, approver list, metrics snapshot
- Evidence copying: when and how evidence is written from PromotionStep status to Bundle status
- Idempotency: what happens if the reconciler runs twice at the same state (must be safe)
- PR lifecycle: creation (branch naming, commit message, PR body template), comment updates on gate changes, merge detection via webhook handler
- Health verification: how the adapter is called, timeout handling, failure escalation to Failed state
- Rollback trigger: when PromotionStep goes to Failed, how does it signal the need for rollback PRs
- approval: auto vs approval: pr-review branching (direct push vs PR creation)
