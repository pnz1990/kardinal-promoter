// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

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
		Short: "Force re-reconciliation of a Pipeline (Kargo parity)",
		Long: `Force the controller to re-reconcile a Pipeline immediately.

Adds a kardinal.io/refresh annotation to the Pipeline, which triggers the
controller to run a reconciliation cycle. Useful when you need to re-evaluate
PolicyGates, re-check health adapters, or force a retry after a transient error.

Example:
  kardinal refresh nginx-demo`,
		Args: cobra.ExactArgs(1),
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
		uiAddress string
		noOpen    bool
	)

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the kardinal UI dashboard in a browser (Kargo parity)",
		Long: `Open the embedded kardinal UI in the default system browser.

The UI is served by the controller at /ui/ (default port 8082).
Uses port-forwarding to access the controller's UI port from localhost.

Example:
  kardinal dashboard
  kardinal dashboard --address http://localhost:8082`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dashboardFn(cmd.OutOrStdout(), uiAddress, noOpen)
		},
	}

	cmd.Flags().StringVar(&uiAddress, "address", "", "Direct URL to the kardinal UI (skip auto-detection)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Print the URL without opening browser")

	return cmd
}

func dashboardFn(w interface{ Write([]byte) (int, error) }, address string, noOpen bool) error {
	if address == "" {
		address = "http://localhost:8082/ui/"
	}

	if _, err := fmt.Fprintf(w, "kardinal UI: %s\n", address); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	if noOpen {
		return nil
	}

	if _, err := fmt.Fprintf(w, "Opening in browser...\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return openBrowser(address)
}

// openBrowser opens the given URL in the default system browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported OS: %s — navigate manually to %s", runtime.GOOS, url)
	}
	return cmd.Start()
}
