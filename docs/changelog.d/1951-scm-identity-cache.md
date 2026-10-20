---
section: Fixed
---

- **The cached SCM identity of a replaced provider is dropped** (#1951). When a token rotates, or the ScmProvider registry drops a client, the identity the receipt comment search cached for it goes too, and the cache is bounded besides (1024, least recently used out). Before, every rotation added an entry for good.
