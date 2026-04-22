# Tasks: issue-985 — PromotionTemplate CRD

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-985 && go build ./...` — expected: 0 exit, no errors
- [CMD] `cd ../kardinal-promoter.issue-985 && go test ./... -count=1 -timeout 60s 2>&1 | tail -5` — expected: all pass

## Implementation
- [AI] Add `PromotionTemplate` type to `api/v1alpha1/` with spec.steps and spec.description
- [AI] Add `PromotionTemplateRef` struct and `PromotionTemplate` field to `EnvironmentSpec` in `pipeline_types.go`
- [CMD] `cd ../kardinal-promoter.issue-985 && go build ./api/...` — expected: 0 exit
- [AI] Update `zz_generated.deepcopy.go` with DeepCopy methods for new types
- [AI] Add `InlinePromotionTemplates(ctx, pipeline, reader)` function to `pkg/translator/` that resolves templates before calling builder
- [CMD] `cd ../kardinal-promoter.issue-985 && go build ./pkg/translator/...` — expected: 0 exit
- [AI] Call `InlinePromotionTemplates` in `Translator.Translate` before calling `builder.Build`
- [AI] Write table-driven tests in `pkg/translator/template_test.go` and add builder test cases in `pkg/graph/builder_test.go`
- [CMD] `cd ../kardinal-promoter.issue-985 && go test ./pkg/translator/... ./pkg/graph/... -count=1 -timeout 60s` — expected: all pass
- [AI] Add CRD YAML manifest at `config/crd/bases/kardinal.io_promotiontemplates.yaml`
- [AI] Add Helm RBAC rules for PromotionTemplate (ClusterRole in chart)
- [AI] Add `## PromotionTemplate` section to `docs/concepts.md`
- [AI] Update `docs/design/15-production-readiness.md` — flip 🔲 to ✅

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-985 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-985 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -10` — expected: all pass
- [CMD] `cd ../kardinal-promoter.issue-985 && go vet ./...` — expected: 0 exit, no issues
