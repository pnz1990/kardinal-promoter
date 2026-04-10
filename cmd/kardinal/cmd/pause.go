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

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <pipeline>",
		Short: "Pause a pipeline, preventing new promotions from starting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("pause: %w", err)
			}
			return pauseFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
}

// pauseFn is the testable implementation of pause.
func pauseFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	var p v1alpha1.Pipeline
	if getErr := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &p); getErr != nil {
		return fmt.Errorf("get pipeline %s: %w", pipeline, getErr)
	}

	patch := sigs_client.MergeFrom(p.DeepCopy())
	p.Spec.Paused = true
	if patchErr := c.Patch(ctx, &p, patch); patchErr != nil {
		return fmt.Errorf("patch pipeline %s paused=true: %w", pipeline, patchErr)
	}

	if _, err := fmt.Fprintf(w, "Pipeline %s paused. No new promotions will start.\n", pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <pipeline>",
		Short: "Resume a paused pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
			return resumeFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
}

// resumeFn is the testable implementation of resume.
func resumeFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	var p v1alpha1.Pipeline
	if getErr := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &p); getErr != nil {
		return fmt.Errorf("get pipeline %s: %w", pipeline, getErr)
	}

	patch := sigs_client.MergeFrom(p.DeepCopy())
	p.Spec.Paused = false
	if patchErr := c.Patch(ctx, &p, patch); patchErr != nil {
		return fmt.Errorf("patch pipeline %s paused=false: %w", pipeline, patchErr)
	}

	if _, err := fmt.Fprintf(w, "Pipeline %s resumed.\n", pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
