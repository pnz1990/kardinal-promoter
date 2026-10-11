// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ─── kardinal refresh ────────────────────────────────────────────────────────

func newRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <pipeline>",
		Short: "Force re-reconciliation of a Pipeline",
		Long: `Make the controller re-read <pipeline> now (status, Ready condition, metrics,
pause gate). It does not retry a Bundle, re-evaluate gates or re-run health
checks: those re-check on their own intervals.`,
		Example: `  kardinal refresh my-app`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("refresh: %w", err)
			}
			return refreshFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
}

func refreshFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &p); err != nil {
		return fmt.Errorf("get pipeline %s: %w", pipeline, err)
	}

	// Annotate with current timestamp to trigger reconciliation.
	patch := sigs_client.MergeFrom(p.DeepCopy())
	if p.Annotations == nil {
		p.Annotations = make(map[string]string)
	}
	p.Annotations["kardinal.io/refresh"] = metav1.Now().UTC().Format("2006-01-02T15:04:05Z")
	if err := c.Patch(ctx, &p, patch); err != nil {
		return fmt.Errorf("patch pipeline %s: %w", pipeline, err)
	}

	if _, err := fmt.Fprintf(w, "Pipeline %s marked for refresh. Controller will re-reconcile shortly.\n", pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// ─── kardinal dashboard ──────────────────────────────────────────────────────

func newDashboardCmd() *cobra.Command {
	var (
		uiAddress    string
		noOpen       bool
		controllerNS string
	)

	cmd := &cobra.Command{
		Use:   "dashboard [<pipeline> [<env>]]",
		Short: "Open the kardinal UI dashboard in a browser",
		Long: `Open the kardinal UI (served by the controller at /ui/) in your browser.
Without --address it port-forwards a local port (8082 when it is free) to the
controller in --controller-namespace, opens the UI and keeps forwarding until
Ctrl-C. With --address (or KARDINAL_UI_URL) it opens that URL and forwards
nothing. With <pipeline> it opens that pipeline, with <env> its stage.`,
		Example: `  kardinal dashboard
  kardinal dashboard my-app prod
  kardinal dashboard --address https://kardinal.example.com/ui/`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(contextOf(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			route := ""
			if len(args) > 0 {
				env := ""
				if len(args) > 1 {
					env = args[1]
				}
				route = uiRoute(linkNamespace(), args[0], env)
			}
			return dashboardFn(ctx, cmd.OutOrStdout(), uiAddress, controllerNS, noOpen, route)
		},
	}

	cmd.Flags().StringVar(&uiAddress, "address", "", "URL of the kardinal UI; forwards nothing (default: KARDINAL_UI_URL, else port-forward to the controller)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Print the URL without opening browser")
	cmd.Flags().StringVar(&controllerNS, "controller-namespace", defaultControllerNamespace, "Namespace of the kardinal-promoter controller to port-forward to")

	withReference(cmd, refreshReference)
	withReference(cmd, dashboardReference)
	return cmd
}

// contextOf is cmd's context, or a background one.
func contextOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// dashboardFn opens the UI at address, else KARDINAL_UI_URL, else through a
// port-forward to the controller in controllerNS, which it keeps until ctx
// is done.
func dashboardFn(ctx context.Context, w io.Writer, address, controllerNS string, noOpen bool, route string) error {
	source := "--address"
	if address == "" {
		address, source = os.Getenv("KARDINAL_UI_URL"), "KARDINAL_UI_URL"
	}
	if address != "" {
		if err := checkUIAddress(address); err != nil {
			return fmt.Errorf("dashboard: %s %w", source, err)
		}
	}
	var done <-chan error
	if address == "" {
		var err error
		address, done, err = forwardUI(ctx, controllerNS)
		if err != nil {
			return fmt.Errorf("dashboard: %w", err)
		}
	}

	address = withRoute(address, route)
	if _, err := fmt.Fprintf(w, "kardinal UI: %s\n", address); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if done != nil {
		_, _ = fmt.Fprintf(w, "Forwarding to the controller in %s; Ctrl-C stops.\n", controllerNS)
	}
	if !noOpen {
		if _, err := fmt.Fprintf(w, "Opening in browser...\n"); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		if err := openBrowser(address); err != nil {
			if done == nil {
				return err
			}
			_, _ = fmt.Fprintf(errOut, "could not open a browser (%v): open %s yourself\n", err, address)
		}
	}
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			return fmt.Errorf("dashboard: the port-forward ended: %w", err)
		}
		return nil
	}
}

// defaultUIAddress is where kardinal dashboard opens the UI without
// --address: the controller's UI port, forwarded to localhost.
const defaultUIAddress = "http://localhost:8082/ui/"

