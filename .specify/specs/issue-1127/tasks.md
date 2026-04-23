# Tasks: issue-1127 — Document multi-tenant workaround

## Pre-implementation
- [CMD] `grep -n "Multi-tenant" /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1127/docs/guides/security.md` — expected: line ~172 with "### Multi-tenant isolation" heading

## Implementation
- [AI] Expand the "Multi-tenant isolation" section in docs/guides/security.md with workaround instructions
- [AI] Mark 🔲 item as ✅ in docs/design/15-production-readiness.md

## Post-implementation
- [CMD] `grep -A 20 "Multi-tenant isolation" /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1127/docs/guides/security.md` — expected: shows workaround with Helm commands
