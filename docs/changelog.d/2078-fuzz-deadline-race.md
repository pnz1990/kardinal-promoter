---
section: Development
---

- **CI's fuzz job and the release gate's fuzz steps run through `hack/ci/fuzz.sh`** (#2078). Go's fuzz coordinator can fail a run that found nothing with "context deadline exceeded" when `-fuzztime` ends: the deadline closes its Done channel before it cancels the workers' context, and the coordinator compares the two errors in between.
  - **The race:** such a run, which wrote no failing input, now counts as the seed corpus's result.
  - **Unchanged:** a failing input written, or any other failure, still fails.
