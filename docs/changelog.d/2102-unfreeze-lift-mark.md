---
section: Fixed
---

- **A failed `kardinal unfreeze` always takes back its lift mark** (#2102). `kardinal unfreeze` and the UI write who lifts a freeze (`spec.freeze.lifted`) just before they delete it, and take the mark back when the delete fails. When the mark was written on an earlier retry and a later retry failed, or found the freeze frozen again, the mark stayed: the freeze stayed in force, but named someone as lifting it. The freeze was never lifted by mistake ([Who froze, and who lifted it](agentic/freezes.md#who-froze-and-who-lifted-it)).
