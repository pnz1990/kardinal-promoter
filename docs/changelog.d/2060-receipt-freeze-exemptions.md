---
section: Added
---

- **A promotion receipt records the freeze exemptions the promotion went on through** (#2060) — `kardinalPromotion.freezeExemptions` (the freeze, who made the exemption and why, until when) and the `KARDINAL_FREEZE_EXEMPTED` level, so `kardinal verify-receipt --require '!FREEZE_EXEMPTED'` refuses a promotion that broke glass ([Receipts](receipts.md))
