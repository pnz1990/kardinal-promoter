---
section: Development
---

- **The enterprise's day in the life** (#2069). This scenario puts v0.12's features together on one in-process World: three tenants, a fleet, people, a bot, and agents of every autonomy (registered individually, by group, and one suspended).
  - The day's episodes: one-party approvals; a verify gate that passes, fails and times out; an agents-only freeze; stop-the-line with an exemption capped at 24 hours; a pause; a hold; PR reviews whose PRMerged events name the merger; a canary rollback; an agent index outage.
  - It runs `test/scenario/enterprise/day` in a fixed order and with seeded random interleavings. After every step and round it checks that no promotion bypasses a freeze, pause, hold, gate or the one-party rule, and that no forged stamp is recorded.
