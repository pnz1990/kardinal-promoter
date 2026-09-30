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
	"errors"

	"github.com/spf13/cobra"
)

// errApproveRemoved is returned by `kardinal approve`. The command used to
// label the Bundle kardinal.io/approved=true and report success, but nothing
// ever read that label, so it never bypassed a gate.
var errApproveRemoved = errors.New("kardinal approve is deprecated and has no effect: " +
	"it only labelled the Bundle, and no gate ever read the label. " +
	"To force-pass a gate with an audit record, run: " +
	"kardinal override <pipeline> --stage <environment> --gate <gate-name> --reason <text>")

func newApproveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <bundle>",
		Short: "Deprecated: has no effect; use `kardinal override` to force-pass a gate",
		Long: `Deprecated. approve used to label a Bundle kardinal.io/approved=true, but no
gate, reconciler or CEL context reads that label, so it never bypassed anything.
It now fails without changing the Bundle.

To force-pass a PolicyGate with an audit record, use kardinal override:

  kardinal override nginx-demo --stage prod --gate no-weekend-deploys \
    --reason "hotfix for INC-123" --expires-in 1h`,
		Deprecated: "it has no effect; use `kardinal override` to force-pass a gate",
		Args:       cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errApproveRemoved
		},
	}
	// Accepted so old scripts get the deprecation error, not a flag error.
	cmd.Flags().String("env", "", "Ignored")
	return cmd
}
