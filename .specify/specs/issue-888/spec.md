# Spec: docs(roadmap): kardinal-agent shipped — docs drift fix

## Design reference
- N/A — documentation update only, no user-visible behavior change

## Zone 1 — Obligations

**O1** — `docs/roadmap.md` MUST remove the "## Near-Term (v0.9.0 — planned) / kardinal-agent standalone binary" section. That section described planned work that has shipped. Violation: any mention of kardinal-agent as planned/near-term when it's already in `cmd/kardinal-agent/`.

**O2** — `docs/roadmap.md` line referencing distributed mode MUST say kardinal-agent is available (not near-term). Violation: the old text said "is near-term (#508)".

**O3** — `docs/changelog.md` Unreleased→Added section MUST include a kardinal-agent entry describing the binary, its behavior, and PR #886. Violation: changelog doesn't mention kardinal-agent shipped feature.

**O4** — `docs/design/14-v060-roadmap.md` MUST show all items 14.1–14.5 as ✅ Present (not 🔲 Future). Violation: any of 14.1–14.5 remaining as 🔲.

**O5** — `docs/design/07-distributed-architecture.md` MUST have a ## Present section recording the kardinal-agent binary and shard routing as shipped. Violation: Present section absent.

## Zone 2 — Implementer's judgment

- Exact wording of changelog entry (follows existing format: bold name, dash, description, PR reference)
- Whether to add PR references for 14.1-14.4 (they predate the current tracking; "shipped in earlier release" is sufficient)

## Zone 3 — Scoped out

- Changes to vision.md or roadmap.md in docs/aide/ (protected files)
- Updating the public website directly (docs.yml deploys on push to main)
- Adding test coverage for docs validation
