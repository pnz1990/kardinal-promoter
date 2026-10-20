## kardinal

kardinal manages promotion pipelines on Kubernetes

### Synopsis

kardinal promotes versions of your application through environments, with
gates, pull requests and health checks, on Kubernetes.

Getting started:
  kardinal quickstart --local                            try it on a local cluster
  kardinal init --name my-app --repo <gitops repo>       write a Pipeline for your repository
  kardinal ship my-app --image app:1.4 --wait            ship a version and follow it
  kardinal why my-app prod                               why it waits, and what moves it on

Defaults for a repository: in an application's repository the pipeline and the
namespace need not be repeated. A command that takes <pipeline> [<env>]
(why, explain, history, logs, ship, approve, promote, rollback, hold, unhold,
pause, resume, override, verify) takes, in order: the argument, then
KARDINAL_PIPELINE, then the pipeline of .kardinal.yaml; the namespace is -n,
then KARDINAL_NAMESPACE, then the file's namespace, then the kubeconfig
context's. The file is the nearest .kardinal.yaml from the working
directory up to the root of the git repository (outside a repository, the
working directory's only):

  pipeline: my-app
  namespace: team-a

With a default pipeline, no argument is that pipeline, and one argument is
an environment when the default pipeline has an environment of that name
(kardinal why prod), else a pipeline (kardinal why other-app); when the
cluster cannot say (the default pipeline cannot be read), the command
refuses rather than guess. Each default a command uses is said once on
stderr with its source ("using namespace team-a (from
/src/app/.kardinal.yaml)"), so a value the command line does not show is
never silent. The file may set pipeline and namespace only: it never names
a context or a cluster, and any other key is an error; -n (or
KARDINAL_NAMESPACE) and an explicit pipeline go without a broken file.
kardinal mcp's tools never read the file: the agent's working directory
may be a repository it does not trust.

Output follows where it goes. On a terminal, each state reads as a glyph and
its label in the status colours of the UI, from its design tokens
(web/tokens/tokens.json): Live (spruce), Promoting and Checking health
(thread red), Waiting for PR merge and gates not ready yet (dusk blue), Held
by gate (ochre), Failed (on garnet, the only filled state), Replaced by newer
version and Not reached (grey); Paused and Held by rollback are bold.
Without COLORTERM=truecolor and a COLORFGBG that names the background, they
are the nearest of the 16 ANSI colours, which the terminal's theme adapts.
Without a UTF-8 locale the glyphs are ASCII (`* Live`, `~ Waiting for PR merge`).

Piped to a file or another program, or with NO_COLOR (any value) or
TERM=dumb, output is plain words: the labels without glyphs or colour. The
cluster's text (a step's message, a Pipeline's summary) passes through as it
is when the locale is UTF-8 (LC_ALL, LC_CTYPE or LANG); without a UTF-8
locale, or with TERM=dumb, everything is ASCII (a dash is "-", an arrow
"->", any other non-ASCII character "?"). On Windows, colour needs a console
with virtual-terminal processing; without it output has no colour.

-o json and -o yaml print the API's values (Verified, WaitingForMerge) and the
cluster's text unchanged, and no table placeholders ("-", "--", "..."): a
field with nothing to say is left out. With -w, -o json prints one document
per refresh and -o yaml separates them with "---". Hints, prompts and the
CLI's own warnings go to stderr, so stdout stays valid JSON. The notices a
report shows about what it read (the ERROR: and WARNING: lines of status,
get pipelines and explain) are part of its output, on stdout, so
kardinal status my-app | less shows them; with -o json they are fields. get prints the objects as the API server
lists them. The others print these fields (in brackets the ones left out
when empty):

  create bundle, promote, rollback:
    bundle, pipeline, namespace, url, [type, version]; promote adds
    environment and from (the Bundle copied); rollback adds environment,
    rollbackOf (the Bundle restored), [replaces] (the one replaced) and,
    with --hold, hold {environment, bundle, [reason, expiresAt]}
  create bundle --dry-run:
    pipeline, namespace, nodes, environments [{name, [gates]}] in promotion
    order, [type, version, compact]
  unhold (release-hold):
    pipeline, namespace, environment, bundle (the version it was held on),
    url, [createdBy, reason]
  status:
    controller, pipelines, bundles, activeBundles, [degraded]
    (namespace/name)
  status <pipeline>:
    pipeline, namespace, source ("controller" or "cli"), environments,
    blockingGates, [phase, paused, summary, notices, warnings, regions];
    an environment is status.environments' entry: name, [bundle, version,
    incomingBundle, incomingVersion, state, since, blockedBy, prURL, held];
    a gate: name, instance, environment, bundle, expression, [message,
    reason, lastEvaluatedAt]
  explain:
    pipeline, namespace, entries [{environment, bundle, type (Step or
    PolicyGate), name, state, [expression, reason]}], [holds
    [{environment, bundle, [createdBy, reason, bundleMissing]}]]
  history:
    a list of {bundle, action (promote or rollback), environment, state,
    createdAt, [prURL, durationSeconds, running]}: durationSeconds once the
    step ended, running while it runs
  ship:
    the create bundle fields and exitCode, with --wait also [result,
    message, environments]

