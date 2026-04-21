# Tasks — issue-987

## Fix `RequeueAfter: time.Millisecond` hot loop in bundle reconciler

- [CMD] Verify `time.Millisecond` on line 358 of bundle reconciler — DONE
- [AI]  Write spec.md (Zone 1 obligations) — DONE
- [CMD] Replace `time.Millisecond` with `500 * time.Millisecond` — DONE
- [AI]  Update comment to explain 500ms minimum safe floor — DONE
- [AI]  Move design doc 15 item from 🔲 Future to ✅ Present — DONE
- [AI]  Update triage notes in design doc — DONE
- [CMD] go build ./... — DONE (clean)
- [CMD] go test ./pkg/reconciler/bundle/... -race — DONE (ok)
- [CMD] go vet ./... — DONE (clean)
- [CMD] Commit and push — TODO
- [CMD] Open PR — TODO
