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
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// The destructive commands (delete bundle, reject, override, and rollback of
// an environment at the end of the pipeline) ask before they act when a
// person runs them at a terminal; --yes answers for them. Without a terminal
// (a script, CI) they act as they always did: every invocation that worked
// keeps working.

// promptable reports whether the CLI can ask the person running it: stdin
// and stderr are terminals. A test replaces it.
var promptable = func() bool { return style.IsTerminal(os.Stdin) && style.IsTerminal(os.Stderr) }

// addYesFlag registers --yes on a destructive command.
func addYesFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("yes", false, "Do not ask for confirmation on a terminal")
}

// errCancelled is a destructive command the person declined.
var errCancelled = fmt.Errorf("cancelled: nothing was changed")

// confirm asks question on a terminal and returns errCancelled unless the
// answer is yes. With --yes, or without a terminal, it does not ask.
func confirm(cmd *cobra.Command, question string) error {
	if yes, _ := cmd.Flags().GetBool("yes"); yes || !promptable() {
		return nil
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errCancelled
}

// lastEnvironment reports whether env of pipeline is at the end of its
// promotion order: no environment depends on it (prod, in most pipelines).
// An unknown pipeline or ordering counts as the end, so the question is
// asked.
func lastEnvironment(ctx context.Context, c sigs_client.Reader, ns, pipeline, env string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
		return true
	}
	_, deps, err := graph.Ordering(&p)
	if err != nil {
		return true
	}
	for _, d := range deps {
		if slices.Contains(d, env) {
			return false
		}
	}
	return true
}
