// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"io"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// Test-only helpers: production code does not call these (make hygiene).

// stepLabel is the label of a PromotionStep state ("" is Pending).
func stepLabel(state string) string {
	if state == "" {
		state = "Pending"
	}
	return display.Step(state).Label
}

// gateLabel is the label of a gate instance state (graph.GateState).
func gateLabel(state string) string { return display.Gate(state).Label }

// FormatPipelineTableFull writes a table of pipelines to w. VERSION is each
// pipeline's current Bundle (lifecycle.CurrentBundle, the UI's
// activeBundleName) and every environment column describes that Bundle
// (pipelineEnvStates). bundles and steps may span namespaces and pipelines.
// When subs is non-nil a SUB column counts the Watching Subscriptions per
// pipeline. showNamespace prepends a NAMESPACE column (--all-namespaces).
func FormatPipelineTableFull(w io.Writer, pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep, subs []v1alpha1.Subscription, showNamespace bool) error {
	return formatPipelines(w, pipelines, bundles, steps, nil, subs, showNamespace, false)
}

// FormatBundleTable writes a tabwriter-formatted table of bundles to w.
func FormatBundleTable(w io.Writer, bundles []v1alpha1.Bundle) error {
	return formatBundleTable(w, bundles, false)
}

// FormatStepsTable writes a tabwriter-formatted table of promotion steps to w.
// When multiple bundles have steps for the same environment (e.g. after rapid
// successive deploys), only the most-active step per environment is shown using
// the same priority logic as the pipeline table: active states (Promoting,
// WaitingForMerge, HealthChecking) take precedence over terminal states (Verified,
// Failed). Within same priority, the most recently created step wins.
func FormatStepsTable(w io.Writer, steps []v1alpha1.PromotionStep) error {
	return formatStepsTable(w, steps, false)
}

func getAuditEventsFn(out io.Writer, client sigs_client.Client, ns, pipeline, bundle, env string, limit int) error {
	return getAuditEventsFnNS(out, client, ns, pipeline, bundle, env, limit, false)
}

func getBundlesFn(out io.Writer, client sigs_client.Client, ns string, args []string, activeOnly bool) error {
	return getBundlesFnNS(out, client, ns, args, activeOnly, false)
}

// getStepsOnce fetches and renders a single snapshot of PromotionStep status.
func getStepsOnce(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	return getStepsOnceNS(w, c, ns, pipeline, false)
}

// FormatSubscriptionTable writes a tabwriter-formatted table of subscriptions to w.
// When showNamespace is true, a NAMESPACE column is prepended.
func FormatSubscriptionTable(w io.Writer, subs []v1alpha1.Subscription, showNamespace bool) error {
	return formatSubscriptionTable(w, subs, showNamespace, nil)
}

// statusSummaryFn is statusSummaryIn across every namespace.
func statusSummaryFn(out io.Writer, c sigs_client.Reader, controllerNS string) error {
	return statusSummaryIn(context.Background(), out, c, controllerNS, "", "default")
}
