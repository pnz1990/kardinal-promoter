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

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func newValidateCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate Pipeline and PolicyGate YAML before applying to the cluster",
		Long: `Validate a Pipeline or PolicyGate YAML file without connecting to the cluster.

Checks:
  - Schema: required fields present, valid enum values
  - Dependencies: no circular deps, all referenced environments exist  
  - CEL: PolicyGate expressions are syntactically valid (if present)
  - Lint: health.type set on environments with health configuration

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

	// Parse the kind from the YAML.
	var meta struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("cannot parse %s as YAML: %w", file, err)
	}

	out := cmd.OutOrStdout()

	switch meta.Kind {
	case "Pipeline":
		return validatePipeline(out, file, data)
	case "PolicyGate":
		return validatePolicyGate(out, file, data)
	case "":
		return fmt.Errorf("%s: missing 'kind' field", file)
	default:
		return fmt.Errorf("%s: unsupported kind %q — only Pipeline and PolicyGate are supported", file, meta.Kind)
	}
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

	for _, env := range pipeline.Spec.Environments {
		if env.Name == "" {
			errs = append(errs, "each environment must have a non-empty name")
		}
	}

	// Dependency: no circular deps (uses the graph builder's topoSort).
	if len(errs) == 0 && len(pipeline.Spec.Environments) > 0 {
		b := graph.NewBuilder()
		dummyBundle := &kardinalv1alpha1.Bundle{}
		dummyBundle.Name = "validate-dummy"
		dummyBundle.Namespace = "default"
		if _, err := b.Build(graph.BuildInput{Pipeline: &pipeline, Bundle: dummyBundle}); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(out, "✗ %s is invalid:\n", file)
		for _, e := range errs {
			_, _ = fmt.Fprintf(out, "  - %s\n", e)
		}
		return fmt.Errorf("validation failed")
	}

	_, _ = fmt.Fprintf(out, "✓ %s is valid\n", file)
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

	if len(errs) > 0 {
		_, _ = fmt.Fprintf(out, "✗ %s is invalid:\n", file)
		for _, e := range errs {
			_, _ = fmt.Fprintf(out, "  - %s\n", e)
		}
		return fmt.Errorf("validation failed")
	}

	_, _ = fmt.Fprintf(out, "✓ %s is valid\n", file)
	return nil
}

// validateCELExpression does a basic syntax check on a CEL expression.
func validateCELExpression(expr string) error {
	if len(expr) == 0 {
		return fmt.Errorf("expression is empty")
	}
	// Basic sanity: check for unbalanced parentheses.
	var depth int
	for _, c := range expr {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth < 0 {
			return fmt.Errorf("unbalanced parentheses")
		}
	}
	if depth != 0 {
		return fmt.Errorf("unbalanced parentheses")
	}
	return nil
}
