# CLI Reference

<!-- AUTO-GENERATED — do not edit by hand.
     Run: go run ./hack/gen-cli-docs/main.go to regenerate. -->

!!! note "Auto-generated"
    Generated from the kardinal CLI source. Every command is documented.
    See [detailed reference pages](reference/cli/kardinal.md) for flags and examples.

| Command | Description |
|---|---|
| [`kardinal approve`](reference/cli/kardinal-approve.md) | Approve (or reject) the version waiting for an environment's approval gates |
| [`kardinal audit`](reference/cli/kardinal-audit.md) | Audit log commands: view and summarize promotion events |
| [`kardinal audit evidence-pack`](reference/cli/kardinal-audit-evidence-pack.md) | Write the records of a period, with a manifest of hashes and the control mapping, as one tar.gz |
| [`kardinal audit export`](reference/cli/kardinal-audit-export.md) | Write AuditEvents as JSON lines or OCSF API Activity events, for a SIEM |
| [`kardinal audit summary`](reference/cli/kardinal-audit-summary.md) | Aggregate promotion metrics from AuditEvent records |
| [`kardinal budget`](reference/cli/kardinal-budget.md) | Show an agent's budgets: what it has used, what waits, and the exemptions |
| [`kardinal completion`](reference/cli/kardinal-completion.md) | Generate shell completion scripts |
| [`kardinal create`](reference/cli/kardinal-create.md) | Create kardinal resources |
| [`kardinal create bundle`](reference/cli/kardinal-create-bundle.md) | Create a Bundle to trigger promotion through a Pipeline |
| [`kardinal dashboard`](reference/cli/kardinal-dashboard.md) | Open the kardinal UI dashboard in a browser |
| [`kardinal delete`](reference/cli/kardinal-delete.md) | Delete kardinal resources |
| [`kardinal delete bundle`](reference/cli/kardinal-delete-bundle.md) | Delete a Bundle by name |
| [`kardinal diff`](reference/cli/kardinal-diff.md) | Show what changes between two versions (Bundles) |
| [`kardinal doctor`](reference/cli/kardinal-doctor.md) | Run pre-flight checks to verify the cluster is correctly configured |
| [`kardinal freeze`](reference/cli/kardinal-freeze.md) | Freeze agents, or stop the line for everyone; with no flags, list the freezes |
| [`kardinal get`](reference/cli/kardinal-get.md) | Display one or more kardinal resources |
| [`kardinal get auditevents`](reference/cli/kardinal-get-auditevents.md) | List AuditEvent records: the immutable promotion event log |
| [`kardinal get bundles`](reference/cli/kardinal-get-bundles.md) | List Bundles, optionally filtered by pipeline name |
| [`kardinal get pipelines`](reference/cli/kardinal-get-pipelines.md) | List Pipelines |
| [`kardinal get steps`](reference/cli/kardinal-get-steps.md) | List PromotionSteps for a pipeline |
| [`kardinal get subscriptions`](reference/cli/kardinal-get-subscriptions.md) | List Subscriptions (passive artifact watchers) |
| [`kardinal history`](reference/cli/kardinal-history.md) | Show a pipeline's promotion history, newest first |
| [`kardinal hold`](reference/cli/kardinal-hold.md) | Keep an environment on what runs there now |
| [`kardinal inbox`](reference/cli/kardinal-inbox.md) | List what waits for a person: on you first, then on others |
| [`kardinal init`](reference/cli/kardinal-init.md) | Write a Pipeline for your GitOps repository, from flags or a few questions |
| [`kardinal logs`](reference/cli/kardinal-logs.md) | Show promotion step execution logs for a pipeline |
| [`kardinal mcp`](reference/cli/kardinal-mcp.md) | Serve kardinal to an AI agent over MCP (stdio) |
| [`kardinal metrics`](reference/cli/kardinal-metrics.md) | Show promotion metrics (DORA-style) for a pipeline |
| [`kardinal override`](reference/cli/kardinal-override.md) | Force-pass a PolicyGate with a mandatory audit record |
| [`kardinal pause`](reference/cli/kardinal-pause.md) | Pause a pipeline, or one fleet: no new promotion steps start |
| [`kardinal policy`](reference/cli/kardinal-policy.md) | Manage and evaluate promotion policy gates |
| [`kardinal policy list`](reference/cli/kardinal-policy-list.md) | List PolicyGates |
| [`kardinal policy simulate`](reference/cli/kardinal-policy-simulate.md) | Simulate PolicyGate evaluation for a hypothetical promotion context |
| [`kardinal policy test`](reference/cli/kardinal-policy-test.md) | Validate PolicyGate YAML syntax and dry-run CEL expressions |
| [`kardinal promote`](reference/cli/kardinal-promote.md) | Promote the version verified upstream into an environment |
| [`kardinal quickstart`](reference/cli/kardinal-quickstart.md) | Install kardinal on a local cluster and promote a first version |
| [`kardinal refresh`](reference/cli/kardinal-refresh.md) | Force re-reconciliation of a Pipeline |
| [`kardinal reject`](reference/cli/kardinal-reject.md) | Reject a version: it is never promoted again, and rollback never picks it |
| [`kardinal resume`](reference/cli/kardinal-resume.md) | Resume a paused pipeline, or a paused fleet |
| [`kardinal rollback`](reference/cli/kardinal-rollback.md) | Roll back an environment to the version before |
| [`kardinal ship`](reference/cli/kardinal-ship.md) | Ship a version: create a Bundle, and with --wait follow it |
| [`kardinal status`](reference/cli/kardinal-status.md) | Show controller health or per-pipeline in-flight promotion details |
| [`kardinal unfreeze`](reference/cli/kardinal-unfreeze.md) | Lift a freeze |
| [`kardinal unhold`](reference/cli/kardinal-unhold.md) | Release an environment's hold (kardinal hold, or rollback --hold) |
| [`kardinal validate`](reference/cli/kardinal-validate.md) | Validate kardinal YAML before applying to the cluster |
| [`kardinal verify`](reference/cli/kardinal-verify.md) | Record a verdict on the version running in an environment (verify gates) |
| [`kardinal verify list`](reference/cli/kardinal-verify-list.md) | List the verdicts on a version, and how its verify gates count them |
| [`kardinal verify-receipt`](reference/cli/kardinal-verify-receipt.md) | Verify a signed promotion receipt: its signature, subject, environment and levels |
| [`kardinal version`](reference/cli/kardinal-version.md) | Print the CLI, controller, and kro (Graph) versions |
| [`kardinal why`](reference/cli/kardinal-why.md) | Say why an environment waits, and what moves it on |

For full flag documentation, examples, and output formats, see the
[individual command pages](reference/cli/kardinal.md).
