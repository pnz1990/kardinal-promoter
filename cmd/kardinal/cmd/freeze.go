// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

// freezeFlags are the scope flags of freeze and unfreeze.
type freezeFlags struct {
	agents, everyone, cluster bool
}

// add registers --agents, --everyone and --cluster.
func (f *freezeFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.agents, "agents", false, "Hold the versions agents created (an agent freeze)")
	cmd.Flags().BoolVar(&f.everyone, "everyone", false, "Hold every version (stop-the-line)")
	cmd.Flags().BoolVar(&f.cluster, "cluster", false, "Every namespace (the default; -n limits the freeze to one namespace)")
}

// scope returns the actors and the namespace ("" for every namespace) the
// flags name; with none, ok is false.
func (f freezeFlags) scope(cmd *cobra.Command, ns string) (actors, namespace string, ok bool, err error) {
	switch {
	case f.agents && f.everyone:
		return "", "", false, fmt.Errorf("--agents and --everyone: pick one")
	case f.agents:
		actors = v1alpha1.FreezeAgents
	case f.everyone:
		actors = v1alpha1.FreezeEveryone
	default:
		return "", "", false, nil
	}
	nsSet := cmd.Flags().Changed("namespace")
	if f.cluster && nsSet {
		return "", "", false, fmt.Errorf("--cluster and -n: pick one")
	}
	if nsSet {
		namespace = ns
	}
	return actors, namespace, true, nil
}

func newFreezeCmd() *cobra.Command {
	var (
		flags  freezeFlags
		reason string
		dur    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "freeze [--agents | --everyone] [--cluster | -n <ns>] --reason <text> [--for <duration>]",
		Short: "Freeze agents, or stop the line for everyone; with no flags, list the freezes",
		Long: `Hold promotions until the freeze is lifted. --agents holds the versions agents
created (their actor stamp says kind agent), --everyone holds every version
(stop-the-line). The freeze covers every namespace, or with -n one namespace.
A held step waits before it starts or before its next git step; a step
waiting for its PR merge or running its health check finishes. --for ends the
freeze by itself; without it the freeze lasts until kardinal unfreeze.
Let one pipeline through with kardinal override <pipeline> <env> --freeze.

With no flags it lists the freezes.`,
		Example: `  kardinal freeze --agents --reason "INC-4211: agents paused while we investigate"
  kardinal freeze --everyone -n payments --reason "payments incident" --for 2h
  kardinal freeze
  kardinal unfreeze kardinal-freeze-agents`,
		Annotations: map[string]string{ReferenceAnnotation: freezeReference},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("freeze: %w", err)
			}
			actors, namespace, ok, err := flags.scope(cmd, ns)
			if err != nil {
				return err
			}
			if !ok {
				return listFreezesFn(cmd.OutOrStdout(), c, time.Now())
			}
			if reason == "" {
				return fmt.Errorf("--reason is required: every held step shows it")
			}
			where := "every namespace"
			if namespace != "" {
				where = "namespace " + namespace
			}
			if err := confirm(cmd, fmt.Sprintf("Freeze %s in %s? Their promotions hold until you unfreeze.", actors, where)); err != nil {
				return err
			}
			return freezeFn(cmd.OutOrStdout(), c, actors, namespace, reason, dur, time.Now())
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&reason, "reason", "", "Why (shown on every held step)")
	cmd.Flags().Var(durationValue{&dur}, "for", "End the freeze after this long (30m, 4h, 2d); default: until kardinal unfreeze")
	addYesFlag(cmd)
	return cmd
}

// freezeFn is the testable implementation of freeze (lifecycle.Freeze, the
// implementation the UI shares).
func freezeFn(w io.Writer, c sigs_client.Client, actors, namespace, reason string, d time.Duration, now time.Time) error {
	ctx := context.Background()
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("freeze: %w", err)
	}
	cw, created, err := lifecycle.Freeze(ctx, c, lifecycle.FreezeRequest{Actors: actors, Namespace: namespace,
		Reason: reason, For: d, CreatedBy: id.Username, Now: now})
	if err != nil {
		return fmt.Errorf("freeze: %w", err)
	}
	verb := "Frozen"
	if !created {
		verb = "Freeze updated"
	}
	until := "until kardinal unfreeze " + cw.Name
	if !cw.Spec.End.IsZero() {
		until = "until " + cw.Spec.End.UTC().Format(time.RFC3339)
	}
	_, err = fmt.Fprintf(w, "%s: %s (%s), %s.\nHeld steps wait before they start or before their next git step.\n",
		verb, cw.Name, changewindow.FreezeScope(cw.Spec.Freeze), until)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// listFreezesFn lists the freezes and whether each is active at now.
