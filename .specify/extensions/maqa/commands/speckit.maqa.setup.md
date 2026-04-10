---
description: "One-time MAQA setup: copies maqa-config.yml to the project root. Run once per project."
---

You are setting up MAQA for this project. This is a one-time operation.

## What this does

Copies `maqa-config.yml` to the project root so you can customize test commands,
QA checks, execution mode, and agent file path.

## Setup

```bash
# Copy maqa-config.yml to project root if not already present
if [ ! -f "maqa-config.yml" ]; then
  cp .specify/extensions/maqa/config-template.yml maqa-config.yml
  echo "Created maqa-config.yml — edit before running the coordinator."
fi
```

Open `maqa-config.yml` and configure at minimum:

- `test_command` — your test runner (e.g. `go test ./...`, `npm test`, `pytest`)
- `agents_path` — path to your agent instruction files (e.g. `~/.myproject/agents`)
- `mode` — `team` (multiple sessions) or `standalone` (single session)

## Agent files

Agent instruction files live outside the repo so all worktrees always read
the latest version. Create them at your `agents_path`:

- `coordinator.md` — continuous coordinator loop
- `engineer.md`    — feature engineer loop (reads CLAIM file for identity)
- `qa-watcher.md`  — continuous PR review loop
- `scrum-master.md`— one-shot SDLC health review
- `product-manager.md` — one-shot product review
- `standalone.md`  — single session, all roles sequentially

See the reference implementations in your agents_path directory (configured in maqa-config.yml).

## Done

Run `/speckit.maqa.coordinator` (team mode) or `/speckit.maqa.standalone` (standalone mode).
