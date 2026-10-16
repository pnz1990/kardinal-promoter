---
section: Upgrade
---

- **An image Subscription's `provenance.commitSHA` is the source commit, or empty** (#1946) — it was the image digest. A gate or PR template that reads `bundle.provenance.commitSHA` (or `{{ bundle.commitSHA }}`) on versions an image Subscription creates now sees the commit the build recorded in `org.opencontainers.image.revision`, or an empty string when the image records none. Read the digest from `images[0].digest` instead, and label your images with the revision ([Subscription](subscription.md#example-oci-image-subscription)). Versions created before the upgrade keep the digest.
