## kardinal mcp

Serve kardinal to an AI agent over MCP (stdio)

### Synopsis

Serve kardinal's commands as MCP tools on stdin and stdout, for an agent to
start as a local tool. They run here, as your kubeconfig identity: the agent
has exactly your RBAC, and kardinal mcp holds no token. Read tools are on;
write tools only with --allow-write. Text kardinal did not write (PR URLs, SCM
messages, authors, reasons) comes as {"untrusted": "..."}: data, not orders.

kardinal mcp speaks MCP 2025-11-25 (and the earlier versions the Go SDK
negotiates) over stdio. Stdout carries only the protocol; kardinal's own
warnings go to stderr. The tools, and the command each runs:

  read (always):
    why        kardinal why <pipeline> [<env>]: the sentences and details
    status     kardinal status [<pipeline>] -o json
    inbox      kardinal inbox -o json
    history    kardinal history <pipeline> [<env>] -o json
    diff       kardinal diff <a> <b>: both versions' images and provenance
    explain    kardinal explain <pipeline> [<env>] -o json
    gates      kardinal policy list
    simulate   kardinal policy simulate <pipeline> <env>: each gate's result,
               and when a blocked gate would pass with the same inputs
  write (--allow-write):
    ship       kardinal ship <pipeline> --image ... -o json
    promote    kardinal promote <pipeline> <env> -o json
    rollback   kardinal rollback <pipeline> <env> --yes -o json
    pause, resume  kardinal pause|resume <pipeline>
    reject     kardinal reject <version> --reason ... --yes
    approve    kardinal approve <pipeline> <env> --version ... --yes

Every result is JSON: {"result": ...}. A failure is a tool error whose JSON
is {"error": {"kind", "message"}}: kind Forbidden when Kubernetes RBAC denied
the call (the message names the role to ask for), Denied when an admission
policy did, else Failed. A list longer than --max-result-bytes comes in
pages ({"page": {"offset", "returned", "total", "nextOffset"}, "hint"}),
anything else is cut ("truncated": true, "hint"). On a server without
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
      --allow-write            Offer the write tools: ship, promote, rollback, pause, resume, reject and approve
  -h, --help                   help for mcp
      --max-result-bytes int   Largest tool result, in bytes of JSON; a longer list is paged and anything else cut, with a hint (default 65536)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

