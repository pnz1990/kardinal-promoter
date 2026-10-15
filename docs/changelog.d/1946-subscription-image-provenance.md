---
section: Fixed
---

- **An image Subscription's version no longer names its digest as the commit** (#1946, [Subscription](subscription.md#example-oci-image-subscription)). `provenance.commitSHA` was the image digest, which already sits in `images[0].digest`. It showed up in the PR evidence's Commit SHA column, in `bundle.provenance.commitSHA` for gates, and in `{{ bundle.commitSHA }}`. It is now the commit the build recorded in `org.opencontainers.image.revision` (a manifest annotation or an image config label), and empty when there is none.
