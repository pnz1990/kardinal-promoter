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
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// sty renders the running command's output for its stdout: glyphs and
// colour for a person at a terminal, plain words for a pipe, NO_COLOR and
// TERM=dumb (package style). The root command sets it before every command
// runs (useStyle); a function a test calls directly gets plain words.
var sty = style.Plain()

// errOut is where warnings and next-step hints go: the running command's
// stderr, so stdout carries only the result (-o json stays valid JSON).
var errOut io.Writer = os.Stderr

// commandStderr is errOut while a command runs (useStyle): its stderr, and
// what the command said once (noticeDefault). The state lives with the
// command's own writer, so nothing a command says once outlives it.
type commandStderr struct {
	io.Writer
	said map[string]bool
}

// detectStyle is the Styler of a stream (style.Detect); a test replaces it
// to render for a terminal.
var detectStyle = style.Detect

// useStyle sets sty for cmd's stdout, errOut to its stderr (nothing said
// once yet, noticeDefault), and cmd's version skew check.
func useStyle(cmd *cobra.Command) {
	sty = detectStyle(cmd.OutOrStdout())
	errOut = &commandStderr{Writer: cmd.ErrOrStderr(), said: map[string]bool{}}
	skewCheck = &versionSkew{cmd: cmd}
	// Without a UTF-8 locale (or with TERM=dumb) the stream takes ASCII
	// only: what the CLI prints from the cluster is folded too. -o json and
	// yaml keep the cluster's text exact.
	if sty.ASCIIOnly() && !structured() {
		cmd.SetOut(style.NewFolder(cmd.OutOrStdout()))
	}
}

// Styled state cells: the display label (pkg/display), with its glyph and
// colour on a terminal.

func styledStep(state string) string {
	if state == "" {
		state = "Pending"
	}
	return sty.State(display.Step(state))
}

func styledBundle(phase string) string  { return sty.State(display.Bundle(phase)) }
func styledSubStep(state string) string { return sty.State(display.SubStep(state)) }

// notice is line with its leading "ERROR:" in the failed tone or "WARNING:"
// in the blocked tone, the colours of the notices kardinal prints after a
// table; any other line is unchanged.
func notice(line string) string {
	for prefix, paint := range map[string]func(string) string{"ERROR:": sty.Error, "WARNING:": sty.Warning} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return paint(prefix) + rest
		}
	}
	return line
}

// yamlDocument starts a YAML document of a -w stream ("---"), so each
// refresh is a document of its own; nothing for the other formats.
func yamlDocument(w io.Writer) {
	if OutputFormat() == "yaml" {
		_, _ = fmt.Fprintln(w, "---")
	}
}

// watchFooter ends each frame of a -w table.
const watchFooter = "\n(watching; press Ctrl-C to quit)"

// withStructuredOutput marks cmd as one that honours -o json|yaml.
func withStructuredOutput(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[outputAnnotation] = "true"
	return cmd
}

// structured reports whether -o asks for json or yaml.
func structured() bool { f := OutputFormat(); return f == "json" || f == "yaml" }

// writeStructured writes v as -o asks: json or yaml.
func writeStructured(w io.Writer, v any) error {
	switch OutputFormat() {
	case "json":
		return WriteJSON(w, v)
	case "yaml":
		return WriteYAML(w, v)
	}
	return fmt.Errorf("-o %s is not json or yaml", globalOutput)
}

// createdBundle is -o json|yaml of a command that creates a Bundle (create
// bundle, promote, rollback): the name of what it created, so a script can
// read it (kardinal create bundle my-app --image ... -o json | jq -r .bundle).
type createdBundle struct {
	Bundle    string `json:"bundle"`
	Pipeline  string `json:"pipeline"`
	Namespace string `json:"namespace"`
	Type      string `json:"type,omitempty"`
	// Version is what the Bundle ships: the image tag, "config <sha>" or the
	// chart version.
	Version string `json:"version,omitempty"`
	// Environment is the environment promote or rollback targets.
	Environment string `json:"environment,omitempty"`
	// From is the Bundle promote copies.
	From string `json:"from,omitempty"`
	// RollbackOf is the Bundle rollback restores, and Replaces the one it
	// replaces in Environment.
	RollbackOf string `json:"rollbackOf,omitempty"`
	Replaces   string `json:"replaces,omitempty"`
	// Hold is the hold rollback --hold made.
	Hold *createdHold `json:"hold,omitempty"`
	// Fleet is, for the rollback of a whole fleet, each target with what
	// runs there and whether the rollback changes it.
	Fleet []lifecycle.FleetTargetRollback `json:"fleet,omitempty"`
	// URL is the pipeline in the UI: KARDINAL_UI_URL, else the address
	// kardinal dashboard opens.
	URL string `json:"url"`
}

// createdHold is the hold of rollback --hold.
type createdHold struct {
	Environment string       `json:"environment"`
	Bundle      string       `json:"bundle"`
	Reason      string       `json:"reason,omitempty"`
	ExpiresAt   *metav1.Time `json:"expiresAt,omitempty"`
}

// createdOf is the createdBundle of b.
func createdOf(b *v1alpha1.Bundle) createdBundle {
	return createdBundle{Bundle: b.Name, Pipeline: b.Spec.Pipeline, Namespace: b.Namespace, Type: b.Spec.Type,
		Version: scm.BundleVersion(b.Spec), URL: uiURL(b.Namespace, b.Spec.Pipeline)}
}
