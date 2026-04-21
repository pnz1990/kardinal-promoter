// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newGetSubscriptionsCmd() *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "subscriptions [name]",
		Aliases: []string{"subscription", "sub"},
		Short:   "List Subscriptions (passive artifact watchers)",
		Long: `List Subscriptions and their watching status.

Subscriptions passively watch OCI registries or Git repositories and
automatically create Bundles when new artifacts are detected.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetSubscriptions(cmd, args, allNamespaces)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"List subscriptions across all namespaces (adds NAMESPACE column)")
	return cmd
}

func runGetSubscriptions(cmd *cobra.Command, args []string, allNamespaces bool) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get subscriptions: %w", err)
	}

	var opts []sigs_client.ListOption
	if !allNamespaces {
		opts = append(opts, sigs_client.InNamespace(ns))
	}

	var subs v1alpha1.SubscriptionList
	if err := c.List(context.Background(), &subs, opts...); err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}

	// Filter by name if provided.
	items := subs.Items
	if len(args) == 1 {
		name := args[0]
		filtered := items[:0]
		for _, s := range items {
			if s.Name == name {
				filtered = append(filtered, s)
			}
		}
		items = filtered
	}

	switch OutputFormat() {
	case "json":
		return WriteJSON(cmd.OutOrStdout(), items)
	case "yaml":
		return WriteYAML(cmd.OutOrStdout(), items)
	default:
		return FormatSubscriptionTable(cmd.OutOrStdout(), items, allNamespaces)
	}
}

// FormatSubscriptionTable writes a tabwriter-formatted table of subscriptions to w.
// When showNamespace is true, a NAMESPACE column is prepended.
func FormatSubscriptionTable(w io.Writer, subs []v1alpha1.Subscription, showNamespace bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)

	header := "NAME\tTYPE\tPIPELINE\tPHASE\tLAST-CHECK\tLAST-BUNDLE\tAGE"
	if showNamespace {
		header = "NAMESPACE\t" + header
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write subscription table header: %w", err)
	}

	for _, s := range subs {
		phase := s.Status.Phase
		if phase == "" {
			phase = "Unknown"
		}
		lastCheck := "-"
		if s.Status.LastCheckedAt != "" {
			if t, err := time.Parse(time.RFC3339, s.Status.LastCheckedAt); err == nil {
				lastCheck = HumanAge(t) + " ago"
			} else {
				lastCheck = s.Status.LastCheckedAt
			}
		}
		lastBundle := s.Status.LastBundleCreated
		if lastBundle == "" {
			lastBundle = "-"
		}

		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
			s.Name,
			string(s.Spec.Type),
			s.Spec.Pipeline,
			phase,
			lastCheck,
			lastBundle,
			HumanAge(s.CreationTimestamp.Time),
		)
		if showNamespace {
			row = s.Namespace + "\t" + row
		}
		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write subscription row: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush subscription table: %w", err)
	}
	return nil
}
