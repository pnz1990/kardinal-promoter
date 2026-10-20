---
section: Fixed
---

- **The public-good trusted root is fetched in seconds, not 15 minutes** (#2218). The controller's TUF fetcher for image verification's public-good Sigstore root retried every error, a 404 included, for up to 15 minutes, and a TUF root update normally ends on the 404 for the next root version. So the first keyless verification after a start, and every daily refresh, waited about 15 minutes for the root. A 404 (or any other 4xx) now ends a download at once; only a 5xx, a 429 or a transport error is tried again, up to 3 times; and a whole fetch is bounded at 2 minutes. `kardinal verify-receipt` fetches its default root the same way ([Image Signature Verification](image-verification.md)).
