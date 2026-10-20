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
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	crdfiles "github.com/kardinal-promoter/kardinal-promoter/config/crd"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/crdschema"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func newValidateCmd() *cobra.Command {
	var file string
	var allowedRepos []string
	var scmProviderType string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate kardinal YAML before applying to the cluster",
		Long: `Check kardinal.io YAML offline: every object against its CRD's schema (built
into the CLI); Pipelines and PolicyGates also for their environments, the
dependency graph, unimplemented fields and gate expressions. It exits 1
with the errors; kubectl apply --dry-run=server also runs the CRDs' CEL
rules.`,
		Example: `  kardinal validate -f pipeline.yaml
  kardinal validate -f examples/ --allowed-repositories github.com/acme/*`,
		Annotations: map[string]string{ReferenceAnnotation: validateReference},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(cmd, file, allowedRepos, scmProviderType)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to Pipeline or PolicyGate YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringSliceVar(&allowedRepos, "allowed-repositories", nil,
		"The controller's scm.allowedRepositories (comma-separated host/repository globs): report a "+
			"Pipeline that would need the controller's SCM token for a spec.git.url that is not one of them")
	cmd.Flags().StringVar(&scmProviderType, "scm-provider", "",
		"The controller's scm.provider, for --allowed-repositories: with bitbucket-datacenter a spec.git.url "+
			"is matched as KEY/slug however it names the repository (/scm/, browse or ssh path), as the controller does")

	return cmd
}

func runValidate(cmd *cobra.Command, file string, allowedRepos []string, scmProviderType string) error {
	allowed, err := scm.ParseRepositoryAllowlist(allowedRepos)
	if err != nil {
		return fmt.Errorf("--allowed-repositories: %w", err)
	}
	allowed = allowed.WithProviderType(scmProviderType)
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
		case !ours:
			// kubectl apply takes the whole file; only kardinal.io objects
			// are checked (E2E-R01).
			_, _ = fmt.Fprintf(out, "- skipped %s/%s: validate checks only kardinal.io objects\n",
				meta.Kind, meta.Metadata.Name)
			continue
		case meta.Kind == "Pipeline":
			err = validatePipeline(out, file, raw, allowed)
		case meta.Kind == "PolicyGate":
			err = validatePolicyGate(out, file, raw)
		default:
			// Any other kardinal.io kind: its CRD schema (#1287).
			err = reportValidation(out, file, schemaErrors(meta.Kind, raw, nil), nil)
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
		return fmt.Errorf("%s: no kardinal.io object found", file)
	}
	if failed > 0 {
		return fmt.Errorf("validation failed")
	}
	return nil
}

