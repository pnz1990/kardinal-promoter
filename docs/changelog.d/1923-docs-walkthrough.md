---
section: Development
---

- **The docs, executed** (#1923) — a live suite, `docs-walkthrough`, runs the command blocks of the quickstart, approvals, rollback, policy gates, pause (with a fleet), connect an agent and first pipeline pages in order, as a new user would. Where a page shows the output, the walkthrough holds the command's output to it, and it checks each step's outcome (phase, PR, exit code); every command block of those pages is either run or listed as skipped with the reason. It runs on Kubernetes 1.35 and 1.37 and nightly. The documented-command check now refuses hidden, older flag names: `kardinal override` examples use `--env`, not `--stage`.
