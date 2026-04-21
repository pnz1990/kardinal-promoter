# CLI Reference

<!-- AUTO-GENERATED — do not edit by hand.
     Run: go run ./hack/gen-cli-docs/main.go to regenerate.
     Design ref: docs/design/41-published-docs-freshness.md -->

!!! note "Auto-generated"
    Generated from the kardinal CLI source. Every command is documented.
    See [detailed reference pages](reference/cli/) for flags and examples.

| Command | Description |
|---|---|
| [`kardinal approve`](reference/cli/kardinal-approve.md) | Approve a Bundle for promotion, bypassing upstream gate requirements |
| [`kardinal audit`](reference/cli/kardinal-audit.md) | Audit log commands — view and summarize promotion events |
| [`kardinal audit summary`](reference/cli/kardinal-audit-summary.md) | Aggregate promotion metrics from AuditEvent records |
| [`kardinal completion`](reference/cli/kardinal-completion.md) | Generate shell completion scripts |
| [`kardinal create`](reference/cli/kardinal-create.md) | Create kardinal resources |
| [`kardinal create bundle`](reference/cli/kardinal-create-bundle.md) | Create a Bundle to trigger promotion through a Pipeline |
| [`kardinal dashboard`](reference/cli/kardinal-dashboard.md) | Open the kardinal UI dashboard in a browser (Kargo parity) |
| [`kardinal delete`](reference/cli/kardinal-delete.md) | Delete kardinal resources |
| [`kardinal delete bundle`](reference/cli/kardinal-delete-bundle.md) | Delete a Bundle by name |
| [`kardinal diff`](reference/cli/kardinal-diff.md) | Show artifact differences between two Bundles |
| [`kardinal doctor`](reference/cli/kardinal-doctor.md) | Run pre-flight checks to verify the cluster is correctly configured |
| [`kardinal explain`](reference/cli/kardinal-explain.md) | Explain the current state of a promotion pipeline |
| [`kardinal get`](reference/cli/kardinal-get.md) | Display one or more kardinal resources |
| [`kardinal get auditevents`](reference/cli/kardinal-get-auditevents.md) | List AuditEvent records — immutable promotion event log |
| [`kardinal get bundles`](reference/cli/kardinal-get-bundles.md) | List Bundles, optionally filtered by pipeline name |
| [`kardinal get pipelines`](reference/cli/kardinal-get-pipelines.md) | List Pipelines |
| [`kardinal get steps`](reference/cli/kardinal-get-steps.md) | List PromotionSteps for a pipeline |
| [`kardinal get subscriptions`](reference/cli/kardinal-get-subscriptions.md) | List Subscriptions (passive artifact watchers) |
| [`kardinal history`](reference/cli/kardinal-history.md) | Show Bundle promotion history for a pipeline |
| [`kardinal init`](reference/cli/kardinal-init.md) | Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo |
| [`kardinal logs`](reference/cli/kardinal-logs.md) | Show promotion step execution logs for a pipeline (Kargo parity) |
| [`kardinal metrics`](reference/cli/kardinal-metrics.md) | Show promotion metrics (DORA-style) for a pipeline |
| [`kardinal override`](reference/cli/kardinal-override.md) | Force-pass a PolicyGate with a mandatory audit record (K-09) |
| [`kardinal pause`](reference/cli/kardinal-pause.md) | Pause a pipeline, preventing new promotions from starting |
| [`kardinal policy`](reference/cli/kardinal-policy.md) | Manage and evaluate promotion policy gates |
| [`kardinal policy list`](reference/cli/kardinal-policy-list.md) | List PolicyGates |
| [`kardinal policy simulate`](reference/cli/kardinal-policy-simulate.md) | Simulate PolicyGate evaluation for a hypothetical promotion context |
| [`kardinal policy test`](reference/cli/kardinal-policy-test.md) | Validate PolicyGate YAML syntax and dry-run CEL expressions |
| [`kardinal promote`](reference/cli/kardinal-promote.md) | Trigger promotion of a pipeline to a specific environment |
| [`kardinal refresh`](reference/cli/kardinal-refresh.md) | Force re-reconciliation of a Pipeline (Kargo parity) |
| [`kardinal resume`](reference/cli/kardinal-resume.md) | Resume a paused pipeline |
| [`kardinal rollback`](reference/cli/kardinal-rollback.md) | Roll back a pipeline environment to a previous Bundle |
| [`kardinal status`](reference/cli/kardinal-status.md) | Show controller health or per-pipeline in-flight promotion details |
| [`kardinal validate`](reference/cli/kardinal-validate.md) | Validate Pipeline and PolicyGate YAML before applying to the cluster |
| [`kardinal version`](reference/cli/kardinal-version.md) | Print the CLI, controller, and graph versions |

For full flag documentation, examples, and output formats, see the
[individual command pages](reference/cli/).
