# Tasks: issue-995 — Document zero-downtime SCM credential rotation

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-995 && go build ./... 2>&1 | tail -3` — expected: 0 exit (no build errors)

## Implementation

- [AI] Add a note to `docs/quickstart.md` in the credential section explaining that `github.secretRef.name` enables automatic token reload (no restart needed on rotation)
- [CMD] `grep -n "secretRef\|rotation\|reload" docs/quickstart.md | head -10` — expected: contains secretRef.name
- [AI] Add a **Credential rotation (zero-downtime)** section to `docs/scm-providers.md` explaining the flags, 30s interval, and rotation procedure
- [CMD] `grep -n "Credential rotation\|zero-downtime\|scm-token-secret" docs/scm-providers.md | head -10` — expected: contains "Credential rotation"

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-995 && go build ./... 2>&1 | tail -3` — expected: 0 exit
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-995 && go test ./... -count=1 -timeout 60s 2>&1 | tail -5` — expected: PASS or ok