func validatePipeline(out io.Writer, file string, data []byte, allowed *scm.RepositoryAllowlist) error {
	var pipeline kardinalv1alpha1.Pipeline
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		// A value of the wrong type: the schema says which, with its path.
		errs := schemaErrors("Pipeline", data, nil)
		if len(errs) == 0 {
			errs = []string{fmt.Sprintf("YAML parse error: %v", err)}
		}
		return reportValidation(out, file, errs, nil)
	}

	var errs []string

	// At least one environment.
	if len(pipeline.Spec.Environments) == 0 {
		errs = append(errs, "spec.environments must contain at least one environment")
	}

	reserved := false
	for i, env := range pipeline.Spec.Environments {
		if env.Name == "" {
			errs = append(errs, fmt.Sprintf("spec.environments[%d].name is required", i))
		}
		// The API server refuses it with this message (#1358); the Graph build
		// below would report it again, naming validate's own Bundle.
		if graph.ReservedEnvironmentName(env.Name) {
			errs = append(errs, fmt.Sprintf("environment %q: %s", env.Name, graph.ReservedEnvironmentMessage))
			reserved = true
		}
	}

	// The CRD requires spec.git.url (minLength 1); said plainly.
	if pipeline.Spec.Git.URL == "" {
		errs = append(errs, "spec.git.url is required")
	}

	// Reserved fields a Bundle fails on when it reaches an environment that
	// uses one; the controller sets the Pipeline Ready=False/NotImplemented for
	// the same list.
	errs = append(errs, graph.UnimplementedFields(&pipeline)...)

	// Warnings do not fail validation.
	var warnings []string

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
	if err := scm.ValidatePipelinePR(&pipeline); err != nil {
		errs = append(errs, err.Error())
	}
	// #1332: the controller's shared token may act only on the allowed
	// repositories (Ready=False/RepositoryNotAllowed). Offline, a
	// git.secretRef is taken to name a Secret that exists; the controller
	// checks that it does.
	ref := pipeline.Spec.Git.SecretRef
	if err := allowed.CheckPipeline(&pipeline, ref != nil && ref.Name != ""); err != nil {
		errs = append(errs, scm.NotAllowedMessage(err))
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
		buildable := pipeline.DeepCopy()
		for i := range buildable.Spec.Environments {
			if renderedErr != nil {
				buildable.Spec.Environments[i].Layout = "directory" // reported above
			}
			// An Application or ClusterProfile fleet's targets are in the
			// cluster (status.fleets): offline, check the rest of the
			// Pipeline with one stand-in target.
			env := buildable.Spec.Environments[i]
			if f := env.Fleet; f != nil && f.Selector != nil && f.Selector.Kind != kardinalv1alpha1.FleetSelectorTarget &&
				!hasFleetStatus(buildable, env.Name) {
				buildable.Status.Fleets = append(buildable.Status.Fleets, kardinalv1alpha1.FleetStatus{Environment: env.Name,
					Targets: []kardinalv1alpha1.FleetTarget{{Name: "t"}}})
				kind := f.Selector.Kind
				if kind == "" {
					kind = kardinalv1alpha1.FleetSelectorApplication
				}
				warnings = append(warnings, fmt.Sprintf("environment %q: its fleet targets are the %ss its selector matches in the cluster; "+
					"validate does not read the cluster (the Pipeline's status.fleets lists them once applied)", env.Name, kind))
			}
		}
		if renderedErr != nil {
			buildable.Spec.Git.Layout = "directory"
		}
		// A reserved name fails Build on its node ID, already reported above;
		// the ordering checks (unknown dependsOn, cycles) still run.
		check := func() error {
			// The AnalysisTemplates verification names are in the cluster,
			// which validate does not read (#2011).
			preview, _, err := previewPipeline(buildable)
			if err != nil {
				return err
			}
			_, err = b.Build(graph.BuildInput{Pipeline: preview, Bundle: buildableBundle(dummyBundle)})
			return err
		}
		if reserved {
			check = func() error { return graph.ValidateOrdering(buildable) }
		}
		if err := check(); err != nil {
			errs = append(errs, err.Error())
		}
	}

	return reportValidation(out, file, append(errs, schemaErrors("Pipeline", data, errs)...), warnings)
}

func validatePolicyGate(out io.Writer, file string, data []byte) error {
	var gate kardinalv1alpha1.PolicyGate
	if err := yaml.Unmarshal(data, &gate); err != nil {
		// A value of the wrong type: the schema says which, with its path.
		errs := schemaErrors("PolicyGate", data, nil)
		if len(errs) == 0 {
			errs = []string{fmt.Sprintf("YAML parse error: %v", err)}
		}
		return reportValidation(out, file, errs, nil)
	}

	var errs []string

	// Expression required.
	if gate.Spec.Expression == "" {
		errs = append(errs, "spec.expression is required")
	}

	// CEL validation (basic syntax check).
	if gate.Spec.Expression != "" {
		if err := validateCELExpression(gate.Spec.Expression); err != nil {
			errs = append(errs, fmt.Sprintf("spec.expression CEL error: %v", err))
		}
	}

	if !policyGateNameAllowed(&gate) {
		errs = append(errs, fmt.Sprintf("metadata.name %q has %d characters: PolicyGate names are at most %d "+
			"characters, because the name is copied into the kardinal.io/gate-template label of every gate instance; "+
			"use a name of at most %d characters",
			gate.Name, len(gate.Name), maxPolicyGateNameLength, maxPolicyGateNameLength))
	}

	// Warnings do not fail validation.
	var warnings []string
	if gate.Spec.When != "" {
		warnings = append(warnings, "spec.when is ignored: every gate is re-checked right before its PromotionStep "+
			"starts; remove it")
	}

	return reportValidation(out, file, append(errs, schemaErrors("PolicyGate", data, errs)...), warnings)
}

// reportValidation prints file's result: invalid with each error, or
// valid; then the warnings.
func reportValidation(out io.Writer, file string, errs, warnings []string) error {
	if len(errs) > 0 {
		_, _ = fmt.Fprintf(out, "%s%s is invalid:\n", sty.Paint(display.ToneDegraded, sty.Sym("✗ ", "")), file)
		for _, e := range errs {
			_, _ = fmt.Fprintf(out, "  - %s\n", e)
		}
		printValidateWarnings(out, warnings)
		return fmt.Errorf("validation failed")
	}
	_, _ = fmt.Fprintf(out, "%s%s is valid\n", sty.Paint(display.ToneVerified, sty.Sym("✓ ", "")), file)
	printValidateWarnings(out, warnings)
	return nil
}

