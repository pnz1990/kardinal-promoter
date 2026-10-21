---
section: Fixed
---

- **An AgentRun created again after its Verdict names its agent again** (#2345). When kro creates an in-cluster verifier's AgentRun again after it was deleted (its Verdict already recorded, so it does not run again), the run names the agent and sponsor its StepTemplate's AgentIdentity names, and the ServiceAccount that wrote the Verdict, when kardinal's records of the Verdict (its VerdictRecorded AuditEvent, the version's receipts) name the same agent and sponsor. Before, it named none: the verify gate stopped counting the Verdict (the environment waited again) and the evidence pack's AgentRun named no agent ([Trace a Verdict to its agent](compliance.md#trace-a-verdict-to-its-agent)). If the template's agent or the sponsor changed since the run, no record is left, or the agent may no longer verify there, the run names none and says why in `status.message`, and the Verdict does not count: it is never counted as another agent's.
