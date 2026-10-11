## kardinal dashboard

Open the kardinal UI dashboard in a browser

### Synopsis

Open the kardinal UI (served by the controller at /ui/) in your browser.
Without --address it port-forwards a local port (8082 when it is free) to the
controller in --controller-namespace, opens the UI and keeps forwarding until
Ctrl-C. With --address (or KARDINAL_UI_URL) it opens that URL and forwards
nothing.

The UI is served by the controller at /ui/ (port 8082, the Service port named
ui). Without --address or KARDINAL_UI_URL, dashboard finds the
kardinal-promoter Service with a ui port in --controller-namespace (default
kardinal-system) and a ready Pod behind it, forwards 127.0.0.1:8082 (another
free port when 8082 is taken) to that Pod, prints the URL, opens it (not with
--no-open) and keeps forwarding until Ctrl-C. It needs get on services and
endpointslices and create on pods/portforward in that namespace. With
--address it prints and opens that URL, and forwards nothing (see "Accessing
the UI" in docs/installation.md).

```
kardinal dashboard [flags]
```

### Examples

```
  kardinal dashboard
  kardinal dashboard --no-open
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
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

