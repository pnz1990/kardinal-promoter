---
section: Added
---

- **`verify-receipt --image` reads every page of an image's referrers, and only the receipts of the pinned environment** (#1951). Receipt referrers are annotated with their `resourceUri` (`kardinal.io/receipt-resource`). Up to 1024 referrers and 64 pages are read; a page linked twice, a next page on another host, or `--timeout` (60 s) ends in "cannot verify". Past that, verify-receipt warns, and if the pinned environment's receipt is not among them it fails (unknown, never a pass). Before, the first 64 receipts of every environment were read, so one environment's receipt could be crowded out ([Verify a receipt](receipts.md#verify-a-receipt))
- **Receipt size and count** (#1951). Each PromotionReceipt has `status.bytes`, each Pipeline environment `status.environments[].receipts` (`count`, `bytes`), and the controller counts `kardinal_receipts_total{mode}` and `kardinal_receipt_bytes_total`. `kardinal doctor` warns once receipts pass 1 GiB while `receipts.retention.maxAge` is off (#2136) ([Receipts](receipts.md#size))
