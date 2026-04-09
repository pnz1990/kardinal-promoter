# MAQA CI/CD Changelog

## 0.1.0 — 2026-03-26

Initial release.

- Setup command: auto-detects CI provider from repo structure (.github/workflows/, .circleci/, .gitlab-ci.yml, bitbucket-pipelines.yml)
- Check command: queries pipeline status for a branch; returns green/red/pending/unknown
- Coordinator integration: auto-detected when ci-config.yml present; gates In Review move on green CI
- Supports: GitHub Actions, CircleCI, GitLab CI, Bitbucket Pipelines