func listFreezesFn(w io.Writer, c sigs_client.Client, now time.Time) error {
	freezes, err := lifecycle.ListFreezes(context.Background(), c)
	if err != nil {
		return fmt.Errorf("freeze: %w", err)
	}
	if len(freezes) == 0 {
		_, err := fmt.Fprintln(w, "No freezes.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tHOLDS\tACTIVE\tSINCE\tUNTIL\tBY\tEXEMPTIONS\tREASON")
	for _, cw := range freezes {
		until := "unfreeze"
		if !cw.Spec.End.IsZero() {
			until = cw.Spec.End.UTC().Format(time.RFC3339)
		}
		live := 0
		for _, e := range cw.Spec.Freeze.Exemptions {
			if now.Before(e.ExpiresAt.Time) {
				live++
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\t%s\t%d\t%s\n", cw.Name, changewindow.FreezeScope(cw.Spec.Freeze),
			changewindow.Evaluate(cw.Spec, now).Active, cw.Spec.Start.UTC().Format(time.RFC3339), until,
			dash(cw.Spec.Freeze.CreatedBy), live, dash(cw.Spec.Reason))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func newUnfreezeCmd() *cobra.Command {
	var flags freezeFlags
	cmd := &cobra.Command{
		Use:   "unfreeze [<name> | --agents | --everyone] [--cluster | -n <ns>]",
		Short: "Lift a freeze",
		Long: `Delete a freeze: by name (kardinal freeze lists them), or by the scope it was
created with. Held steps go on within a minute. A ChangeWindow that is not a
freeze is never deleted.`,
		Example: `  kardinal unfreeze kardinal-freeze-agents
  kardinal unfreeze --everyone -n payments`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("unfreeze: %w", err)
			}
			actors, namespace, ok, err := flags.scope(cmd, ns)
			if err != nil {
				return err
			}
			switch {
			case ok && len(args) == 1:
				return fmt.Errorf("give a freeze name or a scope (--agents, --everyone), not both")
			case ok:
				args = []string{lifecycle.FreezeName(actors, namespace)}
			case len(args) == 0:
				return fmt.Errorf("which freeze? Give its name (kardinal freeze lists them) or --agents / --everyone")
			}
			return unfreezeFn(cmd.OutOrStdout(), c, args[0])
		},
	}
	flags.add(cmd)
	return cmd
}

// unfreezeFn is the testable implementation of unfreeze.
func unfreezeFn(w io.Writer, c sigs_client.Client, name string) error {
	if err := lifecycle.Unfreeze(context.Background(), c, name); err != nil {
		return fmt.Errorf("unfreeze: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Freeze %s lifted. Held steps go on within a minute.\n", name); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// overrideFreezeFn is override --freeze: it adds an exemption for pipeline
// (in env, of bundle, when set) to every active freeze of its namespace.
func overrideFreezeFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, bundle, reason string,
	d time.Duration, now time.Time) error {
	ctx := context.Background()
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("override: %w", err)
	}
	names, err := lifecycle.ExemptFromFreezes(ctx, c, lifecycle.ExemptRequest{Namespace: ns, Pipeline: pipeline,
		Environment: env, Bundle: bundle, Reason: reason, ExpiresIn: d, CreatedBy: id.Username, Now: now})
	if err != nil {
		return fmt.Errorf("override: %w", err)
	}
	where := "every environment"
	if env != "" {
		where = env
	}
	if d == 0 {
		d = lifecycle.DefaultExemption
	}
	_, err = fmt.Fprintf(w, "Exemption added to %s: %s in %s goes on through the freeze until %s.\nReason: %s\nCreated by: %s\n",
		strings.Join(names, ", "), pipeline, where, now.Add(d).UTC().Truncate(time.Second).Format(time.RFC3339),
		reason, id.Username)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// freezeReference is the detail of freeze for its reference page.
const freezeReference = `A freeze is a cluster-scoped ChangeWindow of type blackout with spec.freeze,
named kardinal-freeze-<agents|everyone>[-<namespace>]. While it is active,
the controller holds every PromotionStep it selects, in every Graph,
including those built before the freeze: the step does not start, or does
not run its next git step, and its message names the freeze, who froze and
why. Steps waiting for a PR merge or a health check finish. A repeated
freeze of the same scope updates its reason and end. Freezing needs create
and update on changewindows (cluster-scoped); the chart's freezes admission
policy records you as spec.freeze.createdBy. kardinal override <pipeline>
<env> --freeze adds a time-limited exemption to the active freezes, recorded
with your name and audited (FreezeExempted) when a step goes on under it.`
