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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// verifyOptions are the flags of kardinal verify.
type verifyOptions struct {
	env, check, version      string
	pass, fail, inconclusive bool
	summary, evidence, name  string
	findings                 []string
	revoke                   bool
}

func newVerifyCmd() *cobra.Command {
	var o verifyOptions
	cmd := &cobra.Command{
		Use:   "verify <pipeline> <env> --check <check> (--pass | --fail | --inconclusive)",
		Short: "Record a verdict on the version running in an environment (verify gates)",
		Long: `Record your verdict, in your Kubernetes name, on the version that runs in <env>
of <pipeline>: it passed, failed or was inconclusive for --check. Give a Bundle
instead with kardinal verify <bundle> --env <env>. A verify gate downstream
counts the verdicts of the checks it waits for (docs/agentic/verify-with-agents.md).
An agent's verdict counts only on a gate that lists it in verify.allowedAgents,
and never on work of its own party (its sponsor's, or its own).`,
		Example: `  # an exploratory test agent in CI reports
  kardinal verify checkout uat --check exploratory-qa --pass \
    --summary "checkout, refunds and coupons work" --evidence https://ci.example.com/runs/88123

  kardinal verify checkout uat --check exploratory-qa --fail \
    --summary "coupon SAVE10 returns 500" --finding "high:coupon SAVE10 returns 500"

  # take your latest verdict back
  kardinal verify checkout uat --check exploratory-qa --revoke

  kardinal verify list checkout --env uat`,
		Args: pipelineArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			bundle, env, err := verifyTarget(ctx, c, ns, cmd, args, o.env)
			if err != nil {
				return err
			}
			o.env = env
			if err := checkVerifyVersion(ctx, c, ns, bundle, env, o.version); err != nil {
				return err
			}
			return verifyFn(ctx, cmd.OutOrStdout(), c, ns, bundle, o, time.Now())
		},
	}
	addEnvFlag(cmd, &o.env, "Environment verified: where the version runs (or give it after the pipeline)")
	cmd.Flags().StringVar(&o.check, "check", "", "The check verified (a verify gate's verify.check)")
	cmd.Flags().BoolVar(&o.pass, "pass", false, "The version passed the check")
	cmd.Flags().BoolVar(&o.fail, "fail", false, "The version failed the check (needs --summary)")
	cmd.Flags().BoolVar(&o.inconclusive, "inconclusive", false, "The check could not decide (needs --summary)")
	cmd.Flags().StringVar(&o.summary, "summary", "", "What you found, in a sentence or two")
	cmd.Flags().StringVar(&o.evidence, "evidence", "", "A link to the evidence (a CI run, a report)")
	cmd.Flags().StringArrayVar(&o.findings, "finding", nil, "A finding, as severity:title (info, low, medium, high, critical); repeatable")
	cmd.Flags().BoolVar(&o.revoke, "revoke", false, "Delete your latest verdict for the version, environment and check")
	cmd.Flags().StringVar(&o.name, "name", "", "With --revoke: the Verdict to delete")
	cmd.Flags().StringVar(&o.version, "version", "", "Record only if this is the version that runs in the environment")
	cmd.AddCommand(newVerifyListCmd())
	withReference(cmd, verifyReference)
	return cmd
}

// verifyTarget is the Bundle and environment of kardinal verify:
// <bundle> --env <env>, or <pipeline> <env>, the Bundle that runs there.
func verifyTarget(ctx context.Context, c sigs_client.Client, ns string, cmd *cobra.Command, args []string, flagEnv string) (string, string, error) {
	// The default pipeline filled in first: one argument left is a Bundle.
	args, err := withDefaultPipeline(args, flagEnv)
	if err != nil {
		return "", "", err
	}
	first, env, err := envArgs(cmd, args, flagEnv)
	if err != nil {
		return "", "", err
	}
	if len(args) == 1 {
		var b v1alpha1.Bundle
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: first}, &b)
		if err == nil {
			if env == "" {
				return "", "", fmt.Errorf("which environment? Give it with --env: kardinal verify %s --env <env>", first)
			}
			return first, env, nil
		}
		if !apierrors.IsNotFound(err) {
			return "", "", fmt.Errorf("verify: get bundle %s: %w", first, err)
		}
	}
	if env == "" {
		return "", "", fmt.Errorf("which environment? Give it after the pipeline: kardinal verify %s <env>", first)
	}
	bundle, err := runningBundle(ctx, c, ns, first, env)
	return bundle, env, err
}

// runningBundle is the Bundle that runs in env of pipeline, the one a
// verdict is about.
func runningBundle(ctx context.Context, c sigs_client.Client, ns, pipeline, env string) (string, error) {
	p, err := requirePipeline(ctx, c, ns, pipeline)
	if err != nil {
		return "", err
	}
	for _, e := range p.Status.Environments {
		if e.Name == env && e.Bundle != "" {
			return e.Bundle, nil
		}
	}
	return "", fmt.Errorf("verify: no version runs in %s of %s yet; name one: kardinal verify <bundle> --env %s", env, pipeline, env)
}

