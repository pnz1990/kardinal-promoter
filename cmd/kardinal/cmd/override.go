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
	"os/user"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newOverrideCmd() *cobra.Command {
	var (
		stage     string
		reason    string
		expiresIn = time.Hour
	)

	cmd := &cobra.Command{
		Use:   "override <pipeline> [<env>] --gate <gate> --reason <text>",
		Short: "Force-pass a PolicyGate with a mandatory audit record",
		Long: `Make --gate pass for the in-progress Bundles of <pipeline> (in <env>, or every
environment), until --expires-in (default 1h). The override is recorded in the
gate's spec.overrides[] under your Kubernetes username, with a GateOverridden
AuditEvent; it applies to the gate instances that exist now, so run it while
the Bundle waits. A single gate instance name is accepted too.`,
		Example: `  kardinal override my-app prod --gate no-weekend-deploys --reason "P0 hotfix, incident 4521"

  # longer window; --stage is the older name of the environment flag
  kardinal override my-app --stage prod --gate change-freeze --reason "approved by CAB" --expires-in 4h`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := envArgs(cmd, args, stage)
			if err != nil {
				return err
			}
			// MarkFlagRequired refuses a missing flag, not an empty one:
			// --reason "" and --gate "" get here.
			if reason == "" {
				return fmt.Errorf("--reason is required for override (audit record)")
			}
			gateName, _ := cmd.Flags().GetString("gate")
			if gateName == "" {
				return missingGateError(pipeline, env)
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("override: %w", err)
			}
			return overrideFn(cmd.OutOrStdout(), c, ns, pipeline, env, gateName, reason, expiresIn)
		},
	}

	addEnvFlag(cmd, &stage, "Environment the override applies to (or give it after the pipeline; default: every environment)")
	cmd.Flags().StringVar(&reason, "reason", "", "Mandatory justification for the override (audit record)")
	cmd.Flags().Var(durationValue{&expiresIn}, "expires-in", "How long the override is active (30m, 4h, 2d)")
	cmd.Flags().String("gate", "", "PolicyGate name to override")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}

// overrideFn is the testable implementation of override.
// gateName is the name of one gate instance, or a gate template name resolved
// to the instances of it that this pipeline's in-progress Bundles have (for
// stage, when set). It appends a PolicyGateOverride entry to each instance's
// spec.overrides slice.
// The policygate reconciler checks for active (non-expired) overrides before
// evaluating CEL, making this Gate-first: no direct status write here.
func overrideFn(
	w interface{ Write([]byte) (int, error) },
	c sigs_client.Client,
	ns, pipeline, stage, gateName, reason string,
	expDuration time.Duration,
) error {
	ctx := context.Background()

	// Say so when the pipeline is wrong, rather than that it has no gate.
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			return pipelineNotFound(ctx, c, ns, pipeline)
		}
		return fmt.Errorf("get pipeline %s: %w", pipeline, err)
	}

	targets, err := overrideTargets(ctx, c, ns, pipeline, stage, gateName)
	if err != nil {
		return err
	}

	// createdBy is the username the API server authenticates this CLI as:
	// the chart's identity policy refuses any other name.
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("override: %w", err)
	}
	createdBy := id.Username

	now := time.Now().UTC()
	expiresAt := metav1.NewTime(now.Add(expDuration))
	createdAt := metav1.NewTime(now)

	override := v1alpha1.PolicyGateOverride{
		Reason:    reason,
		Stage:     stage,
		ExpiresAt: expiresAt,
		CreatedAt: &createdAt,
		CreatedBy: createdBy,
	}

	stageInfo := "all stages"
	if stage != "" {
		stageInfo = "stage=" + stage
	}
	for i := range targets {
		if err := appendOverride(ctx, c, &targets[i], override); err != nil {
			return fmt.Errorf("patch policygate %s: %w", targets[i].Name, err)
		}
		if _, writeErr := fmt.Fprintf(w, "Override applied: gate=%s pipeline=%s %s\n",
			targets[i].Name, pipeline, stageInfo); writeErr != nil {
			return fmt.Errorf("write output: %w", writeErr)
		}
	}
	if _, writeErr := fmt.Fprintf(w,
		"Reason: %s\nExpires: %s (in %s)\nCreated by: %s\n\nThe gate will pass immediately until the override expires.\n",
		reason,
		expiresAt.UTC().Format(time.RFC3339),
		expDuration.Round(time.Minute),
		createdBy,
	); writeErr != nil {
		return fmt.Errorf("write output: %w", writeErr)
	}

	return nil
}

// overrideTargets is lifecycle.OverrideTargets.
func overrideTargets(ctx context.Context, c sigs_client.Client, ns, pipeline, stage, gateName string) (
	[]v1alpha1.PolicyGate, error) {
	return lifecycle.OverrideTargets(ctx, c, ns, pipeline, stage, gateName)
}

// appendOverride is lifecycle.AppendOverride.
func appendOverride(ctx context.Context, c sigs_client.Client, gate *v1alpha1.PolicyGate,
	override v1alpha1.PolicyGateOverride) error {
	return lifecycle.AppendOverride(ctx, c, gate, override)
}

// currentUser returns the current OS user name for audit trail purposes.
// Returns "unknown" if the user cannot be determined.
func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	name := u.Username
	// On some systems Username includes domain prefix (e.g. DOMAIN\user or user@domain)
	if idx := strings.LastIndex(name, "\\"); idx >= 0 {
		name = name[idx+1:]
	}
	if idx := strings.Index(name, "@"); idx >= 0 {
		name = name[:idx]
	}
	return name
}

// ExportedOverrideFn is exported for testing.
var ExportedOverrideFn = overrideFn

// missingGateError is the error of override without --gate: it lists the
// gates holding pipeline's in-progress Bundles back (in env, when set), read
// from the cluster when it can be.
func missingGateError(pipeline, env string) error {
	where := pipeline
	if env != "" {
		where += " in " + env
	}
	c, ns, err := hintClient()
	if err != nil {
		return fmt.Errorf("--gate is required: name the gate to override")
	}
	names, err := notPassingGates(context.Background(), c, ns, pipeline, env)
	if err != nil || len(names) == 0 {
		return fmt.Errorf("--gate is required: no gate of %s is holding a promotion back now", where)
	}
	return fmt.Errorf("which gate? Gates not passing for %s: %s. Pass --gate <name>", where, strings.Join(names, ", "))
}

// notPassingGates lists the gate templates (or instance names, for a gate
// without a template) whose instances for pipeline's in-progress Bundles are
// not ready, in env when set.
func notPassingGates(ctx context.Context, c sigs_client.Client, ns, pipeline, env string) ([]string, error) {
	labels := sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}
	if env != "" {
		labels["kardinal.io/environment"] = env
	}
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates, sigs_client.InNamespace(ns), labels); err != nil {
		return nil, fmt.Errorf("list policygates: %w", err)
	}
	finished := map[string]bool{}
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err == nil {
		for _, b := range bundles.Items {
			switch b.Status.Phase {
			case "Verified", "Failed", "Superseded", "Rejected":
				finished[b.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range gates.Items {
		if g.Status.Ready || finished[g.Labels["kardinal.io/bundle"]] {
			continue
		}
		name := g.Labels["kardinal.io/gate-template"]
		if name == "" {
			name = g.Name
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
