# PR Evidence

For environments with `approval: pr-review`, kardinal-promoter opens a pull request in the GitOps repository. The PR body is not a raw YAML diff. It is a structured promotion record containing artifact provenance, upstream verification results, and policy gate compliance.

## PR Structure

### Title

```
[kardinal] Promote <pipeline> v<version> to <environment>
```

For rollbacks:
```
[kardinal] Rollback <pipeline> in <environment>: v<current> to v<previous>
```

### Labels

| Label | Always present | Description |
|---|---|---|
| `kardinal` | Yes | Identifies the PR as managed by kardinal-promoter |
| `kardinal/promotion` | On promotions | Normal forward promotion |
| `kardinal/rollback` | On rollbacks | Rollback to a previous version |
| `kardinal/emergency` | On `--emergency` rollbacks | Signals priority review |
| `kardinal/<pipeline>` | Yes | Pipeline name |
| `kardinal/<environment>` | Yes | Target environment name |

### Body

```markdown
## Promotion: my-app v1.29.0 to prod

### Policy Gates
| Gate | Scope | Status | Detail |
|---|---|---|---|
| no-weekend-deploys | org | PASS | Tuesday 14:00 UTC |
| staging-soak | org | PASS | Soak: 45m (min: 30m) |

### Artifact
| Field | Value |
|---|---|
| Image | ghcr.io/myorg/my-app:1.29.0 |
| Digest | sha256:a1b2c3d4 |
| Source Commit | abc123d |
| CI Run | Build #12345 |
| Author | engineer-name |

### Upstream Verification
| Environment | Verified | Soak |
|---|---|---|
| dev | 2h ago | n/a |
| staging | 45m ago | 45m |

### Changes
ghcr.io/myorg/my-app: 1.28.0 to 1.29.0
```

## Sections Explained

### Policy Gates

Lists every PolicyGate that was evaluated for this environment. Shows the gate name, scope (org or team), result (PASS, FAIL, PENDING), and a detail string explaining the evaluation.

For `approval: pr-review` environments, the `require-approval` gate shows as PENDING until the PR is merged. The PR merge itself satisfies the `pr-review` gate.

### Artifact

Shows exactly what is being deployed:
- **Image**: the full image reference including tag
- **Digest**: the immutable image digest (sha256)
- **Source Commit**: the Git commit that triggered the CI build (links to the commit on GitHub)
- **CI Run**: the CI pipeline run that built this image (links to the CI run)
- **Author**: who or what triggered the build (human, dependabot, etc.)

This provenance comes from the Bundle's `spec.provenance` field, which is set when the Bundle is created by CI.

### Upstream Verification

Shows each upstream environment's verification status:
- **Environment**: the environment name
- **Verified**: how long ago the Bundle was verified there
- **Soak**: how long the Bundle has been verified (relevant for soak-time PolicyGates)

This data comes from the Bundle's `status.environments` field.

### Changes

A summary of what artifacts changed compared to what is currently deployed in the target environment. Shows the image reference and the version change (old to new).

## PR Comment Updates

The PR body is created at PR open time and updated when gate states change. The controller updates the PR comment via the SCM provider's `UpdatePRComment` method when:
- A PolicyGate transitions from FAIL to PASS (or vice versa)
- Upstream verification completes for a new environment
- Evidence metrics are updated

Between updates, the PR body is a snapshot and may be stale relative to live CRD state. The UI and `kardinal explain` always show live state.

## Merge Detection

kardinal-promoter detects PR merges via webhook. The controller exposes a `/webhooks` endpoint that receives GitHub `pull_request` events. When a PR with the `kardinal` label is merged, the controller advances the PromotionStep to the HealthChecking state.

On controller restart, the controller lists all open PRs with the `kardinal` label and reconciles any that were merged during downtime. There is no periodic polling.

## Auto-Merge Environments

For environments with `approval: auto`, no PR is created. The controller pushes directly to the target branch (or directory).

To create an audit-trail PR even for auto-promoted environments (useful for compliance), add `pr: true` to the environment config:

```yaml
environments:
  - name: staging
    approval: auto
    # pr: true    # uncomment to create auto-merged audit PRs
```

## CODEOWNERS Integration

The controller respects GitHub CODEOWNERS. If the target directory has a CODEOWNERS file that requires specific reviewers, the PR will require those reviews before merge. The controller does not bypass CODEOWNERS.

## Branch Naming

PR branches follow the pattern: `kardinal/<pipeline>/<environment>/<bundle-version>`

Example: `kardinal/my-app/prod/v1.29.0`

## Commit Messages

```
[kardinal] Promote my-app to prod: v1.28.0 to v1.29.0

Bundle: v1.29.0
Images: ghcr.io/myorg/my-app:1.29.0
Route: my-app
Environment: prod
Previous: v1.28.0
```
