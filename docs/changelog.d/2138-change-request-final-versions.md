---
section: Fixed
---

- **A superseded or rejected version's change requests stop polling** (#2138 follow-up). A ChangeRequest instance whose Bundle is Superseded, Rejected, gone or re-created no longer reads its ticket, and its `Ready` condition says `VersionFinal`. The Graph holds such a version's steps, so the instance's `spec.suspend` never flipped, and it kept calling ServiceNow or Jira until its Graph was retired. MetricCheck instances got the same fix in #2138.
