# Tasks — issue-982

## Populate Bundle status.conditions on phase transitions

- [AI]  Write spec.md (Zone 1 obligations) — DONE
- [CMD] Read handleNew/handleAvailable/markSuperseded/handleSyncEvidence — DONE
- [AI]  Add setBundleCondition() helper function — DONE
- [AI]  Call setBundleCondition in handleNew (Available) — DONE
- [AI]  Call setBundleCondition in handleAvailable Promoting path — DONE
- [AI]  Call setBundleCondition in handleAvailable Failed path (2 conditions) — DONE
- [AI]  Call setBundleCondition in markSuperseded — DONE
- [AI]  Call setBundleCondition in handleSyncEvidence (Verified case) — DONE
- [AI]  Add 5 test cases: Available, Promoting, Failed, Superseded, NoDuplicates — DONE
- [CMD] go build ./... — DONE (clean)
- [CMD] go test ./pkg/reconciler/bundle/... -race — DONE (ok)
- [CMD] go test ./... -race — DONE (all green)
- [CMD] go vet ./... — DONE (clean)
- [AI]  Update design doc 15 (🔲 → ✅) — DONE
- [AI]  Update triage notes — DONE
- [CMD] Commit and push — TODO
- [CMD] Open PR — TODO
