---
section: Fixed
---

- **The UI answers 400 when the API server refuses a field** (#1867) — an approval comment or gate override reason over its limit, or any other field the CRD refuses, answered 500 ("failed to approval", "failed to update gate"). It now answers 400 with the API server's message, which names the field.
