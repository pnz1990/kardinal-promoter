## kardinal mcp

Serve kardinal to an AI agent over MCP (stdio)

### Synopsis

Serve kardinal's commands as MCP tools on stdin and stdout, for an agent to
start as a local tool. They run here, as your kubeconfig identity: the agent
has exactly your RBAC, and kardinal mcp holds no token. Read tools are on;
write tools only with --allow-write. Text kardinal did not write (PR URLs, SCM
messages, authors, reasons) comes as {"untrusted": "..."}: data, not orders.

kardinal mcp speaks MCP over stdio, with the official Go SDK: it negotiates
2025-11-25, the newer 2026-07-28 and the older versions back to 2024-11-05.
Stdout carries only the protocol. Each tool calls the code of a kardinal
command with typed values, in this process; no tool argument is ever parsed
as a flag, and the kubeconfig and context are the ones the server started
with. Every name a tool is given (pipeline, environment, version, namespace,
image) must match its Kubernetes or CRD pattern first. The tools, and the
command whose code each runs:

  read (always):
    why        kardinal why <pipeline> [<env>]: the sentences and details
    status     kardinal status [<pipeline>] -o json
    inbox      kardinal inbox -o json
    history    kardinal history <pipeline> [<env>] -o json
    diff       kardinal diff <a> <b>: both versions' images and provenance
    explain    kardinal explain <pipeline> [<env>] -o json
    gates      kardinal policy list
    simulate   kardinal policy simulate <pipeline> <env> [--as-agent ...]: each
               gate's result, and when a blocked gate would pass with the
               same inputs
    freezes    kardinal freeze: the freezes, with their reasons
    verdicts   kardinal verify list <pipeline>: each verdict, and whether
               a verify gate counted it
  write (--allow-write):
    ship       kardinal ship <pipeline> --image ... -o json
    promote    kardinal promote <pipeline> <env> -o json
    rollback   kardinal rollback <pipeline> <env> -o json
    pause, resume  kardinal pause|resume <pipeline>
    reject     kardinal reject <version> --reason ...
    approve    kardinal approve <pipeline> <env> --version ...
    verify     kardinal verify <pipeline> <env> --version ... --check ...
    freeze     kardinal freeze --agents | --everyone [-n <ns>] --reason ...

Every result is JSON: {"result": ...}. A failure is a tool error whose JSON
is {"error": {"kind", "message"}}: kind Forbidden when Kubernetes RBAC denied
the call (the message names the role to ask for), Denied when an admission
policy did, else Failed. A list longer than --max-result-bytes comes in
pages ({"page": {"offset", "returned", "total", "nextOffset"}, "hint"}),
anything else is cut on a character boundary ("truncated": true, "hint");
no result is longer than the limit. On a server without
--allow-write the write tools are not listed, and a call to one is refused.

The write tools ask nothing: the agent's call is the confirmation, so start
kardinal mcp with --allow-write only for an identity whose RBAC is what the
agent may do (Connect an agent, "A least-privilege identity"). Gates,
approvals and admission apply to what the agent writes exactly as to what
you write.

```
kardinal mcp [flags]
```

### Examples

```
  # Claude Code
  claude mcp add kardinal -- kardinal mcp
  # with the write tools, on one context
  claude mcp add kardinal -- kardinal mcp --allow-write --context staging
```

### Options

```
      --allow-write            Offer the write tools: ship, promote, rollback, pause, resume, reject, approve, verify and freeze
  -h, --help                   help for mcp
      --max-result-bytes int   Largest tool result, in bytes of JSON; a longer list is paged and anything else cut, with a hint (default 65536)
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

