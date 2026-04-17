# CLI Reference

The `kardinal` CLI communicates with the Kubernetes API server to read and write kardinal-promoter CRDs. It is a single static Go binary with no runtime dependencies beyond a valid kubeconfig.

## Installation

```bash
# Download the latest release
curl -LO https://github.com/pnz1990/kardinal-promoter/releases/latest/download/kardinal-$(uname -s)-$(uname -m)
chmod +x kardinal-*
sudo mv kardinal-* /usr/local/bin/kardinal
```

Verify:
```bash
kardinal version
```

## Commands

### kardinal init

Interactive wizard to generate a Pipeline CRD YAML.

```bash
kardinal init
```

The wizard prompts for application name, namespace, environments, Git repository URL, base branch, and update strategy. It generates a `pipeline.yaml` file ready to apply with `kubectl apply -f pipeline.yaml`.

Options:
- `--stdout`: print generated YAML to stdout instead of writing to a file
- `--output <file>`, `-o <file>`: write to the specified file (default: `pipeline.yaml`)

Example interactive session:

```bash
kardinal init
# Application name [my-app]: nginx-demo
# Namespace [default]: default
# Environments (comma-separated) [test,uat,prod]: test,uat,prod
# Git repository URL: https://github.com/myorg/gitops
# Base branch [main]: main
# Update strategy (kustomize/helm) [kustomize]: kustomize
# Pipeline YAML written to pipeline.yaml
# Apply with: kubectl apply -f pipeline.yaml
```

The last environment automatically gets `approval: pr-review`; earlier environments get `approval: auto`.

### kardinal get pipelines

List all Pipelines with the current Bundle and per-environment status.

```bash
kardinal get pipelines
kardinal get pipelines --watch  # live updates, Ctrl-C to quit
```

Output:
```
PIPELINE     BUNDLE    DEV        STAGING    PROD           AGE
my-app       v1.29.0   Verified   Verified   Promoting      2h
my-api       v3.5.1    Verified   Promoting  Waiting        45m
frontend     v2.1.0    Verified   Verified   Verified       1d
```

Flags:
- `-n, --namespace <ns>`: filter by namespace (default: current context namespace)
- `-A, --all-namespaces`: list across all namespaces
- `-w, --watch`: stream live updates (polls every 2s, Ctrl-C to quit)
- `-o, --output <format>`: output format (`table`, `json`, `yaml`)

### kardinal get steps

Show all PromotionSteps and PolicyGates for a Pipeline's current promotion.

```bash
kardinal get steps <pipeline>
kardinal get steps <pipeline> --watch  # live updates, Ctrl-C to quit
```

Output:
```
ENVIRONMENT   STEP-TYPE             STATE             MESSAGE
dev           kustomize-set-image   Verified          health check passed
staging       kustomize-set-image   Verified          health check passed
prod          kustomize-set-image   WaitingForMerge   PR #144 open
```

Flags:
- `--bundle <version>`: show steps for a specific Bundle (default: latest active)
- `-w, --watch`: stream live updates (polls every 2s, Ctrl-C to quit)

### kardinal get bundles

List Bundles for a Pipeline with provenance and per-environment status.

```bash
kardinal get bundles <pipeline>
```

Output:
```
BUNDLE                   TYPE    PHASE        AGE
nginx-demo-v1-29-0       image   Promoting    2m
nginx-demo-v1-28-0       image   Superseded   1d
nginx-demo-v1-27-0       image   Superseded   3d
```

Flags:
- `--active`: show only active bundles (Promoting/Verified/Failed — excludes Superseded)
- `-o, --output <format>`: output format

### kardinal get auditevents

List AuditEvent records — the immutable promotion event log.

```bash
kardinal get auditevents [--pipeline <name>] [--bundle <name>] [--env <env>] [--limit N]
```