// checkVerifyVersion refuses to record when version is set and is not the
// version bundle ships.
func checkVerifyVersion(ctx context.Context, c sigs_client.Client, ns, bundle, env, version string) error {
	if version == "" {
		return nil
	}
	got, err := approvalVersion(ctx, c, ns, bundle)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("verify: %s runs %s (%s) in %s, not %s: nothing recorded", bundle, versionOr(got, bundle), bundle, env, version)
	}
	return nil
}

// verdictResult is the result the flags give, or an error.
func (o verifyOptions) verdictResult() (string, error) {
	var set []string
	if o.pass {
		set = append(set, v1alpha1.VerdictPass)
	}
	if o.fail {
		set = append(set, v1alpha1.VerdictFail)
	}
	if o.inconclusive {
		set = append(set, v1alpha1.VerdictInconclusive)
	}
	if len(set) != 1 {
		return "", fmt.Errorf("verify: give exactly one of --pass, --fail and --inconclusive")
	}
	return set[0], nil
}

// parseFindings reads --finding values (severity:title).
func parseFindings(in []string) ([]v1alpha1.VerdictFinding, error) {
	var out []v1alpha1.VerdictFinding
	for _, f := range in {
		sev, title, ok := strings.Cut(f, ":")
		sev = strings.ToLower(strings.TrimSpace(sev))
		switch {
		case !ok || strings.TrimSpace(title) == "":
			return nil, fmt.Errorf("verify: --finding %q: give severity:title", f)
		case sev != "info" && sev != "low" && sev != "medium" && sev != "high" && sev != "critical":
			return nil, fmt.Errorf("verify: --finding %q: the severity is info, low, medium, high or critical", f)
		}
		out = append(out, v1alpha1.VerdictFinding{Severity: sev, Title: strings.TrimSpace(title)})
	}
	return out, nil
}

// verifyFn is the testable implementation of kardinal verify.
func verifyFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, bundleName string, o verifyOptions, now time.Time) error {
	if o.check == "" {
		return fmt.Errorf("verify: which check? Give it with --check (the verify gate's verify.check)")
	}
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleName}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("verify: bundle %s not found in namespace %s", bundleName, ns)
		}
		return fmt.Errorf("verify: get bundle %s: %w", bundleName, err)
	}
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	req := lifecycle.VerdictRequest{Namespace: ns, Bundle: bundleName, Environment: o.env, Check: o.check,
		By: id.Username, Groups: id.Groups, Revoke: o.revoke, Name: o.name, Now: now,
		Summary: o.summary, EvidenceURL: o.evidence}
	if o.revoke {
		v, err := lifecycle.RecordVerdict(ctx, c, req)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		return writef(w, "Revoked: %s no longer finds %s %s in %s for %s (Verdict %s deleted)\n",
			id.Username, bundleName, v.Spec.Result, o.env, o.check, v.Name)
	}
	if req.Result, err = o.verdictResult(); err != nil {
		return err
	}
	if req.Findings, err = parseFindings(o.findings); err != nil {
		return err
	}
	stamp, err := stampOf(ctx, c, id, actor.Where{Namespace: ns, Pipeline: b.Spec.Pipeline, Environment: o.env})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	req.Actor = stamp
	v, err := lifecycle.RecordVerdict(ctx, c, req)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	verifier := actor.Describe(actor.OrUnknown(stamp, id.Username))
	if err := writef(w, "Recorded: %s finds %s %s in %s for %s (Verdict %s)\n", verifier, bundleName, v.Spec.Result,
		o.env, o.check, v.Name); err != nil {
		return err
	}
	return verdictStanding(ctx, w, c, &b, o, actor.OrUnknown(stamp, id.Username), id.Groups)
}

// verdictStanding tells the caller whether the gates that wait for the
// check count the verdict, from the rules the gates count with.
func verdictStanding(ctx context.Context, w io.Writer, c sigs_client.Client, b *v1alpha1.Bundle, o verifyOptions,
	verifier v1alpha1.Actor, groups []string) error {
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates, sigs_client.InNamespace(b.Namespace), sigs_client.MatchingLabels{"kardinal.io/bundle": b.Name}); err != nil {
		return nil // best effort: the verdict is recorded
	}
	author := actor.OrUnknown(b.Spec.Actor, b.Annotations[lifecycle.AnnotationCreatedBy])
	waiting := 0
	for i := range gates.Items {
		g := &gates.Items[i]
		p := g.Spec.Verify
		if p == nil || p.Check != o.check || p.Environment != o.env {
			continue
		}
		waiting++
		s := lifecycle.VerdictStandingOf(g, author, verifier, groups)
		if !s.Counts {
			if err := writef(w, "  not counted by gate %s: %s\n", verifyGateName(g), s.Reason); err != nil {
				return err
			}
		} else if err := writef(w, "  counted by gate %s (%s)\n", verifyGateName(g), g.Labels["kardinal.io/environment"]); err != nil {
			return err
		}
	}
	if waiting == 0 {
		return writef(w, "  no gate of %s waits for check %s in %s yet\n", b.Name, o.check, o.env)
	}
	return nil
}

