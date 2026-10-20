---
section: Fixed
---

- **yaml-update keeps the rest of the file byte for byte after a key with non-ASCII characters** (#2080). When a value after such a key was a quoted scalar spanning lines, the whole file could be re-encoded, so the promotion PR showed unrelated line changes. The values were never changed ([The yaml update strategy](pipeline-reference.md#the-yaml-update-strategy)).
