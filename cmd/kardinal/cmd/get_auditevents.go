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
)

func newGetAuditEventsCmd() *cobra.Command {
	var (
		pipeline string
		bundle   string
		env      string
		limit    int
	)

	cmd := &cobra.Command{
		Use:     "auditevents",
		Aliases: []string{"auditevent", "ae", "audit"},
		Short:   "List AuditEvent records — immutable promotion event log",
		Long: `List AuditEvents recording promotion lifecycle transitions.
AuditEvents are written by the controller at key points:
  PromotionStarted     — Bundle starts promoting through an environment
  PromotionSucceeded   — Health check passed; step reached Verified
  PromotionFailed      — Step reached Failed state
  PromotionSuperseded  — Newer Bundle superseded an in-flight promotion
  GateEvaluated        — PolicyGate changed readiness state
  RollbackStarted      — onHealthFailure=rollback triggered a rollback Bundle`,
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
	if len(events) == 0 {
		fmt.Fprintln(out, "No audit events found.")
		return nil
	}

	// Sort by timestamp descending (most recent first).
	sort.Slice(events, func(i, j int) bool {
		return events[i].Spec.Timestamp.After(events[j].Spec.Timestamp.Time)
	})

	// Apply limit.
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	// Print table.
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIMESTAMP\tPIPELINE\tBUNDLE\tENV\tACTION\tOUTCOME\tMESSAGE")
	for _, ae := range events {
		ts := ae.Spec.Timestamp.UTC().Format("2006-01-02T15:04Z")
		msg := ae.Spec.Message
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
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
