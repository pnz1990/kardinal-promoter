## kardinal ship

Ship a version: create a Bundle, and with --wait follow it

### Synopsis

Ship a version through <pipeline>: the same Bundle as kardinal create bundle,
with the same flags. --wait follows it until it is live everywhere it goes
(exit 0), fails (1), stops at a gate or a hold (3), waits for its PR to be
merged (4), is replaced or rejected (5), or --timeout passes (6).

ship takes create bundle's flags and creates the same Bundle; create bundle
stays. Without --wait it prints the Bundle's name and, on stderr, how to
follow it.

--wait follows the Bundle, printing on stderr each new state of its
environments after the time since the start (+1m5s), the URL of each PR it
opens, and at the end a summary: each environment's last state and how long
its own promotion there took ("took": from its first step's start, else from
its upstream going live, else from the Bundle's creation), or how long it has
waited in it ("for"). --quiet (-q) prints none of that.
It ends with one line on stdout (or, with -o json, an object with
bundle, pipeline, namespace, type, version, url, result, exitCode, message
and the Bundle's environments from status.environments). Exit codes:

  CODE  RESULT                         WHEN
  0     Verified                       live in every environment it promotes
  1     Failed, Error                  it failed, or it could not be read three times in a row (or the command itself failed)
  3     Held                           a person is needed and nothing else of it is moving: an approval gate, a hold, a paused pipeline, or a gate that does not pass and does not open by itself (with --stop-at-gate, any gate that does not pass)
  4     WaitingForMerge                its PR waits for a person to merge it (not one the SCM merges by itself, pr.merge.auto), and nothing else of it is moving
  5     Superseded, Rejected, Deleted  a newer version replaced it, it was rejected, or it was deleted
  6     Timeout, Interrupted           --timeout (default 30m) passed, or Ctrl-C or SIGTERM interrupted the wait; the result is printed first

A stop at a gate, a hold or a PR counts once two reads agree, and a gate not
evaluated yet never stops it. A gate that does not pass is waited for, until
--timeout, when it opens by itself: its expression, outside its string
literals, reads one of these variables that change with time (with a . or
[ after it: schedule.hour, schedule["hour"]):

  schedule                    schedule.isWeekend, schedule.hour, schedule.dayOfWeek
  upstream                    upstream.<env>.soakMinutes and every other field of upstream.<env>
  bundle.upstreamSoakMinutes  the soak of the environment before this one
  metrics                     metrics.<name>.value, metrics.<name>.result
  changewindow                changewindow.<name>

So CI can wait through a soak or a schedule. A PR the SCM merges by itself
(pr.merge.auto) is waited for too. Any other gate that does not pass (on a
Bundle label, its provenance, its version) needs a person: exit 3, as do an
approval gate, a hold and a paused pipeline. --stop-at-gate stops at any
gate that does not pass. Stopping to follow does not stop the promotion:
kardinal status <pipeline> -w goes on following it. The codes
are stable: a new outcome gets a new code.

```
kardinal ship <pipeline> --image <ref> [flags]
```

### Examples

```
  kardinal ship my-app --image ghcr.io/org/my-app:1.5.0
  kardinal ship my-app --image ghcr.io/org/my-app:1.5.0 --wait --timeout 1h

  # in CI: the Bundle's name and the outcome as JSON
  kardinal ship my-app --image "$IMAGE" --wait -o json | jq -r .result
```

### Options

```
      --author string             Author or CI actor of the build (provenance)
      --ci-run-url string         Absolute http(s) URL of the CI run that built the Bundle (provenance)
      --commit string             Source commit SHA that produced the Bundle (provenance)
      --commit-time string        When the source commit was made, RFC 3339 (provenance; lead time starts at it, e.g. "$(git show -s --format=%cI)")
      --config-commit string      Commit SHA of the config repository to promote; required for config and mixed Bundles
      --config-repo string        Git URL of the config repository (default: the Pipeline's git.url)
      --dry-run                   Preview the promotion graph without creating any cluster resources
      --graph-compact-above int   With --dry-run: the controller's --graph-compact-above (chart graph.compactAbove), the environment count above which the Graph is compact (default 100)
  -h, --help                      help for ship
      --image stringArray         Container image reference (can be specified multiple times); required for image and mixed Bundles
  -q, --quiet                     With --wait: print only the last line (or -o's document); no progress, PR links or summary on stderr
      --stop-at-gate              With --wait: stop (exit 3) at any gate that does not pass, also one that opens by itself (a soak, a schedule)
      --timeout duration          With --wait: give up after this long (exit 6) (default 30m)
      --type string               Bundle type: image, config or mixed (chart Bundles come from POST /api/v1/bundles or a Helm Subscription: this command has no chart flags) (default "image")
      --wait                      Follow the version until it is live, fails, stops at a gate, a hold or a PR, or is replaced
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

