## kardinal dashboard

Open the kardinal UI dashboard in a browser (Kargo parity)

### Synopsis

Open the embedded kardinal UI in the default system browser.

The UI is served by the controller at /ui/ (default port 8082). This command
only prints and opens the URL (default http://localhost:8082/ui/); it does not
port-forward. For an in-cluster controller, start a kubectl port-forward to
port 8082 first (see "Accessing the UI" in docs/installation.md).

Example:
  kardinal dashboard
  kardinal dashboard --address http://localhost:8082

```
kardinal dashboard [flags]
```

### Options

```
      --address string   Direct URL to the kardinal UI (skip auto-detection)
  -h, --help             help for dashboard
      --no-open          Print the URL without opening browser
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

