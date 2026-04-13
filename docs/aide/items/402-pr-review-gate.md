# Item 402: K-08 — bundle.pr().isApproved() CEL function

> Queue: queue-017
> Issue: #452
> Priority: high
> Size: s
> Milestone: v0.6.0 — Pipeline Expressiveness

## Summary

New CEL functions on the `bundle` context: `bundle.pr(stageName).isApproved()` and `bundle.pr(stageName).hasMinReviewers(n)`. Read existing PRStatus CRD via the BundleContext. No new CRD.

## Acceptance Criteria

- [ ] `bundle.pr("staging").isApproved()` returns true if PRStatus.status.approved == true for the staging env
- [ ] `bundle.pr("staging").hasMinReviewers(2)` returns true if PRStatus.status.reviewerCount >= 2
- [ ] Returns false (not error) when no PRStatus exists for the stage
- [ ] `BundleContext` extended with PR accessor reading from Bundle.status.environments[].prStatusRef
- [ ] Unit tests cover: approved/not-approved, reviewers count, missing PRStatus
- [ ] `docs/policy-gates.md` updated with `bundle.pr()` function docs and examples

## Package

`pkg/cel/context.go` — extend BundleContext with PR accessor
`pkg/cel/evaluator.go` — register pr() functions
