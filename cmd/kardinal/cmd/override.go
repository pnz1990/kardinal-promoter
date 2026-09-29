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
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
override is recorded on the instances of that gate the Pipeline's promoting
Bundles have for --stage (every stage when --stage is not set), so run it
while the Bundle waits on the gate. The name of one gate instance is also
accepted.

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
// gateName is a gate template name, resolved to this pipeline's instances of
// it (for stage, when set), or the name of one gate instance. It appends a
// PolicyGateOverride entry to each instance's spec.overrides slice.
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

	// A gate template (for example platform-policies/no-weekend-deploy) is
	// never evaluated itself: each Bundle's Graph stamps an instance of it per
	// environment in the Pipeline namespace, labelled with the template name,
	// and the PolicyGate reconciler evaluates the instances. Record the
	// override on this Pipeline's instances of the named template.
	var instances v1alpha1.PolicyGateList
	selector := sigs_client.MatchingLabels{
		"kardinal.io/pipeline":      pipeline,
		"kardinal.io/gate-template": gateName,
	}
	if stage != "" {
		selector["kardinal.io/environment"] = stage
	}
	if listErr := c.List(ctx, &instances, sigs_client.InNamespace(ns), selector); listErr != nil {
		return fmt.Errorf("list instances of policygate %s: %w", gateName, listErr)
	}
	if len(instances.Items) > 0 {
		for _, inst := range instances.Items {
			if err := overrideFn(w, c, ns, pipeline, stage, inst.Name, reason, expiresIn); err != nil {
				return err
			}
		}
		return nil
	}

	// Otherwise gateName names a gate instance directly.
	var gate v1alpha1.PolicyGate
	if getErr := c.Get(ctx, types.NamespacedName{Name: gateName, Namespace: ns}, &gate); getErr != nil {
		return fmt.Errorf("get policygate %s: no instance of a gate template of that name for pipeline %s "+
			"in namespace %s: %w", gateName, pipeline, ns, getErr)
	}
	if _, isInstance := gate.Labels["kardinal.io/bundle"]; !isInstance {
		return fmt.Errorf("policygate %s/%s is a template, and no Bundle of pipeline %s has an instance of it "+
			"(stage %q); an override applies to the instances a promoting Bundle creates, so run it while "+
			"the Bundle waits on the gate", ns, gateName, pipeline, stage)
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

	// Append to spec.overrides. A merge patch replaces the whole list, so it
	// carries the resourceVersion and is retried on conflict: a concurrent
	// override is re-read, not overwritten.
	appendErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		patch := sigs_client.MergeFromWithOptions(gate.DeepCopy(), sigs_client.MergeFromWithOptimisticLock{})
		gate.Spec.Overrides = append(gate.Spec.Overrides, override)
		err := c.Patch(ctx, &gate, patch)
		if apierrors.IsConflict(err) {
			if getErr := c.Get(ctx, sigs_client.ObjectKeyFromObject(&gate), &gate); getErr != nil {
				return getErr
			}
		}
		return err
	})
	if appendErr != nil {
		return fmt.Errorf("patch policygate %s: %w", gateName, appendErr)
	}

	stageInfo := "all stages"
	if stage != "" {
		stageInfo = "stage=" + stage
	}
	if _, writeErr := fmt.Fprintf(w,
		"Override applied: gate=%s pipeline=%s %s\nReason: %s\nExpires: %s (in %s)\nCreated by: %s\n\nThe gate will pass immediately until the override expires.\n",
		gateName, pipeline, stageInfo,
		reason,
		expiresAt.UTC().Format(time.RFC3339),
		expDuration.Round(time.Minute),
		createdBy,
	); writeErr != nil {
		return fmt.Errorf("write output: %w", writeErr)
	}

	return nil
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