Output:
```
TIMESTAMP          PIPELINE      BUNDLE           ENV    ACTION                OUTCOME
2026-04-17T03:15Z  nginx-demo    nginx-demo-v1    prod   PromotionStarted      Pending
2026-04-17T03:18Z  nginx-demo    nginx-demo-v1    prod   PromotionSucceeded    Success
```

Actions:
- `PromotionStarted` — Bundle started promoting through an environment
- `PromotionSucceeded` — Health check passed; environment reached Verified
- `PromotionFailed` — Step reached Failed state
- `PromotionSuperseded` — Newer Bundle superseded this promotion
- `GateEvaluated` — PolicyGate changed readiness state (on flip only)
- `RollbackStarted` — onHealthFailure=rollback created a rollback Bundle

Flags:
- `--pipeline <name>`: filter by pipeline name
- `--bundle <name>`: filter by bundle name
- `--env <env>`: filter by environment name
- `--limit N`: max results (default: 20, 0 = unlimited)

### kardinal create bundle

Create a Bundle CRD. This is equivalent to `kubectl apply -f bundle.yaml` but with a simpler interface.

```bash
kardinal create bundle <pipeline> \
  --image <reference> \
  [--digest <sha256:...>] \
  [--commit <sha>] \
  [--ci-run <url>] \
  [--author <name>] \
  [--target <environment>] \
  [--label key=value ...]
```

Example:
```bash
kardinal create bundle my-app \
  --image ghcr.io/myorg/my-app:1.29.0 \
  --digest sha256:a1b2c3d4e5f6 \
  --commit abc123def456 \
  --ci-run https://github.com/myorg/my-app/actions/runs/12345 \
  --author "engineer-name"
```

Output:
```
Bundle my-app-v1-29-0-1712567890 created.
  Images: ghcr.io/myorg/my-app:1.29.0
  Commit: abc123d
  Phase:  Available
  Next:   dev (gate: auto)
```

### kardinal promote

Manually trigger promotion by creating a Bundle targeting a specific environment. The Bundle flows through all upstream environments before reaching the target environment. PolicyGates and approval mode apply as configured.

```bash
kardinal promote <pipeline> --env <environment>
```

Example:
```bash
kardinal promote my-app --env prod
```

Output:
```
Promoting my-app to prod: bundle my-app-x7k2p created
Track with: kardinal get bundles my-app
```

### kardinal explain

Show the PolicyGate evaluation trace for a specific environment. This is the primary debugging tool when a promotion is blocked.

```bash
kardinal explain <pipeline> --env <environment>
```

Output:
```
ENVIRONMENT   TYPE            NAME                STATE           EXPRESSION                        REASON
prod          PolicyGate      no-weekend-deploys  Blocked         !schedule.isWeekend               !schedule.isWeekend = false
prod          PolicyGate      staging-soak        Blocked         bundle.upstreamSoakMinutes >= 30  bundle.version=1.29.0: bundle.upstreamSoakMinutes >= 30 = false
prod          PromotionStep   prod                WaitingForMerge                                   PR #42 open
```

Flags:
- `--watch`: continuous output, re-evaluates and reprints when gate states change
- `--bundle <version>`: explain a specific Bundle (default: latest active)
- `--env <env>`: filter by environment

If the environment name is not found, the command prints the available environments:

```
environment "bogus" not found in pipeline "nginx-demo".
Available environments: test, uat, prod
```

### kardinal rollback

Roll back an environment to the previous verified Bundle. This creates a new Bundle targeting the prior version and runs it through the same pipeline.

```bash
kardinal rollback <pipeline> --env <environment>
```

Output:
```
Rolling back my-app in prod: v1.29.0 -> v1.28.0
  Previous verified Bundle: v1.28.0
  PR #145 opened: https://github.com/myorg/gitops-repo/pull/145
  Merge PR #145 to complete rollback (gate: pr-review)
```

Flags:
- `--to <version>`: roll back to a specific version (default: previous verified)
- `--emergency`: add `kardinal/emergency` label to the PR for priority review

### kardinal pause

