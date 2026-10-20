---
section: Fixed
---

- **A receipt part-way through storing reaches Stored after `receipts.sinks.oci.repository` changes** (#2331). Its referrers were pushed again to the new repository and appended beside the old ones, so a receipt of 51 images or more ran past `status.stored`'s bound and stayed Signed. Each new referrer now replaces its image's entry. ([Where receipts are stored](receipts.md#where-receipts-are-stored))