// verifyGateName is the template name of a gate instance.
func verifyGateName(g *v1alpha1.PolicyGate) string {
	if n := g.Labels["kardinal.io/gate-name"]; n != "" {
		return n
	}
	return g.Name
}

func newVerifyListCmd() *cobra.Command {
	var env, check string
	cmd := &cobra.Command{
		Use:   "list <pipeline|bundle>",
		Short: "List the verdicts on a version, and how its verify gates count them",
		Example: `  # the verdicts on the version that runs in uat
  kardinal verify list checkout --env uat

  kardinal verify list checkout-1-5-0 --check exploratory-qa`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("verify list: %w", err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			bundle := args[0]
			var b v1alpha1.Bundle
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundle}, &b); apierrors.IsNotFound(err) {
				p, err := requirePipeline(ctx, c, ns, args[0])
				if err != nil {
					return err
				}
				bundle = ""
				for _, e := range p.Status.Environments {
					if env == "" || e.Name == env {
						if e.Bundle != "" {
							bundle = e.Bundle
						}
					}
				}
				if bundle == "" {
					return fmt.Errorf("verify list: no version runs in %s yet", args[0])
				}
			} else if err != nil {
				return fmt.Errorf("verify list: get bundle %s: %w", bundle, err)
			}
			return verifyListFn(ctx, cmd.OutOrStdout(), c, ns, bundle, env, check)
		},
	}
	addEnvFlag(cmd, &env, "Only verdicts for this environment")
	cmd.Flags().StringVar(&check, "check", "", "Only verdicts for this check")
	return cmd
}

// verdictsCounted is the Verdicts of bundle (of env and check, when set),
// and by Verdict name whether a verify gate counted it, from the gates'
// status.verdicts: "yes (<gate>)", "no: <reason>", or absent.
func verdictsCounted(ctx context.Context, c sigs_client.Client, ns, bundle, env, check string) ([]v1alpha1.Verdict, map[string]string, error) {
	list, err := lifecycle.VerdictsOf(ctx, c, ns, bundle, env, check)
	if err != nil {
		return nil, nil, fmt.Errorf("verify list: %w", err)
	}
	counted := map[string]string{}
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"kardinal.io/bundle": bundle}); err == nil {
		for _, g := range gates.Items {
			for _, rec := range g.Status.Verdicts {
				switch {
				case rec.Counted:
					counted[rec.Name] = "yes (" + verifyGateName(&g) + ")"
				case counted[rec.Name] == "":
					counted[rec.Name] = "no: " + rec.Reason
				}
			}
		}
	}
	return list, counted, nil
}

// verifyListFn prints the Verdicts of bundle and, for each, whether a verify
// gate counted it (from the gates' status.verdicts).
func verifyListFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, bundle, env, check string) error {
	list, counted, err := verdictsCounted(ctx, c, ns, bundle, env, check)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return writef(w, "No verdicts on %s.\n", bundle)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tENV\tCHECK\tVERIFIER\tRESULT\tCOUNTED\tAGE\tEVIDENCE")
	for _, v := range list {
		cnt := counted[v.Name]
		if cnt == "" {
			cnt = "—"
		}
		ev := v.Spec.EvidenceURL
		if ev == "" {
			ev = "—"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Spec.Environment, v.Spec.Check,
			actor.Describe(actor.OrUnknown(v.Spec.Actor, v.Spec.By)), v.Spec.Result, cnt,
			time.Since(v.CreationTimestamp.Time).Round(time.Second), ev)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// verifyReference is the detail of the command for its reference page.
const verifyReference = `Creates a Verdict for the Bundle, environment and check in your name: your
Kubernetes username and groups (a SelfSubjectReview) and your actor stamp
(docs/actors.md), owned by the Bundle. The chart's ValidatingAdmissionPolicy
refuses a Verdict in anyone else's name. A Verdict is never changed: a new one
from the same verifier replaces the old one as that verifier's vote.

A PolicyGate with spec.verify downstream of the environment counts the
Verdicts for its check: it is ready when its expression is true, at least
verify.required votes pass and no more than verify.maxFailures fail. An
agent's verdict counts only when the gate lists the agent in
verify.allowedAgents, and never on its own party's work.

  --fail, --inconclusive   need --summary.
  --revoke                 deletes your latest Verdict (or --name).

Asserted fields come from KARDINAL_ACTOR_MODEL, KARDINAL_ACTOR_SESSION and
KARDINAL_ACTOR_CLIENT. See docs/agentic/verify-with-agents.md.`
