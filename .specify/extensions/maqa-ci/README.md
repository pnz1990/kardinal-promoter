# MAQA CI/CD Integration

> CI/CD pipeline status gate for the [MAQA](https://github.com/GenieRobot/spec-kit-maqa-ext) spec-kit extension.

Prevents a feature from moving to In Review until its CI pipeline is green. The coordinator checks CI status automatically after each feature completes.

## Requirements

- [maqa](https://github.com/GenieRobot/spec-kit-maqa-ext) extension installed
- One of:
  - **GitHub Actions**: `GH_TOKEN` with repo scope (or `gh auth login`)
  - **CircleCI**: `CIRCLE_TOKEN`
  - **GitLab CI**: `GITLAB_TOKEN`
  - **Bitbucket**: `BITBUCKET_USER` + `BITBUCKET_TOKEN`

## Installation

```bash
specify ext add maqa
specify ext add maqa-ci
```

## Setup

```bash
/speckit.maqa-ci.setup
```

Auto-detects your CI provider from repo structure. Writes `maqa-ci/ci-config.yml`. The coordinator gates In Review on green CI from that point on.

## Coordinator behaviour

When `maqa-ci/ci-config.yml` is present:

1. Feature agent reports `done`
2. Coordinator checks CI status for the feature branch
3. **Green** → proceed to QA, then In Review
4. **Red** → add BLOCKED comment, return to feature agent for fix
5. **Pending** → wait up to `wait_timeout_seconds`, then treat as blocked
6. **Unknown** → warn and proceed (configurable via `block_on_red: false`)

## License

MIT