Pause all promotion activity for a Pipeline by setting `spec.paused: true`.
In-flight PromotionSteps are held at their current state; new promotions will not start.

```bash
kardinal pause <pipeline>
```

Output:
```
Pipeline my-app paused. No new promotions will start.
```

### kardinal resume

Resume a paused Pipeline by setting `spec.paused: false`.

```bash
kardinal resume <pipeline>
```

### kardinal override

Force-pass a PolicyGate with a mandatory audit record (K-09). The gate passes immediately without evaluating CEL until the override expires.

```bash
kardinal override <pipeline> --gate <gate-name> [--stage <env>] --reason "<text>" [--expires-in <duration>]
```

Flags: `--gate` (required), `--reason` (required), `--stage` (empty = all envs), `--expires-in` (default: `1h`).

Example:
```bash
kardinal override my-app --stage prod --gate no-weekend-deploy \
  --reason "P0 hotfix — incident #4521" --expires-in 2h
```

See [Policy Gates — Emergency Overrides](policy-gates.md#emergency-overrides-k-09) for details.

### kardinal history

Show the promotion history for a Pipeline, including which Bundles were promoted to which environments and when.

```bash
kardinal history <pipeline>
```

Output:
```
BUNDLE                  ACTION    ENV     PR    DURATION   TIMESTAMP
nginx-demo-v1-29-0      promote   dev     --    3m         2026-04-09 10:05
nginx-demo-v1-29-0      promote   staging --    8m         2026-04-09 10:10
nginx-demo-v1-29-0      promote   prod    #144  15m        2026-04-09 10:20
nginx-demo-v1-28-0      rollback  prod    #140  5m         2026-04-08 16:30
nginx-demo-v1-28-0      promote   prod    #138  12m        2026-04-07 14:00
```

Flags:
- `--env <environment>`: filter by environment
- `--limit <n>`: number of entries (default: 20)

### kardinal policy list

List all PolicyGate CRDs across scanned namespaces.

```bash
kardinal policy list
```

Output:
```
NAME                 NAMESPACE           SCOPE   APPLIES-TO   RECHECK   READY     LAST-EVALUATED
no-weekend-deploys   platform-policies   org     prod         5m        Block     1m ago
require-uat-soak     platform-policies   org     prod         1m        Block     45s ago
no-bot-deploys       my-team             team    prod         5m        Pass      2m ago
```

Note: shows user-defined template PolicyGates only, not per-bundle Graph instances.
Add `--pipeline <name>` to filter by pipeline.

### kardinal policy test

Validate a PolicyGate YAML file: check CEL syntax, verify referenced context attributes exist in the current phase, and optionally dry-run against a sample context.

```bash
kardinal policy test <file>
```

Output:
```
PolicyGate "no-weekend-deploys" (policy-gates.yaml):
  Expression: !schedule.isWeekend
  Syntax: valid
  Context attributes: schedule.isWeekend (Phase 1)
  Result: PASS
```

### kardinal policy simulate

Simulate a PolicyGate evaluation against a hypothetical context (future time, soak-minutes override, etc.).

```bash
kardinal policy simulate \
  --pipeline my-app \
  --env prod \
  --time "Saturday 3pm"
```

Output (blocked):
```
RESULT: BLOCKED
Blocked by: no-weekend-deploys
Message: "Production deployments are blocked on weekends"
Next window: Monday 00:00 UTC

no-weekend-deploys:   BLOCK   (!schedule.isWeekend = false)
```

Output (passing):
```
RESULT: PASS
no-weekend-deploys:   PASS   (!schedule.isWeekend = true)
staging-soak:         PASS   (bundle.upstreamSoakMinutes >= 30 = true)
```

### kardinal diff

Show the artifact differences between two Bundles.

```bash
kardinal diff <bundle-a> <bundle-b>
```

Output:
```
ARTIFACT                          BUNDLE-A (v1.28.0)    BUNDLE-B (v1.29.0)
ghcr.io/myorg/my-app              1.28.0                1.29.0
  digest                          sha256:def456...       sha256:abc123...
  commit                          def456                 abc123
  author                          dependabot[bot]        engineer-name
```

### kardinal approve

Approve a Bundle for promotion, bypassing upstream gate requirements.

```bash
kardinal approve <bundle-name> [--env <environment>]
```

Approval patches the Bundle with `kardinal.io/approved=true` (and optionally
`kardinal.io/approved-for=<env>`). Useful for hotfix deployments that must skip
the normal upstream soak or gate requirements.

```bash
# Approve for all environments
kardinal approve kardinal-test-app-sha-abc1234

# Approve for a specific environment only
kardinal approve kardinal-test-app-sha-abc1234 --env prod
```

Output:
```
Bundle "kardinal-test-app-sha-abc1234" approved for "prod".
  Label: kardinal.io/approved=true, kardinal.io/approved-for=prod
  To track: kardinal explain kardinal-test-app --env prod
```

Flags:
- `--env <env>`: target environment to approve for (optional)

### kardinal metrics

Show DORA-style promotion metrics for a pipeline.

```bash
kardinal metrics --pipeline <pipeline> [--env <env>] [--days <n>]
```

Computes from CRD history:

```bash
kardinal metrics --pipeline kardinal-test-app --env prod --days 30
```

Output:
```
PIPELINE              ENV    PERIOD   DEPLOY_FREQ   LEAD_TIME     FAIL_RATE   ROLLBACKS
kardinal-test-app     prod   30d      2.1/day       45m avg       3.2%        1
```

| Column | Description |
|---|---|
| `DEPLOY_FREQ` | Successful promotions to the target environment per day |
| `LEAD_TIME` | Average time from Bundle creation to target environment verification |
| `FAIL_RATE` | Percentage of Bundles that reached `Failed` state |
| `ROLLBACKS` | Number of rollback Bundles in the period |

Flags:
- `--pipeline <name>`: pipeline name (required)
- `--env <env>`: target environment for lead time calculation (default: `prod`)
- `--days <n>`: lookback period in days (default: `30`)

### kardinal refresh

Force re-reconciliation of a Pipeline immediately.

```bash
kardinal refresh <pipeline>
```

Adds a `kardinal.io/refresh` annotation to the Pipeline, triggering the controller
to run a reconciliation cycle. Useful to force a retry after a transient error or
to re-evaluate PolicyGates.

```bash
kardinal refresh kardinal-test-app
# Pipeline "kardinal-test-app" refresh requested.
```

### kardinal dashboard

Open the kardinal UI dashboard in a browser.

```bash
kardinal dashboard [--address <url>] [--no-open]
```

Uses port-forwarding to access the controller's embedded UI (port 8082 by default).

```bash
kardinal dashboard             # open in default browser
kardinal dashboard --no-open   # print URL only
# kardinal UI: http://localhost:8082/ui/
```

Flags:
- `--address <url>`: direct URL to the kardinal UI (skip auto-detection)
- `--no-open`: print the URL without opening browser

### kardinal logs

Show promotion step execution logs for a pipeline.

```bash
kardinal logs <pipeline> [--env <env>] [--bundle <bundle>]
```

For each active PromotionStep, shows state, message, and outputs (branch, PR URL).

```bash
kardinal logs kardinal-test-app
# --- kardinal-test-app-sha-abc1234-test (test) ---
# State:   Verified
# Message: health check passed (ArgoCD Synced)
#
# --- kardinal-test-app-sha-abc1234-uat (uat) ---
# State:   WaitingForMerge
# PR URL:  https://github.com/pnz1990/kardinal-demo/pull/42
```

Flags:
- `--env <env>`: filter by environment
- `--bundle <name>`: show logs for a specific Bundle (default: most recent active)

### kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured for
kardinal-promoter. Use this as the first troubleshooting step. (#578)

```bash
kardinal doctor
# also check a specific pipeline:
kardinal doctor --pipeline my-app
```

Output:
```
kardinal-promoter pre-flight check
==================================================
✅  Controller reachable          kardinal-promoter v0.6.0 in kardinal-system
✅  CRDs installed                pipelines, bundles, promotionsteps, policygates, prstatuses
✅  krocodile running             graph-controller 948ad6c in kro-system
✅  krocodile CRDs installed      group experimental.kro.run registered
⚠️  GitHub token                  secret github-token not found in kardinal-system
                                  Create: kubectl create secret generic github-token --namespace kardinal-system --from-literal=token=<PAT>

4 check(s) passed, 1 warning(s)
```

Exits with code 1 if any check fails (warnings do not trigger non-zero exit).

| Flag | Description |
|------|-------------|
| `--pipeline <name>` | Also check health of this Pipeline |

### kardinal version

Print the CLI and controller versions.

```bash
kardinal version
```

Output:
```
CLI:        v0.1.0
Controller: v0.1.0
Graph:      v0.9.1 (kro)
```

### kardinal completion

Generate shell completion scripts for bash, zsh, fish, or PowerShell. (#579)

```bash
# Bash (Linux/macOS)
kardinal completion bash > ~/.bash_completion.d/kardinal
# or source directly:
source <(kardinal completion bash)

# Zsh
kardinal completion zsh > "${fpath[1]}/_kardinal"
# or source directly:
source <(kardinal completion zsh)

# Fish
kardinal completion fish > ~/.config/fish/completions/kardinal.fish

# PowerShell
kardinal completion powershell | Out-String | Invoke-Expression
```

## Global Flags

| Flag | Description |
|---|---|
| `--kubeconfig <path>` | Path to kubeconfig file (default: `$KUBECONFIG` or `~/.kube/config`) |
| `--context <name>` | Kubernetes context to use |
| `-n, --namespace <ns>` | Namespace (default: current context namespace) |
| `-o, --output <format>` | Output format: `table` (default), `json`, `yaml` |
| `--verbose` | Enable verbose logging |

### kardinal audit summary

Show aggregate promotion metrics from the AuditEvent log.

```bash
kardinal audit summary [--pipeline <name>] [--since <duration>]
```

Output:
```
Pipeline: nginx-demo  (last 24h)

Promotions:   12 started, 10 succeeded, 1 failed, 1 superseded
Success rate: 83.3%
Avg duration: 8m 42s

Gates:        45 evaluations, 3 blocked (6.7% block rate)
Rollbacks:    1 triggered
```

Flags:
- `--pipeline <name>`: filter by pipeline name (default: all pipelines)
- `--since <duration>`: time window — e.g. `24h` (default), `7d`, `30d`


### kardinal validate

Validate a Pipeline or PolicyGate YAML file before applying to the cluster. Works offline — no cluster connection needed.

```bash
kardinal validate --file pipeline.yaml
kardinal validate --file policy-gate.yaml
```

Output:
```
✓ pipeline.yaml is valid
```
or:
```
✗ pipeline.yaml is invalid:
  - build: circular dependency in pipeline environments: prod → uat → prod (cycle!)
    Fix: remove one of the dependsOn references to break the cycle
```

Checks:
- Schema: required fields present, valid enum values
- Dependencies: no circular deps, all referenced environments exist
- CEL: PolicyGate expressions are syntactically valid

Flags:
- `-f, --file <path>`: Path to Pipeline or PolicyGate YAML file (required)


### kardinal status

Show controller health and a summary of managed resources.

```bash
kardinal status
```

Output:
```
Controller:  v0.8.1
Pipelines:   3
Bundles:     5 (1 active)
```

If there are failed pipelines:
```
Controller:  v0.8.1
Pipelines:   3 (1 failed: [nginx-demo])
Bundles:     5 (1 active)

Warning: 1 pipeline(s) in failed state — run 'kardinal get pipelines' for details
```

For detailed diagnostics, use `kardinal doctor`.

