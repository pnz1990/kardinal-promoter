## kardinal dashboard

Open the kardinal UI dashboard in a browser (Kargo parity)

### Synopsis

Open the embedded kardinal UI in the default system browser.

The UI is served by the controller at /ui/ (default port 8082).
Uses port-forwarding to access the controller's UI port from localhost.

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
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

