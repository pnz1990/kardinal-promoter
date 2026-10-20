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

	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newPauseCmd() *cobra.Command {
	var env, reason string
	cmd := &cobra.Command{
		Use:   "pause <pipeline>",
		Short: "Pause a pipeline, or one fleet: no new promotion steps start",
		Long: `Hold <pipeline>: no step starts, and a step still preparing its change holds
before its next step; a step waiting for its PR merge or health check finishes.
With --env <fleet>, pause one fleet: no further target starts, for any version,
and the targets in flight finish. Resume with kardinal resume (--env <fleet>).`,
		Example: `  kardinal pause my-app --reason "INC-4211: hold releases while we investigate"
  kardinal resume my-app
  kardinal pause my-app --env prod      # prod is a fleet: stop starting targets
  kardinal resume my-app --env prod`,
		Annotations: map[string]string{ReferenceAnnotation: pauseReference},
		Args:        pipelineArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, err := pipelineOf(args)
			if err != nil {
				return fmt.Errorf("pause: %w", err)
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("pause: %w", err)
			}
			if env != "" {
				return fleetPauseFn(cmd.OutOrStdout(), c, ns, pipeline, env, true, reason)
			}
			return pauseFn(cmd.OutOrStdout(), c, ns, pipeline, reason)
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "pause only this fleet environment: no further target starts")
	cmd.Flags().StringVar(&reason, "reason", "", "why, recorded with your name in the PipelinePaused AuditEvent")
	return cmd
}

// pauseFn is the testable implementation of pause. It uses lifecycle.Pause,
// the implementation the UI shares.
func pauseFn(w io.Writer, c sigs_client.Client, ns, pipeline, reason string) error {
	who, err := changeBy(context.Background(), c, reason, actor.Where{Namespace: ns, Pipeline: pipeline})
	if err != nil {
		return fmt.Errorf("pause %s: %w", pipeline, err)
	}
	if err := lifecycle.Pause(context.Background(), c, ns, pipeline, who); err != nil {
		return fmt.Errorf("pause %s: %w", pipeline, err)
	}
	if _, err := fmt.Fprintf(w,
		"Pipeline %s paused. No new promotions will start; in-flight steps hold at the next safe point.\n",
		pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// fleetPauseFn is the testable implementation of pause and resume --env
// <fleet>. It uses lifecycle.SetFleetPaused, the implementation the UI
// shares.
func fleetPauseFn(w io.Writer, c sigs_client.Client, ns, pipeline, fleet string, paused bool, reason string) error {
	verb := "resume"
	if paused {
		verb = "pause"
	}
	who, err := changeBy(context.Background(), c, reason, actor.Where{Namespace: ns, Pipeline: pipeline, Environment: fleet})
	if err != nil {
		return fmt.Errorf("%s %s --env %s: %w", verb, pipeline, fleet, err)
	}
	if err := lifecycle.SetFleetPaused(context.Background(), c, ns, pipeline, fleet, paused, who); err != nil {
		return fmt.Errorf("%s %s --env %s: %w", verb, pipeline, fleet, err)
	}
	msg := fmt.Sprintf("Fleet %s of pipeline %s resumed. Its targets start again as places free up.\n", fleet, pipeline)
	if paused {
		msg = fmt.Sprintf("Fleet %s of pipeline %s paused. No further target starts; the targets in flight finish.\n"+
			"Resume with: kardinal resume %s --env %s\n", fleet, pipeline, pipeline, fleet)
	}
	if _, err := io.WriteString(w, msg); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func newResumeCmd() *cobra.Command {
	var env, reason string
	cmd := &cobra.Command{
		Use:   "resume <pipeline>",
		Short: "Resume a paused pipeline, or a paused fleet",
		Long: `Resume <pipeline> after kardinal pause: its held steps continue and new
promotions start again. With --env <fleet>, resume a fleet paused with
kardinal pause --env <fleet>.`,
		Example: `  kardinal resume my-app
  kardinal resume my-app --env prod`,
		Args: pipelineArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, err := pipelineOf(args)
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
			if env != "" {
				return fleetPauseFn(cmd.OutOrStdout(), c, ns, pipeline, env, false, reason)
			}
			return resumeFn(cmd.OutOrStdout(), c, ns, pipeline, reason)
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "resume only this fleet environment")
	cmd.Flags().StringVar(&reason, "reason", "", "why, recorded with your name in the PipelineResumed AuditEvent")
	return cmd
}

// resumeFn is the testable implementation of resume. It uses
// lifecycle.Resume, the implementation the UI shares.
func resumeFn(w io.Writer, c sigs_client.Client, ns, pipeline, reason string) error {
	who, err := changeBy(context.Background(), c, reason, actor.Where{Namespace: ns, Pipeline: pipeline})
	if err != nil {
		return fmt.Errorf("resume %s: %w", pipeline, err)
	}
	if err := lifecycle.Resume(context.Background(), c, ns, pipeline, who); err != nil {
		return fmt.Errorf("resume %s: %w", pipeline, err)
	}
	if _, err := fmt.Fprintf(w, "Pipeline %s resumed.\n", pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// pauseReference is the detail of the command for its reference page.
const pauseReference = `Sets spec.paused on the Pipeline and creates the freeze PolicyGate
freeze-<pipeline>. While the pipeline is paused, no PromotionStep leaves
Pending and a step that is still preparing its change (clone, update,
commit, open PR) holds before its next step. A step that is waiting for a
PR merge or running its health check finishes, so a merged change is never
left unverified. Resume with: kardinal resume <pipeline>.

With --env <fleet> it adds the fleet environment to spec.pausedFleets
instead (update on pipelines/pause, as for spec.paused). The Graph of every
version in flight is rebuilt in place and starts no further target of the
fleet; steps that exist run to their end. kardinal resume <pipeline> --env
<fleet> removes the entry. A fleet target or an environment that is not a
fleet is refused.`
