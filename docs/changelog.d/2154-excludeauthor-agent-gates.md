---
section: Documentation
---

- **The compliance mapping names the gates where `excludeAuthor` is on by default** (#2154) — the separation-of-duties rows said `approval.excludeAuthor` is off by default; the approval gates kardinal adds for agents' versions in `agents: approve` environments set it, and a verify gate's `verify.excludeAuthor` is on by default ([Map to compliance controls](compliance.md#the-controls)).
