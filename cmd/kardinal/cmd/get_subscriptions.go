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
	"strings"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

func newGetSubscriptionsCmd() *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:         "subscriptions [name]",
		Annotations: map[string]string{outputAnnotation: "true", wideAnnotation: "true"},
		Aliases:     []string{"subscription", "sub"},
		Short:       "List Subscriptions (passive artifact watchers)",
		Long: `List Subscriptions: watchers of an OCI registry or a git repository that create
a Bundle when a new artifact appears.`,
		Example: `  kardinal get subscriptions
  kardinal get subscriptions -A`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetSubscriptions(cmd, args, allNamespaces)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"List subscriptions across all namespaces (adds NAMESPACE column)")
	withReference(cmd, getSubscriptionsReference)
	return cmd
}

func runGetSubscriptions(cmd *cobra.Command, args []string, allNamespaces bool) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get subscriptions: %w", err)
	}
	return getSubscriptionsFn(cmd.OutOrStdout(), c, ns, args, allNamespaces)
}

// getSubscriptionsFn is the testable implementation of get subscriptions: it
// lists the Subscriptions of ns (every namespace with allNamespaces), keeps
// the one named args[0] when given, and writes them in the --output format.
func getSubscriptionsFn(w io.Writer, c sigs_client.Client, ns string, args []string, allNamespaces bool) error {
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
		return WriteJSON(w, items)
	case "yaml":
		return WriteYAML(w, items)
	default:
		// The table names each last Bundle by its version: one token, or
		// with -o wide the whole version.
		versions := map[string]string{}
		var bundles v1alpha1.BundleList
		if err := c.List(context.Background(), &bundles, opts...); err == nil {
			for i := range bundles.Items {
				b := &bundles.Items[i]
				v := subscriptionVersion(b)
				if OutputFormat() == "wide" {
					v = createdOf(b).Version
				}
				versions[b.Namespace+"/"+b.Name] = v
			}
		}
		return formatSubscriptionTable(w, items, allNamespaces, versions)
	}
}

// subscriptionVersion is b's version as one token, for LAST-VERSION: a
// chart's version (not "name 1.2.3"), a config Bundle's commit (not "config
// abc1234"), else its version when that is one word, else "" (the Bundle's
// name is shown).
func subscriptionVersion(b *v1alpha1.Bundle) string {
	switch {
	case b.Spec.Type == "chart" && b.Spec.Chart != nil && b.Spec.Chart.Version != "":
		return b.Spec.Chart.Version
	case (b.Spec.Type == "config" || len(b.Spec.Images) == 0) && b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "":
		sha := b.Spec.ConfigRef.CommitSHA
		return sha[:min(7, len(sha))]
	}
	if v := createdOf(b).Version; len(strings.Fields(v)) == 1 {
		return v
	}
	return ""
}

// FormatSubscriptionTable writes a tabwriter-formatted table of subscriptions to w.
// When showNamespace is true, a NAMESPACE column is prepended.
func FormatSubscriptionTable(w io.Writer, subs []v1alpha1.Subscription, showNamespace bool) error {
	return formatSubscriptionTable(w, subs, showNamespace, nil)
}

// formatSubscriptionTable is FormatSubscriptionTable; versions maps
// namespace/name to a Bundle's version for LAST-VERSION.
func formatSubscriptionTable(w io.Writer, subs []v1alpha1.Subscription, showNamespace bool, versions map[string]string) error {
	tw := style.NewTable(w, 3)

	wide := OutputFormat() == "wide"
	header := "NAME\tTYPE\tPIPELINE\tPHASE\tLAST-CHECK\tLAST-VERSION\tAGE"
	if wide {
		header = "NAME\tTYPE\tPIPELINE\tPHASE\tLAST-CHECK\tLAST-VERSION\tLAST-BUNDLE\tAGE"
	}
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
		// LAST-VERSION is one token (the tag, the chart version or the
		// config commit, else the Bundle's name), so the columns after it
		// stay where scripts read them; -o wide shows the whole version and
		// adds LAST-BUNDLE.
		lastVersion, lastBundle := "-", "-"
		if n := s.Status.LastBundleCreated; n != "" {
			lastVersion, lastBundle = versionOr(versions[s.Namespace+"/"+n], n), n
		}

		cells := []string{s.Name, string(s.Spec.Type), s.Spec.Pipeline, phase, lastCheck, lastVersion}
		if wide {
			cells = append(cells, lastBundle)
		}
		row := strings.Join(append(cells, HumanAge(s.CreationTimestamp.Time)), "\t")
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

// getSubscriptionsReference is the detail of the command for its reference page.
const getSubscriptionsReference = `Subscriptions passively watch OCI registries or Git repositories and
automatically create Bundles when new artifacts are detected.

LAST-VERSION is the version of the last Bundle a Subscription created, as
one token: an image's tag, a git Subscription's commit (7 characters), a
helm Subscription's chart version, else the Bundle's name; so the columns
after it stay where a script reads them. -o wide shows the whole version
("config abc1234", "podinfo 6.15.0") and adds LAST-BUNDLE.`
