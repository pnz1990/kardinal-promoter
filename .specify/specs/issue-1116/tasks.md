# Tasks: issue-1116 — Add ResourceRef to HealthConfig

## Pre-implementation
- [CMD] `cd $MY_WORKTREE && go build ./...` — expected: 0 exit

## Implementation
- [AI] Add ResourceRef struct and Resource field to HealthConfig in api/v1alpha1/pipeline_types.go
- [CMD] `grep -n "ResourceRef" $MY_WORKTREE/api/v1alpha1/pipeline_types.go | head -5` — expected: found
- [AI] Update DeepCopy in zz_generated.deepcopy.go for HealthConfig + add ResourceRef deepcopy
- [AI] Update healthOptsForEnv in pkg/translator/translator.go to use Resource field when set
- [CMD] `cd $MY_WORKTREE && bin/controller-gen crd:allowDangerousTypes=true paths="./api/..." output:crd:artifacts:config=config/crd/bases` — expected: 0 exit
- [CMD] `grep "resource:" $MY_WORKTREE/config/crd/bases/kardinal.io_pipelines.yaml | head -3` — expected: found

## Post-implementation
- [CMD] `cd $MY_WORKTREE && go build ./...` — expected: 0 exit
- [CMD] `cd $MY_WORKTREE && go test ./pkg/translator/... -run TestInjectHealthWatchNodes_ResourceRef -v` — expected: PASS
- [CMD] `cd $MY_WORKTREE && go test ./api/... ./pkg/... -race -count=1 -timeout 120s` — expected: all pass
