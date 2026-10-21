// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newBudgetCmd() *cobra.Command {
	var ledger string
	cmd := &cobra.Command{
		Use:   "budget <agent>",
		Short: "Show an agent's budgets: what it has used, what waits, and the exemptions",
		Long: `Show the budgets of agent <agent> (its AgentIdentity spec.budgets) as the controller
counts them in each ledger (status.usage): for each rule and limit, what is used
of it, the rolling window and when room frees; the versions that wait for room,
for the ones you may read; and the exemptions, in effect, expired or ignored.
-o json prints status.usage.`,
		Example: `  kardinal budget fixer
  kardinal budget fixer --ledger kardinal-promoter
  kardinal budget fixer -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := buildClient()
			if err != nil {
				return fmt.Errorf("budget: %w", err)
			}
			return budgetFn(contextOf(cmd), cmd.OutOrStdout(), c, args[0], ledger, time.Now())
		},
	}
	cmd.Flags().StringVar(&ledger, "ledger", "", "Only this ledger (an installation's; default: every one)")
	withReference(cmd, budgetReference)
	return withStructuredOutput(cmd)
}

// budgetReference is the detail of budget for its reference page.
const budgetReference = `A budget rule limits the agent's versions a day (versionsPerDay), its
promotions at once (maxConcurrent), its fleet targets an hour
(fleetTargetsPerHour), the failures it leaves in place (maxUnavailable) and a
share of one fleet (fleetShare). A step of the agent's version beyond a limit
waits in Pending, never failed, until the ledger grants it; kardinal why says
so. The ledger is written by one controller of each installation; a missing
ledger means that controller does not count this agent yet (budgets off in
that release, or its writer is not running): kardinal doctor says which. A
person lets one version through with kardinal override <pipeline> <env>
--budget --reason <why>.`

// budgetFn prints agent's budgets and ledgers.
func budgetFn(ctx context.Context, w io.Writer, c sigs_client.Client, agent, only string, now time.Time) error {
	var id v1alpha1.AgentIdentity
	if err := c.Get(ctx, types.NamespacedName{Name: agent}, &id); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("budget: agent %s is not registered (no AgentIdentity %s)", agent, agent)
		}
		return fmt.Errorf("budget: read AgentIdentity %s: %w", agent, err)
	}
	ledgers := make([]string, 0, len(id.Status.Usage))
	for name := range id.Status.Usage {
		if only == "" || name == only {
			ledgers = append(ledgers, name)
		}
	}
	sort.Strings(ledgers)
	if structured() {
		out := map[string]v1alpha1.AgentUsage{}
		for _, l := range ledgers {
			out[l] = id.Status.Usage[l]
		}
		return writeStructured(w, out)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Agent %s (sponsor %s): %d budget rule(s).\n", agent, id.Spec.Sponsor.User, len(id.Spec.Budgets))
	if len(id.Spec.Budgets) == 0 {
		_, _ = fmt.Fprintln(tw, "No budgets: nothing holds its versions.")
		return tw.Flush()
	}
	if len(ledgers) == 0 {
		what := "no installation counts it yet"
		if only != "" {
			what = "ledger " + only + " does not count it yet"
		}
		_, _ = fmt.Fprintf(tw, "No ledger: %s (budgets off there, or its writer is not running; kardinal doctor says which).\n", what)
		return tw.Flush()
	}
	waiting := budgetWaiting(ctx, c, agent)
	for _, l := range ledgers {
		u := id.Status.Usage[l]
		state := "current"
		if u.ObservedGeneration != id.Generation {
			state = "behind the spec: its versions wait until it is evaluated"
		}
		evaluated := "-"
		if u.EvaluatedAt != nil {
			evaluated = u.EvaluatedAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(tw, "\nLedger %s (evaluated %s; %s)\n", l, evaluated, state)
		_, _ = fmt.Fprintln(tw, "  RULE\tLIMIT\tUSED\tWINDOW\tFREES AT\tWAITING\t")
		for _, r := range u.Rules {
			for _, row := range []struct {
				name   string
				count  *v1alpha1.AgentBudgetCount
				window string
			}{{"versionsPerDay", r.Versions, "24h"}, {"maxConcurrent", r.Concurrent, "-"},
				{"fleetTargetsPerHour", r.FleetTargets, "1h"}, {"maxUnavailable", r.Unavailable, "-"}} {
				if row.count == nil {
					continue
				}
				frees := "-"
				if row.count.FreesAt != nil {
					frees = row.count.FreesAt.UTC().Format(time.RFC3339)
				}
				mark := ""
				if row.count.Used >= row.count.Limit {
					mark = "exhausted"
				}
				_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d/%d\t%s\t%s\t%d\t%s\n", r.Name, row.name, row.count.Used, row.count.Limit,
					row.window, frees, r.Waiting, mark)
			}
			if r.FleetShare != "" {
				_, _ = fmt.Fprintf(tw, "  %s\tfleetShare\t%s of a fleet\t-\t-\t%d\t\n", r.Name, r.FleetShare, r.Waiting)
			}
		}
		if len(u.Exemptions) > 0 {
			_, _ = fmt.Fprintln(tw, "  Exemptions:")
			for _, e := range u.Exemptions {
				state := "in effect"
				switch {
				case e.Ignored != "":
					state = "ignored: " + e.Ignored
				case !now.Before(e.ExpiresAt.Time):
					state = "expired"
				}
				_, _ = fmt.Fprintf(tw, "    %s\t%s\tuntil %s\t%s\n", e.Name, exemptionWhere(&id, e.Name),
					e.ExpiresAt.UTC().Format(time.RFC3339), state)
			}
		}
	}
	switch {
	case waiting == nil:
		_, _ = fmt.Fprintln(tw, "\nWaiting: not shown (you may not list PromotionSteps in every namespace).")
	case len(waiting) == 0:
		_, _ = fmt.Fprintln(tw, "\nWaiting: none.")
	default:
		_, _ = fmt.Fprintln(tw, "\nWaiting:")
		for _, s := range waiting {
			since := "-"
			if s.Status.Budget.Since != nil {
				since = s.Status.Budget.Since.UTC().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(tw, "  %s/%s\t%s\t%s\tsince %s\trules %s\n", s.Namespace, s.Spec.PipelineName, s.Spec.Environment,
				s.Spec.BundleName, since, strings.Join(s.Status.Budget.Rules, ", "))
		}
	}
	return tw.Flush()
}

// budgetWaiting are the agent's steps that wait for its budget, oldest
// first; nil when the caller may not list them.
func budgetWaiting(ctx context.Context, c sigs_client.Client, agent string) []v1alpha1.PromotionStep {
	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps); err != nil {
		return nil
	}
	out := []v1alpha1.PromotionStep{}
	for _, s := range steps.Items {
		if s.Status.Budget != nil && s.Status.Budget.Agent == agent && s.Status.Budget.State == "Waiting" {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Status.Budget.Since, out[j].Status.Budget.Since
		if a == nil || b == nil {
			return out[i].Name < out[j].Name
		}
		return a.Before(b)
	})
	return out
}

// exemptionWhere is where exemption name lets the agent through.
func exemptionWhere(id *v1alpha1.AgentIdentity, name string) string {
	for _, e := range id.Spec.BudgetExemptions {
		if e.Name == name {
			where := e.Namespace + "/" + e.Pipeline + " " + e.Environment
			if e.Bundle != "" {
				where += " " + e.Bundle
			}
			return where + ", by " + e.CreatedBy
		}
	}
	return "-"
}
