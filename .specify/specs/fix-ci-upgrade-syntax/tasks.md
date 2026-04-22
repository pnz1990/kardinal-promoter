# Tasks: fix-ci-upgrade-syntax

## Pre-implementation
- [CMD] `cd $MY_WORKTREE && go build ./...` — expected: 0 exit

## Implementation
- [AI] Update otherness-config.yaml agent_version from v0.2.0 to v0.3.0
- [CMD] `grep agent_version $MY_WORKTREE/otherness-config.yaml` — expected: v0.3.0
- [AI] Replace problematic python3 -c inline in workflow with heredoc temp file approach
- [CMD] `bash -n /tmp/test_install_fixed.sh` — expected: 0 exit

## Post-implementation
- [CMD] `cd $MY_WORKTREE && go build ./...` — expected: 0 exit
- [CMD] `cd $MY_WORKTREE && go vet ./...` — expected: 0 exit
