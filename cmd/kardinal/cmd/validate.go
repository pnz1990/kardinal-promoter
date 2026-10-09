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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func newValidateCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate Pipeline and PolicyGate YAML before applying to the cluster",
		Long: `Validate Pipeline and PolicyGate YAML without connecting to the cluster.
The file may hold several documents; each kardinal.io Pipeline and PolicyGate
is checked. Other objects (a Namespace, another API group's Pipeline) are
skipped with a note.

Checks:
  - Pipeline: at least one environment, every environment named, spec.git.url
    set, the environment dependencies form a valid graph (no cycles, no
    unknown dependsOn), and no reserved field that is not implemented is set
    (steps, promotionTemplate, autoRollback, two or more regions,
    layout: branch, health.cluster, a health.resource.kind other than
    Deployment). The controller reports the same fields as
    Ready=False/NotImplemented on the Pipeline. With metadata.namespace set,
    a git.secretRef in another namespace is an error too (the controller
    reports it as Ready=False/ValidationFailed). spec.policyGates is an
    error (the API server rejects it); spec.git.provider is a warning (the
    controller ignores it).
  - PolicyGate: spec.expression set and compiles with the controller's
    PolicyGate CEL environment; no spec.selector and a name of at most 63
    characters (the API server rejects both); spec.when is a warning (it has
    no effect)

This is not full CRD schema validation; 'kubectl apply --dry-run=server'
checks the schema.

Exit codes:
  0 — file is valid
  1 — validation failed (actionable errors printed)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(cmd, file)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to Pipeline or PolicyGate YAML file (required)")
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runValidate(cmd *cobra.Command, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", file, err)
	}

	out := cmd.OutOrStdout()
	dec := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	docs, checked, failed := 0, 0, 0
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("cannot parse %s as YAML: %w", file, err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			continue // empty document
		}
		docs++
		var meta struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("cannot parse %s as YAML: %w", file, err)
		}
		if meta.Kind == "" {
			return fmt.Errorf("%s: missing 'kind' field", file)
		}
		// A missing apiVersion is taken as kardinal.io, so a bare
		// "kind: Pipeline" snippet is still checked.
		group, _, _ := strings.Cut(meta.APIVersion, "/")
		ours := meta.APIVersion == "" || group == kardinalv1alpha1.GroupVersion.Group
		switch {
		case ours && meta.Kind == "Pipeline":
			err = validatePipeline(out, file, raw)
		case ours && meta.Kind == "PolicyGate":
			err = validatePolicyGate(out, file, raw)
		default:
			// kubectl apply takes the whole file; only the objects this
			// command knows are checked (E2E-R01).
			_, _ = fmt.Fprintf(out, "- skipped %s/%s: validate checks only kardinal.io Pipelines and PolicyGates\n",
				meta.Kind, meta.Metadata.Name)
			continue
		}
		checked++
		if err != nil {
			failed++
		}
	}
	if docs == 0 {
		return fmt.Errorf("%s: no documents found", file)
	}
	if checked == 0 {
		return fmt.Errorf("%s: no kardinal.io Pipeline or PolicyGate found", file)
	}
	if failed > 0 {
		return fmt.Errorf("validation failed")
	}
	return nil
}

func validatePipeline(out io.Writer, file string, data []byte) error {
	var pipeline kardinalv1alpha1.Pipeline
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		return fmt.Errorf("%s: YAML parse error: %w", file, err)
	}

	var errs []string

	// Schema: at least one environment.
	if len(pipeline.Spec.Environments) == 0 {
		errs = append(errs, "spec.environments must contain at least one environment")
	}

	reserved := false
	for _, env := range pipeline.Spec.Environments {
		if env.Name == "" {
			errs = append(errs, "each environment must have a non-empty name")
		}
		// The API server refuses it with this message (#1358); the Graph build
		// below would report it again, naming validate's own Bundle.
		if graph.ReservedEnvironmentName(env.Name) {
			errs = append(errs, fmt.Sprintf("environment %q: %s", env.Name, graph.ReservedEnvironmentMessage))
			reserved = true
		}
	}

	// Schema: the CRD requires spec.git.url (minLength 1).
	if pipeline.Spec.Git.URL == "" {
		errs = append(errs, "spec.git.url is required")
	}

	// Reserved fields a Bundle fails on when it reaches an environment that
	// uses one; the controller sets the Pipeline Ready=False/NotImplemented for
	// the same list.
	errs = append(errs, graph.UnimplementedFields(&pipeline)...)

	// The API server rejects a non-empty spec.policyGates (CRD CEL rule), so
	// the file would not apply.
	if len(pipeline.Spec.PolicyGates) > 0 { //nolint:staticcheck // SA1019: read the deprecated field to reject it
		errs = append(errs, "spec.policyGates is not implemented; remove it (org gates use the kardinal.io/applies-to label)")
	}

	// Warnings do not fail validation.
	var warnings []string
	if pipeline.Spec.Git.Provider != "" { //nolint:staticcheck // SA1019: warn that the deprecated field is set
		warnings = append(warnings, "spec.git.provider is deprecated and ignored: the controller's "+
			"--scm-provider flag selects the SCM provider; remove it")
	}

	// A git.secretRef in another namespace is refused (ValidationFailed). A file
	// without metadata.namespace gets its namespace at apply time, so it is not
	// judged offline.
	if pipeline.Namespace != "" {
		if err := graph.ValidateSecretRef(&pipeline); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := graph.ValidateUpdateStrategy(&pipeline); err != nil {
		errs = append(errs, err.Error())
	}

	renderedErr := graph.ValidateRenderedBranches(&pipeline)
	if renderedErr != nil {
		errs = append(errs, renderedErr.Error())
	}

	// Dependency: no circular deps (uses the graph builder's topoSort).
	if len(pipeline.Spec.Environments) > 0 && !hasUnnamedEnv(pipeline) {
		b := graph.NewBuilder()
		dummyBundle := &kardinalv1alpha1.Bundle{}
		dummyBundle.Name = "validate-dummy"
		dummyBundle.Namespace = "default"
		// Build rejects custom steps and two or more regions too; they are
		// reported above already.
		buildable := pipeline.DeepCopy()
		for i := range buildable.Spec.Environments {
			buildable.Spec.Environments[i].Steps = nil             //nolint:staticcheck // SA1019: clear the deprecated field reported above
			buildable.Spec.Environments[i].PromotionTemplate = nil //nolint:staticcheck // SA1019: clear the deprecated field reported above
			buildable.Spec.Environments[i].Regions = nil           //nolint:staticcheck // SA1019: cleared because it is reported above
			if renderedErr != nil {
				buildable.Spec.Environments[i].Layout = "directory" // reported above
			}
		}
		if renderedErr != nil {
			buildable.Spec.Git.Layout = "directory"
		}
		// A reserved name fails Build on its node ID, already reported above;
		// the ordering checks (unknown dependsOn, cycles) still run.
		check := func() error {
			_, err := b.Build(graph.BuildInput{Pipeline: buildable, Bundle: buildableBundle(dummyBundle)})
			return err
		}
		if reserved {
			check = func() error { return graph.ValidateOrdering(buildable) }
		}
		if err := check(); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(out, "✗ %s is invalid:\n", file)
		for _, e := range errs {
			_, _ = fmt.Fprintf(out, "  - %s\n", e)
		}
		printValidateWarnings(out, warnings)
		return fmt.Errorf("validation failed")
	}

	_, _ = fmt.Fprintf(out, "✓ %s is valid\n", file)
	printValidateWarnings(out, warnings)
	return nil
}

func validatePolicyGate(out io.Writer, file string, data []byte) error {
	var gate kardinalv1alpha1.PolicyGate
	if err := yaml.Unmarshal(data, &gate); err != nil {
		return fmt.Errorf("%s: YAML parse error: %w", file, err)
	}

	var errs []string

	// Schema: expression required.
	if gate.Spec.Expression == "" {
		errs = append(errs, "spec.expression is required")
	}

	// CEL validation (basic syntax check).
	if gate.Spec.Expression != "" {
		if err := validateCELExpression(gate.Spec.Expression); err != nil {
			errs = append(errs, fmt.Sprintf("spec.expression CEL error: %v", err))
		}
	}

	// The API server rejects both (CRD CEL rules), so the file would not apply.
	if gate.Spec.Selector != nil { //nolint:staticcheck // SA1019: read the deprecated field to reject it
		errs = append(errs, "spec.selector is not implemented; use the kardinal.io/applies-to label")
	}
	if !policyGateNameAllowed(&gate) {
		errs = append(errs, fmt.Sprintf("metadata.name %q has %d characters: PolicyGate names are at most %d "+
			"characters, because the name is copied into the kardinal.io/gate-template label of every gate instance; "+
			"use a name of at most %d characters",
			gate.Name, len(gate.Name), maxPolicyGateNameLength, maxPolicyGateNameLength))
	}

	// Warnings do not fail validation.
	var warnings []string
	if gate.Spec.When != "" { //nolint:staticcheck // SA1019: warn that the deprecated field is set (#1323)
		warnings = append(warnings, "spec.when is deprecated and has no effect: every gate is re-checked "+
			"right before its PromotionStep starts; remove it")
	}

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(out, "✗ %s is invalid:\n", file)
		for _, e := range errs {
			_, _ = fmt.Fprintf(out, "  - %s\n", e)
		}
		printValidateWarnings(out, warnings)
		return fmt.Errorf("validation failed")
	}

	_, _ = fmt.Fprintf(out, "✓ %s is valid\n", file)
	printValidateWarnings(out, warnings)
	return nil
}

// maxPolicyGateNameLength is the longest PolicyGate name the CRD accepts.
const maxPolicyGateNameLength = 63

// policyGateNameAllowed mirrors the PolicyGate CRD rule: a name is at most 63
// characters unless spec.generated is set, as kardinal sets it on the gate
// instances and pause freeze gates it creates.
func policyGateNameAllowed(gate *kardinalv1alpha1.PolicyGate) bool {
	return len(gate.Name) <= maxPolicyGateNameLength || gate.Spec.Generated
}

// printValidateWarnings prints each warning under a file's result line.
func printValidateWarnings(out io.Writer, warnings []string) {
	for _, w := range warnings {
		_, _ = fmt.Fprintf(out, "  ! warning: %s\n", w)
	}
}

func hasUnnamedEnv(p kardinalv1alpha1.Pipeline) bool {
	for _, env := range p.Spec.Environments {
		if env.Name == "" {
			return true
		}
	}
	return false
}

// validateCELExpression compiles expr with the controller's PolicyGate CEL
// environment, the same check the controller runs on a template gate.
func validateCELExpression(expr string) error {
	if len(expr) == 0 {
		return fmt.Errorf("expression is empty")
	}
	msg, invalid, err := celSyntaxCheck(context.Background(), expr)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New(msg)
	}
	return nil
}
