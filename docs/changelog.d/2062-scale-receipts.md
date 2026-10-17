---
section: Development
---

- **Receipts in the scale suite** (#2062) — `TestScale_Receipts` has the controller sign and store 1,000 receipts (10,000 in the full profile; 1% near the size cap) arriving at once, within a time and memory budget, signs a new one within two minutes of a restart of every controller Pod, and checks that retention's deletes do not hold up signing. Budgets are the profile's `Receipt*` fields.
