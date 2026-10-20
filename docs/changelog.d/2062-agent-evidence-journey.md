---
section: Fixed
---

- **The evidence pack's control mapping cites every file and change request record it holds** (#2062): `controls.json` now names `policygate-templates.ndjson`, `changerequest-templates.ndjson` and `auditevents.ocsf.ndjson` on the rows that cite their kinds, and the change-approval rows cite `ChangeRequestOpened` and `ChangeRequestLinked` as well as `ChangeRequestApproved`. Before, an auditor reading [the controls](compliance.md#the-controls) found records in the pack that no row cited.
