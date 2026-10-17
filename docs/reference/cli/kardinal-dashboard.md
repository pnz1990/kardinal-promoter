## kardinal dashboard

Open the kardinal UI dashboard in a browser

### Synopsis

Open the kardinal UI (served by the controller at /ui/) in your browser.
Without --address it port-forwards a local port (8082 when it is free) to the
controller in --controller-namespace, opens the UI and keeps forwarding until
Ctrl-C. With --address (or KARDINAL_UI_URL) it opens that URL and forwards
nothing. With <pipeline> it opens that pipeline, with <env> its stage.

The UI is served by the controller at /ui/ (port 8082, the Service port named
ui). Without --address or KARDINAL_UI_URL, dashboard finds the
kardinal-promoter Service with a ui port in --controller-namespace (default
kardinal-system) and a ready Pod behind it, forwards 127.0.0.1:8082 (another
free port when 8082 is taken) to that Pod, prints the URL, opens it (not with
--no-open) and keeps forwarding until Ctrl-C. It needs get on services and
endpointslices and create on pods/portforward in that namespace. With
--address it prints and opens that URL, and forwards nothing (see "Accessing
the UI" in docs/installation.md).

With <pipeline> it opens the UI's page of it (#/p/<namespace>/<pipeline>, the
namespace from -n or the kubeconfig context), with <env> that stage's
drawer (/env/<env>). kardinal why and kardinal status <pipeline> print the
same link on stderr when KARDINAL_UI_URL says where the UI is.

--address and KARDINAL_UI_URL must be http or https URLs with a host, in
printable ASCII (punycode a non-ASCII host) and without user@: dashboard refuses another value
before it opens anything, and why and status print a warning instead of the
link. The url of -o json (create bundle, promote, rollback) is the same
#/p/<namespace>/<pipeline> page, empty when KARDINAL_UI_URL is not one.

```
kardinal dashboard [<pipeline> [<env>]] [flags]
```

### Examples

```
  kardinal dashboard
  kardinal dashboard my-app prod
  kardinal dashboard --address https://kardinal.example.com/ui/
```

### Options

```
      --address string                URL of the kardinal UI; forwards nothing (default: KARDINAL_UI_URL, else port-forward to the controller)
      --controller-namespace string   Namespace of the kardinal-promoter controller to port-forward to (default "kardinal-system")
  -h, --help                          help for dashboard
      --no-open                       Print the URL without opening browser
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

