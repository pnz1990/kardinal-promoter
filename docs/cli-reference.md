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

Generate a Pipeline CRD from a simple configuration file.

```bash
kardinal init -f kardinal.yaml
```

Input file format:
```yaml
app: my-app
image: ghcr.io/myorg/my-app
git:
  url: https://github.com/myorg/gitops-repo
  secret: github-token
environments: [dev, staging, prod]
prodApproval: pr-review
```

The command creates a Pipeline CRD and applies it to the cluster. It also prints instructions for creating your first Bundle from CI.

### kardinal get pipelines

List all Pipelines with the current Bundle and per-environment status.

```bash
kardinal get pipelines
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
- `-o, --output <format>`: output format (`table`, `json`, `yaml`)

### kardinal get steps

Show all PromotionSteps and PolicyGates for a Pipeline's current promotion.

```bash
kardinal get steps <pipeline>
```

Output:
```
STEP                              TYPE            STATE             ENV
my-app-v1-29-0-dev                PromotionStep   Verified          dev
my-app-v1-29-0-staging            PromotionStep   Verified          staging
my-app-v1-29-0-no-weekend         PolicyGate      Pass              prod
my-app-v1-29-0-staging-soak       PolicyGate      Pass              prod
my-app-v1-29-0-prod               PromotionStep   WaitingForMerge   prod
```

Flags:
- `--bundle <version>`: show steps for a specific Bundle (default: latest active)

### kardinal get bundles

List Bundles for a Pipeline with provenance and per-environment status.

```bash
kardinal get bundles <pipeline>
```

Output:
```
BUNDLE    IMAGES                            DEV        STAGING    PROD         PHASE        AGE
v1.29.0   ghcr.io/myorg/my-app:1.29.0      Verified   Verified   Promoting    Promoting    2h
v1.28.0   ghcr.io/myorg/my-app:1.28.0      Verified   Verified   Verified     Verified     1d
v1.27.0   ghcr.io/myorg/my-app:1.27.0      Verified   Verified   Verified     Superseded   3d
```

Flags:
- `--phase <phase>`: filter by phase (`Available`, `Promoting`, `Verified`, `Failed`, `Superseded`)
- `-o, --output <format>`: output format

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

Manually trigger promotion of the latest available Bundle to a specific environment. Normally promotion is automatic, but this command is useful for debugging or for environments that require manual triggering.

```bash
kardinal promote <pipeline> --env <environment>
```

Example:
```bash
kardinal promote my-app --env prod
```

Output:
```
Promoting my-app to prod: v1.28.0 -> v1.29.0
  Metric gate passed (staging success-rate: 99.7%)
  PR #144 opened: https://github.com/myorg/gitops-repo/pull/144
  Merge PR #144 to complete promotion (gate: pr-review)
```

### kardinal explain

Show the PolicyGate evaluation trace for a specific environment. This is the primary debugging tool when a promotion is blocked.

```bash
kardinal explain <pipeline> --env <environment>
```

Output:
```
PROMOTION: my-app / prod
  Bundle: v1.29.0

POLICY GATES:
  no-weekend-deploys  [org]   PASS   schedule.isWeekend = false
  staging-soak        [org]   FAIL   bundle.upstreamSoakMinutes = 12 (threshold: >= 30)
                                     ETA: ~18 minutes (based on staging verifiedAt)

RESULT: BLOCKED by staging-soak
```

Flags:
- `--watch`: continuous output, re-evaluates and reprints when gate states change
- `--bundle <version>`: explain a specific Bundle (default: latest active)

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

Pause all promotion activity for a Pipeline by injecting a PolicyGate with `expression: "false"`.

```bash
kardinal pause <pipeline>
```

Output:
```
Pipeline my-app paused.
  PolicyGate "freeze-1712567890" created with expression: "false"
  All in-flight promotions will be blocked at the next gate evaluation.
  Run "kardinal resume my-app" to remove the freeze.
```

### kardinal resume

Resume a paused Pipeline by deleting the freeze PolicyGate.

```bash
kardinal resume <pipeline>
```

### kardinal history

Show the promotion history for a Pipeline, including which Bundles were promoted to which environments and when.

```bash
kardinal history <pipeline>
```

Output:
```
BUNDLE    ACTION     ENV     PR     APPROVER   DURATION   TIMESTAMP
v1.29.0   promote    dev     --     (auto)     3m         2026-04-09 10:05
v1.29.0   promote    staging --     (auto)     8m         2026-04-09 10:10
v1.29.0   promote    prod    #144   alice      15m        2026-04-09 10:20
v1.28.0   rollback   prod    #140   bob        5m         2026-04-08 16:30
v1.28.0   promote    prod    #138   alice      12m        2026-04-07 14:00
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
NAME                  SCOPE   APPLIES-TO     TYPE             EXPRESSION                          NAMESPACE
no-weekend-deploys    org     prod           gate             !schedule.isWeekend                 platform-policies
require-staging-soak  org     prod           gate             bundle.upstreamSoakMinutes >= 30    platform-policies
allow-hotfix-skip     org     staging        skip-permission  bundle.labels.hotfix == true         platform-policies
extra-review          team    prod           gate             bundle.provenance.author != "bot"   my-team
```

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

Simulate a PolicyGate evaluation against a hypothetical context (future time, different labels, etc.).

```bash
kardinal policy simulate \
  --pipeline my-app \
  --env prod \
  --time "Saturday 3pm" \
  [--label hotfix=true]
```

Output:
```
SIMULATED: my-app -> prod at Saturday 15:00 UTC

POLICY GATES:
  no-weekend-deploys  [org]   FAIL   schedule.isWeekend = true
  staging-soak        [org]   PASS   bundle.upstreamSoakMinutes = 45 (>= 30)

RESULT: BLOCKED
  Blocked by: no-weekend-deploys
  Message: "Production deployments are blocked on weekends"
  Next window: Monday 00:00 UTC
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

## Global Flags

| Flag | Description |
|---|---|
| `--kubeconfig <path>` | Path to kubeconfig file (default: `$KUBECONFIG` or `~/.kube/config`) |
| `--context <name>` | Kubernetes context to use |
| `-n, --namespace <ns>` | Namespace (default: current context namespace) |
| `-o, --output <format>` | Output format: `table` (default), `json`, `yaml` |
| `--verbose` | Enable verbose logging |
