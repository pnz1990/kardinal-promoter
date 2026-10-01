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
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newOverrideCmd() *cobra.Command {
	var (
		stage     string
		reason    string
		expiresIn string
	)

	cmd := &cobra.Command{
		Use:   "override <pipeline> --stage <environment> --gate <gate-name> --reason <text> [--expires-in <duration>]",
		Short: "Force-pass a PolicyGate with a mandatory audit record (K-09)",
		Long: `Override a PolicyGate for a specific pipeline stage.

The override is time-limited and creates a mandatory audit record in
PolicyGate.spec.overrides[]. The gate passes immediately without evaluating
the CEL expression until the override expires.

All overrides are preserved for audit purposes. Use --expires-in to control
the override window (default: 1h).

--gate takes the gate template name (for example no-weekend-deploy). The
override is recorded on the instances of that gate that the Pipeline's
in-progress Bundles have for --stage (every stage when --stage is not set),
so run it while the Bundle waits on the gate. Instances of Verified, Failed
and Superseded Bundles are left alone, because no promotion waits on them; if
a Failed Bundle resumes, run the override again. The name of one gate
instance, as kubectl get policygates shows it, is also accepted; that instance
alone gets the override.

Example:
  kardinal override my-app --stage prod --gate no-weekend-deploy \
    --reason "P0 hotfix — incident #4521"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return fmt.Errorf("--reason is required for override (audit record)")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("override: %w", err)
			}
			// Parse the gate name from args — the pipeline is args[0]
			// The gate flag is required when --stage is set
			gateName, _ := cmd.Flags().GetString("gate")
			if gateName == "" {
				return fmt.Errorf("--gate is required")
			}
			return overrideFn(cmd.OutOrStdout(), c, ns, args[0], stage, gateName, reason, expiresIn)
		},
	}

	cmd.Flags().StringVar(&stage, "stage", "", "Environment (stage) name the override applies to")
	cmd.Flags().StringVar(&reason, "reason", "", "Mandatory justification for the override (audit record)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "1h", "How long the override is active (Go duration, e.g. 1h, 4h, 30m)")
	cmd.Flags().String("gate", "", "PolicyGate name to override")
	_ = cmd.MarkFlagRequired("reason")
	_ = cmd.MarkFlagRequired("gate")

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
	ns, pipeline, stage, gateName, reason, expiresIn string,
) error {
	ctx := context.Background()

	// Parse expiry duration
	expDuration, err := time.ParseDuration(expiresIn)
	if err != nil || expDuration <= 0 {
		return fmt.Errorf("invalid --expires-in %q: must be a positive Go duration (e.g. 1h, 30m)", expiresIn)
	}

	targets, err := overrideTargets(ctx, c, ns, pipeline, stage, gateName)
	if err != nil {
		return err
	}

	// Determine who is creating the override (best-effort)
	createdBy := currentUser()

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

// overrideTargets returns the gate instances an override of gateName is
// recorded on.
//
// A gate template (for example platform-policies/no-weekend-deploy) is never
// evaluated itself: each Bundle's Graph stamps an instance of it per
// environment in the Pipeline namespace, labelled with the template name, and
// the PolicyGate reconciler evaluates the instances. An instance name is
// usually longer than the 63 characters a label value may have, so a name
// that is an instance is read directly and never used in a label selector
// (E2E-R08). A template name is resolved to the instances of the Bundles
// still in progress. No promotion waits on the instances of a Verified,
// Failed or Superseded Bundle, so they are left alone.
func overrideTargets(ctx context.Context, c sigs_client.Client, ns, pipeline, stage, gateName string) (
	[]v1alpha1.PolicyGate, error) {
	var gate v1alpha1.PolicyGate
	getErr := c.Get(ctx, types.NamespacedName{Name: gateName, Namespace: ns}, &gate)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("get policygate %s: %w", gateName, getErr)
	}
	if getErr == nil {
		if _, isInstance := gate.Labels["kardinal.io/bundle"]; isInstance {
			if p := gate.Labels["kardinal.io/pipeline"]; p != "" && p != pipeline {
				return nil, fmt.Errorf("policygate %s/%s is an instance of pipeline %s, not %s",
					ns, gateName, p, pipeline)
			}
			if env := gate.Labels["kardinal.io/environment"]; stage != "" && env != "" && env != stage {
				return nil, fmt.Errorf("policygate %s/%s is the instance for stage %s, not %s",
					ns, gateName, env, stage)
			}
			return []v1alpha1.PolicyGate{gate}, nil
		}
	}

	// A template name is a valid label value (pkg/graph/validate.go checks
	// it); anything longer cannot label an instance.
	if len(validation.IsValidLabelValue(gateName)) > 0 {
		return nil, fmt.Errorf("get policygate %s: no gate instance of that name in namespace %s: %w",
			gateName, ns, getErr)
	}
	var instances v1alpha1.PolicyGateList
	selector := sigs_client.MatchingLabels{
		"kardinal.io/pipeline":      pipeline,
		"kardinal.io/gate-template": gateName,
	}
	if stage != "" {
		selector["kardinal.io/environment"] = stage
	}
	if err := c.List(ctx, &instances, sigs_client.InNamespace(ns), selector); err != nil {
		return nil, fmt.Errorf("list instances of policygate %s: %w", gateName, err)
	}
	var bundles v1alpha1.BundleList
	if len(instances.Items) > 0 {
		if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
	}
	inProgress := make(map[string]bool, len(bundles.Items))
	for _, b := range bundles.Items {
		switch b.Status.Phase {
		case "Verified", "Failed", "Superseded":
		default:
			inProgress[b.Name] = true
		}
	}
	var targets []v1alpha1.PolicyGate
	for _, inst := range instances.Items {
		if inProgress[inst.Labels["kardinal.io/bundle"]] {
			targets = append(targets, inst)
		}
	}
	if len(targets) > 0 {
		sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
		return targets, nil
	}

	// An org gate's template is in a policy namespace, so the Get above does
	// not find it; its instances are still here.
	if getErr != nil && len(instances.Items) == 0 {
		return nil, fmt.Errorf("get policygate %s: no gate instance or template of that name for pipeline %s "+
			"in namespace %s: %w", gateName, pipeline, ns, getErr)
	}
	return nil, fmt.Errorf("no in-progress Bundle of pipeline %s has an instance of gate %s in namespace %s "+
		"(stage %q; %d instance(s) of finished Bundles); an override applies to the instances a promoting "+
		"Bundle creates, so run it while the Bundle waits on the gate",
		pipeline, gateName, ns, stage, len(instances.Items))
}

// appendOverride appends override to gate's spec.overrides. A merge patch
// replaces the whole list, so it carries the resourceVersion and is retried
// on conflict: a concurrent override is re-read, not overwritten.
func appendOverride(ctx context.Context, c sigs_client.Client, gate *v1alpha1.PolicyGate,
	override v1alpha1.PolicyGateOverride) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		patch := sigs_client.MergeFromWithOptions(gate.DeepCopy(), sigs_client.MergeFromWithOptimisticLock{})
		gate.Spec.Overrides = append(gate.Spec.Overrides, override)
		err := c.Patch(ctx, gate, patch)
		if apierrors.IsConflict(err) {
			if getErr := c.Get(ctx, sigs_client.ObjectKeyFromObject(gate), gate); getErr != nil {
				return getErr
			}
		}
		return err
	})
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
