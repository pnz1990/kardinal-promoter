---
section: Fixed
---

- **An image Subscription's version no longer names its digest as the commit** (#1946, [Subscription](subscription.md#example-oci-image-subscription)). `provenance.commitSHA` was the image digest, which already sits in `images[0].digest`. It showed up in the PR evidence's Commit SHA column, in `bundle.provenance.commitSHA` for gates, and in `{{ bundle.commitSHA }}`. It is now the commit the build recorded in `org.opencontainers.image.revision` (a manifest annotation or an image config label), when that is a full commit SHA (40 or 64 hex characters, lowercased), and empty otherwise: a tag such as `sha-1a2b3c4`, a short SHA or a build number is not taken for a commit. Versions created before the upgrade keep the digest in `provenance.commitSHA`.
