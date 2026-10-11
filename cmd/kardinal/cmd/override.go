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
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newOverrideCmd() *cobra.Command {
	var (
		stage     string
		reason    string
		expiresIn string
	)

	cmd := &cobra.Command{
		Use:   "override <pipeline> --stage <environment> --gate <gate-name> --reason <text> [--expires-in <duration>]",
		Short: "Force-pass a PolicyGate with a mandatory audit record",
		Long: `Override a PolicyGate for a specific pipeline stage.

The override is time-limited and creates a mandatory audit record in
PolicyGate.spec.overrides[]. The gate passes immediately without evaluating
the CEL expression until the override expires.

The record names you by your Kubernetes username, read from the API server
with a SelfSubjectReview (as kubectl auth whoami does); the chart's
ValidatingAdmissionPolicy refuses an override in anyone else's name. The
controller writes a GateOverridden AuditEvent for each override.

Expired overrides stay in spec.overrides[] as an audit record for as long
as the Bundle exists. Deleting a Bundle deletes its gate instances and their
overrides, and the Pipeline's historyLimit cleanup deletes old finished
Bundles (50 by default). Use --expires-in to control the override window
(default: 1h).

--gate takes the gate template name (for example no-weekend-deploys). The
override is recorded on the instances of that gate that the Pipeline's
in-progress Bundles have for --stage (every stage when --stage is not set),
so run it while the Bundle waits on the gate. Instances of Verified, Failed,
Superseded and Rejected Bundles are left alone, because no promotion waits on them; if
a Failed Bundle resumes, run the override again. The name of one gate
instance, as kubectl get policygates shows it, is also accepted; that instance
alone gets the override.

Example:
  kardinal override my-app --stage prod --gate no-weekend-deploys \
    --reason "P0 hotfix — incident #4521"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// MarkFlagRequired refuses a missing flag, not an empty one:
			// --reason "" and --gate "" get here.
			if reason == "" {
				return fmt.Errorf("--reason is required for override (audit record)")
			}
			gateName, _ := cmd.Flags().GetString("gate")
			if gateName == "" {
				return fmt.Errorf("--gate is required")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("override: %w", err)
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

	// Say so when the pipeline is wrong, rather than that it has no gate.
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pipeline %q not found in namespace %q", pipeline, ns)
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
