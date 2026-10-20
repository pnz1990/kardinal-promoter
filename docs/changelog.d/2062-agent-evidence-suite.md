---
section: Development
---

- **The `agent-evidence` live suite, the v0.13 acceptance test** (#2062): an agent's version passes an attestations gate, an agent verifier, a ServiceNow change request and a person's approval naming its ticket; its signed receipt names all of it; an auditor's evidence pack holds it; `kardinal verify-receipt` verifies the pack's receipt in a network namespace with no network; and the pack's control mapping cites every record. It runs in `hack/e2e/matrix.txt` and the release gate.