The CLI sends at most 50 requests a second to the API server, in bursts of
up to 300; KARDINAL_KUBE_QPS and KARDINAL_KUBE_BURST set other numbers, for
a small or busy control plane (KARDINAL_KUBE_QPS=10 KARDINAL_KUBE_BURST=20).

url is the pipeline in the UI: KARDINAL_UI_URL, else the address kardinal
dashboard opens (http://localhost:8082/ui/). For example
`kardinal create bundle my-app --image app:1.4 -o json | jq -r .bundle`
names the new Bundle.

### Options

```
      --context string      Kubeconfig context override
  -h, --help                help for kardinal
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal approve](kardinal-approve.md)	 - Approve (or reject) the version waiting for an environment's approval gates
* [kardinal audit](kardinal-audit.md)	 - Audit log commands: view and summarize promotion events
* [kardinal completion](kardinal-completion.md)	 - Generate shell completion scripts
* [kardinal create](kardinal-create.md)	 - Create kardinal resources
* [kardinal dashboard](kardinal-dashboard.md)	 - Open the kardinal UI dashboard in a browser
* [kardinal delete](kardinal-delete.md)	 - Delete kardinal resources
* [kardinal diff](kardinal-diff.md)	 - Show what changes between two versions (Bundles)
* [kardinal doctor](kardinal-doctor.md)	 - Run pre-flight checks to verify the cluster is correctly configured
* [kardinal freeze](kardinal-freeze.md)	 - Freeze agents, or stop the line for everyone; with no flags, list the freezes
* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources
* [kardinal history](kardinal-history.md)	 - Show a pipeline's promotion history, newest first
* [kardinal hold](kardinal-hold.md)	 - Keep an environment on what runs there now
* [kardinal inbox](kardinal-inbox.md)	 - List what waits for a person: on you first, then on others
* [kardinal init](kardinal-init.md)	 - Write a Pipeline for your GitOps repository, from flags or a few questions
* [kardinal logs](kardinal-logs.md)	 - Show promotion step execution logs for a pipeline
* [kardinal mcp](kardinal-mcp.md)	 - Serve kardinal to an AI agent over MCP (stdio)
* [kardinal metrics](kardinal-metrics.md)	 - Show promotion metrics (DORA-style) for a pipeline
* [kardinal override](kardinal-override.md)	 - Force-pass a PolicyGate with a mandatory audit record
* [kardinal pause](kardinal-pause.md)	 - Pause a pipeline, or one fleet: no new promotion steps start
* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates
* [kardinal promote](kardinal-promote.md)	 - Promote the version verified upstream into an environment
* [kardinal quickstart](kardinal-quickstart.md)	 - Install kardinal on a local cluster and promote a first version
* [kardinal refresh](kardinal-refresh.md)	 - Force re-reconciliation of a Pipeline
* [kardinal reject](kardinal-reject.md)	 - Reject a version: it is never promoted again, and rollback never picks it
* [kardinal resume](kardinal-resume.md)	 - Resume a paused pipeline, or a paused fleet
* [kardinal rollback](kardinal-rollback.md)	 - Roll back an environment to the version before
* [kardinal ship](kardinal-ship.md)	 - Ship a version: create a Bundle, and with --wait follow it
* [kardinal status](kardinal-status.md)	 - Show controller health or per-pipeline in-flight promotion details
* [kardinal unfreeze](kardinal-unfreeze.md)	 - Lift a freeze
* [kardinal unhold](kardinal-unhold.md)	 - Release an environment's hold (kardinal hold, or rollback --hold)
* [kardinal validate](kardinal-validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal verify](kardinal-verify.md)	 - Record a verdict on the version running in an environment (verify gates)
* [kardinal verify-receipt](kardinal-verify-receipt.md)	 - Verify a signed promotion receipt: its signature, subject, environment and levels
* [kardinal version](kardinal-version.md)	 - Print the CLI, controller, and kro (Graph) versions
* [kardinal why](kardinal-why.md)	 - Say why an environment waits, and what moves it on

