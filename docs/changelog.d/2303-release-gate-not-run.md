---
section: Development
---

- **The release gate says what it does not run** (#2303, #2304, #2282) — its report lists each item of `hack/release-gate/not-run.txt` (receipts signed with a real cloud KMS key, a real EKS cluster, public-good sigstore, real ServiceNow and Jira, what `release.yml` publishes, and running the artifacts for platforms other than linux/amd64) with why the gate does not run it and the coverage rows or `release.yml` job that stand in for it, so a GO verdict does not read as proof of them. A covered `test/e2e/coverage.tsv` row that no test claims any more holds the verdict. A suite `hack/e2e/matrix.txt` pins to its minors runs on those only, and the report lists its other minor as PINNED with the reason. The gate builds every binary `release.yml` ships, for its platform. Live suites on one host take cluster slots first come, first served, the release gate's first, and `hack/e2e/local-live.sh` takes `-run`, `-count` and `-p`.
