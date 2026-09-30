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

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <pipeline>",
		Short: "Pause a pipeline: no new promotion steps start, in-flight ones hold at the next safe point",
		Long: `Pause a pipeline.

Sets spec.paused on the Pipeline and creates the freeze PolicyGate
freeze-<pipeline>. While the pipeline is paused, no PromotionStep leaves
Pending and a step that is still preparing its change (clone, update,
commit, open PR) holds before its next step. A step that is waiting for a
PR merge or running its health check finishes, so a merged change is never
left unverified. Resume with: kardinal resume <pipeline>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("pause: %w", err)
			}
			return pauseFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
}

// pauseFn is the testable implementation of pause. It uses lifecycle.Pause,
// the implementation the UI shares.
func pauseFn(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	if err := lifecycle.Pause(context.Background(), c, ns, pipeline); err != nil {
		return fmt.Errorf("pause %s: %w", pipeline, err)
	}
	if _, err := fmt.Fprintf(w,
		"Pipeline %s paused. No new promotions will start; in-flight steps hold at the next safe point.\n",
		pipeline); err != nil {
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

// resumeFn is the testable implementation of resume. It uses
// lifecycle.Resume, the implementation the UI shares.
func resumeFn(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	if err := lifecycle.Resume(context.Background(), c, ns, pipeline); err != nil {
		return fmt.Errorf("resume %s: %w", pipeline, err)
	}
	if _, err := fmt.Fprintf(w, "Pipeline %s resumed.\n", pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
