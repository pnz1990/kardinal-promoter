// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newGetAuditEventsCmd() *cobra.Command {
	var (
		pipeline string
		bundle   string
		env      string
		limit    int
	)

	cmd := &cobra.Command{
		Use:         "auditevents",
		Aliases:     []string{"auditevent", "ae", "audit"},
		Short:       "List AuditEvent records — immutable promotion event log",
		Annotations: map[string]string{outputAnnotation: "true"},
		Long: `List AuditEvents recording promotion lifecycle transitions.
AuditEvents are written by the controller at key points:
  PromotionStarted     — Bundle starts promoting through an environment
  PromotionSucceeded   — Health check passed; step reached Verified
  PromotionFailed      — Step reached Failed state
  PromotionSuperseded  — Newer Bundle superseded an in-flight promotion
  PromotionRejected    — kardinal reject cancelled an in-flight promotion
  GateEvaluated        — PolicyGate changed readiness state
  GateOverridden       — An override was recorded on a gate (verified author)
  RollbackStarted      — onHealthFailure=rollback triggered a rollback Bundle
  RollbackSucceeded    — A rollback Bundle's step reached Verified (written
                         besides PromotionSucceeded, once per step)

Events are listed most recent first, at most --limit of them. -o json and
-o yaml print the same events as a list of AuditEvent objects ([] when there
are none).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetAuditEvents(cmd, pipeline, bundle, env, limit)
		},
	}

	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Filter by pipeline name")
	cmd.Flags().StringVar(&bundle, "bundle", "", "Filter by bundle name")
	cmd.Flags().StringVar(&env, "env", "", "Filter by environment name")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results to show (0 = unlimited)")

	return cmd
}

func runGetAuditEvents(cmd *cobra.Command, pipeline, bundle, env string, limit int) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get auditevents: %w", err)
	}
	return getAuditEventsFn(cmd.OutOrStdout(), c, ns, pipeline, bundle, env, limit)
}

func getAuditEventsFn(out io.Writer, client sigs_client.Client, ns, pipeline, bundle, env string, limit int) error {

	var aeList v1alpha1.AuditEventList
	listOpts := []sigs_client.ListOption{sigs_client.InNamespace(ns)}

	// Apply label selectors for filters.
	if pipeline != "" || bundle != "" || env != "" {
		matchLabels := map[string]string{}
		if pipeline != "" {
			matchLabels["kardinal.io/pipeline"] = pipeline
		}
		if bundle != "" {
			matchLabels["kardinal.io/bundle"] = bundle
		}
		if env != "" {
			matchLabels["kardinal.io/environment"] = env
		}
		listOpts = append(listOpts, sigs_client.MatchingLabels(matchLabels))
	}

	if err := client.List(context.Background(), &aeList, listOpts...); err != nil {
		return fmt.Errorf("list auditevents: %w", err)
	}

	events := aeList.Items
	if events == nil {
		events = []v1alpha1.AuditEvent{} // -o json prints [], not null
	}

	// Most recent first; within a second, in the order they were written.
	sort.SliceStable(events, func(i, j int) bool {
		return lifecycle.CompareAuditEvents(&events[i], &events[j]) > 0
	})

	// Apply limit.
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	switch OutputFormat() {
	case "json":
		return WriteJSON(out, events)
	case "yaml":
		return WriteYAML(out, events)
	}
	if len(events) == 0 {
		_, _ = fmt.Fprintln(out, "No audit events found.")
		return nil
	}

	// Print table.
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIMESTAMP\tPIPELINE\tBUNDLE\tENV\tACTION\tOUTCOME\tMESSAGE")
	for _, ae := range events {
		ts := ae.Spec.Timestamp.UTC().Format("2006-01-02T15:04Z")
		msg := truncateRunes(ae.Spec.Message, 50)
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			ts,
			ae.Spec.PipelineName,
			ae.Spec.BundleName,
			ae.Spec.Environment,
			ae.Spec.Action,
			ae.Spec.Outcome,
			msg,
		)
	}
	return tw.Flush()
}
