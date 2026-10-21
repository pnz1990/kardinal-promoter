---
section: Upgrade
---

- **Status writes by anyone but the controller are refused** (#2446) — scripts or tools that patch a kardinal object's status (`kubectl patch --subresource status`) as a person, a cluster-admin or a ServiceAccount of their own are refused by `<fullname>-status-writers`. `<fullname>-admin` no longer grants `resources: ["*"]`, so it covers no new kind until the chart names it. To change a status by hand on purpose, delete the policy's binding; `helm upgrade` restores it ([Status is the controller's](guides/security.md#status-is-the-controllers)).
