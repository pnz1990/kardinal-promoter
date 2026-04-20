# Spec: TLS for UI and Webhook HTTP Servers

**Issue**: #911
**Design ref**: `docs/design/06-kardinal-ui.md §Future → TLS for UI and webhook HTTP servers`

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future`
- **Implements**: TLS for UI and webhook HTTP servers (🔲 → ✅)

## Zone 1 — Obligations (falsifiable)

1. `--tls-cert-file` flag added to `cmd/kardinal-controller/main.go`. Env var: `KARDINAL_TLS_CERT_FILE`. Default: empty string.
2. `--tls-key-file` flag added to `cmd/kardinal-controller/main.go`. Env var: `KARDINAL_TLS_KEY_FILE`. Default: empty string.
3. When both `--tls-cert-file` AND `--tls-key-file` are non-empty: the UI server uses `http.ListenAndServeTLS(addr, certFile, keyFile, handler)` instead of `http.ListenAndServe`.
4. When both flags are set: the webhook server also uses `ListenAndServeTLS`.
5. When neither flag is set: both servers use plain `http.ListenAndServe` (backwards compatible, no regression).
6. When exactly one of the two flags is set: log `Warn` "TLS: both --tls-cert-file and --tls-key-file must be set; falling back to plain HTTP" and use plain HTTP.
7. A unit test `TestTLSConfigFromFlags` verifies the three cases: both set → TLS, neither set → plain, one set → plain with warning.
8. `chart/kardinal-promoter/values.yaml` gains `controller.tlsCertFile` and `controller.tlsKeyFile` string fields (default: `""`).
9. `chart/kardinal-promoter/templates/deployment.yaml` passes the Helm values as `KARDINAL_TLS_CERT_FILE` and `KARDINAL_TLS_KEY_FILE` env vars when non-empty.
10. `docs/guides/security.md` gains a `## TLS Configuration` section explaining the flags, cert-manager volume mount pattern, and port-forward as the non-TLS access method.

## Zone 2 — Implementer's judgment

- Cert source is file-based only (works with any cert-manager / manual mount approach).
- No automatic cert rotation or hot-reload — file is read at server start time.
- Test uses self-signed cert generated in-process (no test fixtures committed).
- Chart values are strings so platform teams can set cert paths via Helm.

## Zone 3 — Scoped out

- mTLS
- cert-manager Certificate CRD auto-provisioning
- Automatic cert rotation on file change
- Any other HTTP server (metrics :8080, health :8081 — managed by controller-runtime, not this code)