// uiRoute is the UI's route (#/p/<ns>/<pipeline>, with /env/<env> for a
// stage) of pipeline in ns, "" without a pipeline.
func uiRoute(ns, pipeline, env string) string {
	if pipeline == "" {
		return ""
	}
	r := "#/p/" + url.PathEscape(ns) + "/" + url.PathEscape(pipeline)
	if env != "" {
		r += "/env/" + url.PathEscape(env)
	}
	return r
}

// withRoute is the UI address with route in place of its fragment.
func withRoute(address, route string) string {
	if route == "" {
		return address
	}
	if i := strings.Index(address, "#"); i >= 0 {
		address = address[:i]
	}
	return address + route
}

// checkUIAddress is why address is not a UI address kardinal links to or
// hands to the system's opener (open, xdg-open, rundll32), or nil: it must be
// an http or https URL with a host, in printable ASCII (no control or
// format characters, such as a bidi override, and no spaces), and without
// userinfo (user:password@, which can make a link look like another host).
// So it never starts with "-" (an opener's option) and is never a file, an
// app or a javascript: link.
func checkUIAddress(address string) error {
	bad := strings.IndexFunc(address, func(r rune) bool { return r <= ' ' || r >= 0x7f }) >= 0
	u, err := url.Parse(address)
	if bad || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || strings.HasPrefix(address, "-") {
		return fmt.Errorf("%q is not an http(s) URL with a host, in ASCII and without user@: set it to the UI's address, e.g. https://kardinal.example.com/ui/", address)
	}
	return nil
}

// uiBase is KARDINAL_UI_URL when it is a UI address (checkUIAddress), and
// why it is not.
func uiBase() (string, error) {
	base := os.Getenv("KARDINAL_UI_URL")
	if base == "" {
		return "", nil
	}
	if err := checkUIAddress(base); err != nil {
		return "", fmt.Errorf("KARDINAL_UI_URL %w", err)
	}
	return base, nil
}

// uiLink is the UI's page of pipeline (and env) in ns when the UI's address
// is known (KARDINAL_UI_URL), else "": a port-forward may not be running.
func uiLink(ns, pipeline, env string) (string, error) {
	base, err := uiBase()
	if base == "" {
		return "", err
	}
	return withRoute(base, uiRoute(ns, pipeline, env)), nil
}

// printUILink prints, on stderr, the UI page of pipeline (and env) in ns
// when the UI's address is known (KARDINAL_UI_URL), or a warning when it is
// not a UI address.
func printUILink(ns, pipeline, env string) {
	link, err := uiLink(ns, pipeline, env)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(errOut, "%s no UI link: %v\n", sty.Warning("warning:"), err)
	case link != "":
		_, _ = fmt.Fprintf(errOut, "%s %s\n", sty.Dim("UI:"), link)
	}
}

// linkNamespace is the namespace a link names: -n, else the kubeconfig
// context's, else default.
func linkNamespace() string {
	if globalNamespace != "" {
		return globalNamespace
	}
	if _, ns, err := buildRestConfig(); err == nil && ns != "" {
		return ns
	}
	return "default"
}

// uiURL is the UI's page of pipeline in ns (uiRoute): on KARDINAL_UI_URL,
// else defaultUIAddress; "" when KARDINAL_UI_URL is not a UI address.
func uiURL(ns, pipeline string) string {
	base, err := uiBase()
	switch {
	case err != nil:
		return ""
	case base == "":
		base = defaultUIAddress
	}
	return withRoute(base, uiRoute(ns, pipeline, ""))
}

// openBrowser opens url, a UI address (checked again here: nothing else
// reaches the opener), in the system's browser. The check keeps a leading
// "-" out, so no "--" is needed (xdg-open refuses one).
func openBrowser(url string) error {
	if err := checkUIAddress(url); err != nil {
		return fmt.Errorf("open a browser: %w", err)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported OS: %s; open %s in a browser", runtime.GOOS, url)
	}
	return cmd.Start()
}

// refreshReference is the detail of the command for its reference page.
const refreshReference = `Sets the kardinal.io/refresh annotation on the Pipeline to the current time.
The change requeues the Pipeline reconciler, which re-derives the Pipeline's
status (phase, Ready condition, deployment metrics) and its pause freeze gate.
Bundle reconcilers follow Pipeline spec changes only, so this does not retry
a Bundle. PolicyGates and PromotionSteps do not watch Pipelines, so this
does not re-evaluate gates or re-run health checks; they re-check on their
own intervals.`

// dashboardReference is the detail of the command for its reference page.
const dashboardReference = `The UI is served by the controller at /ui/ (port 8082, the Service port named
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
#/p/<namespace>/<pipeline> page, empty when KARDINAL_UI_URL is not one.`
