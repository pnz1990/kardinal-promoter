# Changelog

All notable changes are documented here. Maintained automatically by SM §4a.

## [Unreleased]

- fix(bundle): replace 1ms RequeueAfter hot loop with 500ms safe floor (#988)
- feat(steps): add argocd-set-image step for ArgoCD-native promotion (#966)
- feat(ci): GitHub Actions native bundle creation via composite action (#953)
- Scans clean; 6 new Future gaps added. (#952)
- feat(ui): Create Bundle dialog — POST /api/v1/ui/bundles + React dialog (#917) (#950)
- feat(cli): kardinal get subscriptions + SUB column in get pipelines (#948)
- 5 promoted, 5 reverted, 6 new gaps added. (#946)
- 9 gaps logged; 7 new Future items added. (#944)
- fix(workflow): Step 3 bash syntax error — restore scheduled runs (#943)
- feat(controller): NotificationHook CRD for outbound event webhook notifications (#942)
- feat(ui): insecure connection warning banner + port-forward documentation (#941)
- feat(controller): CORS lockdown for UI API via --cors-allowed-origins flag (#940)
- feat(workflow): upgrade_policy — auto-upgrade agent version in Step 3 (#938)
- feat(controller): add TLS support via --tls-cert-file / --tls-key-file flags (#937)
- chore: pin agent_version to v0.2.0 (#936)
- fix(workflow): App token validation — use /repos/:repo not /user (#935)
- security(m2): pin agent_version — close attack vector 3B (otherness clone unversioned) (#934)
- fix(ci): replace Python heredoc with inline one-liners to fix YAML parse error (#933)
- fix(ci): manifest schema validation — prevent Demo E2E drift (design doc 39) (#931)
- test(api): add CRD printer column drift detection test (#930)
- docs(security): add UI API Access Control section (#929)
- fix(ci): remove invalid health sub-fields from demo and example YAML (#927)
- feat(ui): add Bearer token auth to UI API via --ui-auth-token flag (#924)
- vision(auto): autonomous scan — promote shipped items, fix stale flags, infer Kargo-competitive gaps (#923)
- 3 PRs merged: CI fix, Bundle GC, panic docs (#922)
- feat(controller): document and guard RecoverPanic invariant in controller manager (#921)
- feat(reconciler): enforce Bundle historyLimit to prevent etcd accumulation (#919)
- fix(crd): regenerate CRD schemas and deepcopy for WaitForMergeExpiry field (#908)
- feat(reconciler): add WaitingForMerge timeout to prevent stuck promotions (#906)
- feat(crd): add kubectl printer columns to Bundle and PromotionStep CRDs (#903)

