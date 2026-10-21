## kardinal verify

Record a verdict on the version running in an environment (verify gates)

### Synopsis

Record your verdict, in your Kubernetes name, on the version that runs in <env>
of <pipeline>: it passed, failed or was inconclusive for --check. Give a Bundle
instead with kardinal verify <bundle> --env <env>. A verify gate downstream
counts the verdicts of the checks it waits for (docs/agentic/verify-with-agents.md).
An agent's verdict counts only on a gate that lists it in verify.allowedAgents,
and never on work of its own party (its sponsor's, or its own).

Creates a Verdict for the Bundle, environment and check in your name: your
Kubernetes username and groups (a SelfSubjectReview) and your actor stamp
(docs/actors.md), owned by the Bundle. The chart's ValidatingAdmissionPolicy
refuses a Verdict in anyone else's name. A Verdict is never changed: a new one
from the same verifier replaces the old one as that verifier's vote.

A PolicyGate with spec.verify downstream of the environment counts the
Verdicts for its check: it is ready when its expression is true, at least
verify.required votes pass and no more than verify.maxFailures fail. An
agent's verdict counts only when the gate lists the agent in
verify.allowedAgents, and never on its own party's work.

  --fail, --inconclusive   need --summary.
  --revoke                 deletes your latest Verdict (or --name).

Asserted fields come from KARDINAL_ACTOR_MODEL, KARDINAL_ACTOR_SESSION and
KARDINAL_ACTOR_CLIENT. See docs/agentic/verify-with-agents.md.

```
kardinal verify <pipeline> <env> --check <check> (--pass | --fail | --inconclusive) [flags]
```

### Examples

```
  # an exploratory test agent in CI reports
  kardinal verify checkout uat --check exploratory-qa --pass \
    --summary "checkout, refunds and coupons work" --evidence https://ci.example.com/runs/88123

  kardinal verify checkout uat --check exploratory-qa --fail \
    --summary "coupon SAVE10 returns 500" --finding "high:coupon SAVE10 returns 500"

  # take your latest verdict back
  kardinal verify checkout uat --check exploratory-qa --revoke

  kardinal verify list checkout --env uat
```

### Options

```
      --check string          The check verified (a verify gate's verify.check)
  -e, --env string            Environment verified: where the version runs (or give it after the pipeline)
      --evidence string       A link to the evidence (a CI run, a report)
      --fail                  The version failed the check (needs --summary)
      --finding stringArray   A finding, as severity:title (info, low, medium, high, critical); repeatable
  -h, --help                  help for verify
      --inconclusive          The check could not decide (needs --summary)
      --name string           With --revoke: the Verdict to delete
      --pass                  The version passed the check
      --revoke                Delete your latest verdict for the version, environment and check
      --summary string        What you found, in a sentence or two
      --version string        Record only if this is the version that runs in the environment
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: budget, create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes
* [kardinal verify list](kardinal-verify-list.md)	 - List the verdicts on a version, and how its verify gates count them