// crdSchemas are the schemas of the CRDs the CLI embeds (config/crd/bases),
// by kind, loaded once.
var crdSchemas = sync.OnceValues(func() (map[string]crdschema.Schema, error) {
	return crdschema.Load(crdfiles.Bases(), kardinalv1alpha1.GroupVersion.Version)
})

// schemaErrors checks the document data of kind against its CRD's schema
// (#1287), as the API server does before the CRD's CEL rules: the fields it
// would drop, each with its path, then the schema's errors (types, enums,
// patterns, required fields). An error on a field one of reported already
// names ("spec.git.url is required") is left out, so a problem is said once.
func schemaErrors(kind string, data []byte, reported []string) []string {
	schemas, err := crdSchemas()
	if err != nil {
		return []string{fmt.Sprintf("the CLI's embedded CRDs: %v", err)}
	}
	s, ok := schemas[kind]
	if !ok {
		return []string{fmt.Sprintf("kind %q is not a kardinal.io kind (the CLI knows: %s)", kind, strings.Join(slices.Sorted(maps.Keys(schemas)), ", "))}
	}
	var obj map[string]any
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return []string{fmt.Sprintf("YAML parse error: %v", err)}
	}
	unknown, invalid, err := s.Check(obj)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, p := range unknown {
		out = append(out, fmt.Sprintf("unknown field %q: the API server refuses a field the CRD does not define; remove it", p))
	}
	for _, e := range invalid {
		if !slices.ContainsFunc(reported, func(r string) bool { return strings.HasPrefix(r, e.Field+" ") }) {
			out = append(out, e.Error())
		}
	}
	return out
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

// hasFleetStatus reports whether p's status.fleets has environment env.
func hasFleetStatus(p *kardinalv1alpha1.Pipeline, env string) bool {
	for _, f := range p.Status.Fleets {
		if f.Environment == env {
			return true
		}
	}
	return false
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

// validateReference is the detail of the command for its reference page.
const validateReference = `Validate kardinal.io YAML without connecting to the cluster. The file may
hold several documents; each kardinal.io object is checked. Other objects (a
Namespace, another API group's Pipeline) are skipped with a note.

Checks:
  - Every kardinal.io document, against the OpenAPI schema of its CRD, which
    the CLI embeds (the CRDs of its own version, config/crd/bases): no field
    the CRD does not define (kubectl apply's strict field validation refuses
    it), and the schema's types, enums, patterns, formats, bounds and
    required fields, each with its path and the API server's message. As
    the API server does on create, a required field with a default may be
    left out (MetricCheck spec.provider) and a status is ignored (kubectl
    get -o yaml output validates). A kind kardinal.io does not have is an
    error.
  - Pipeline: at least one environment, every environment named, spec.git.url
    set, the environment dependencies form a valid graph (no cycles, no
    unknown dependsOn), and no reserved field that is not implemented is set
    (autoRollback, a health.resource.kind other than Deployment). The
    controller reports the same fields as Ready=False/NotImplemented on the
    Pipeline. With metadata.namespace set,
    a git.secretRef in another namespace is an error too (the controller
    reports it as Ready=False/ValidationFailed). With
    --allowed-repositories (the controller's scm.allowedRepositories), a
    Pipeline must point spec.git.url at one of them unless it never needs
    the controller's SCM token: a git.secretRef and no pr-review
    environment (the controller reports Ready=False/RepositoryNotAllowed);
    with --scm-provider bitbucket-datacenter a URL is matched as KEY/slug,
    as the controller does.
  - PolicyGate: spec.expression set and compiles with the controller's
    PolicyGate CEL environment, and a name of at most 63 characters (the API
    server rejects a longer one); spec.when is a warning (it is ignored)

The CRDs' CEL rules (x-kubernetes-validations) are not run offline, beyond
the ones the checks above repeat (the PolicyGate name length, a reserved
environment name, the removed fields); 'kubectl apply --dry-run=server' runs
them all. The schema is the CLI's version's: use the CLI of the controller's
version.

Exit codes:
  0: file is valid
  1: validation failed (actionable errors printed)`
