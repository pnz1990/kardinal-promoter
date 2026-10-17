---
section: Added
---

- **`kardinal init --insecure-http`** (#1949) — `init` accepts an `http://` `--repo` (or wizard answer) for a git server without TLS, such as a local or in-cluster Forgejo or Gitea, but only when you pass `--insecure-http`. It prints a warning on stderr because the controller then sends the git token in clear text. Without the flag, `init` refuses an `http://` URL and the error names the flag. The host is never used to decide (a Pipeline's `localhost` is the controller pod's own loopback). The other URL rules still apply: no password, query or fragment. This matches the opt-in that `scm.providersAllowInsecureHTTP` requires for an ScmProvider's `apiURL`. See [Your first pipeline](first-pipeline.md).
