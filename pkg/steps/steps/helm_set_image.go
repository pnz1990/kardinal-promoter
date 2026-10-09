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

package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&helmSetImageStep{})
}

// helmSetImageStep writes the Bundle's image tag into a Helm values file at
// update.helm.imagePathTemplate (default ".image.tag"), and a chart Bundle's
// chart version into update.helm.chartVersionFile (default "Chart.yaml") at
// update.helm.chartVersionPath (default ".dependencies.0.version").
//
// The path template addresses one value, so the Bundle must carry exactly one
// image. A digest is pinned by writing "<tag>@<digest>", which a chart that
// renders "<repository>:<tag>" turns into a digest-pinned reference. A
// digest-only image, a Bundle with several images, or a path that runs into a
// scalar fail the step instead of succeeding without a change (C05-steps-17).
// The chart version path must lead through existing lists (a numeric segment
// indexes one); a missing list element fails the step.
//
// The files are edited through the YAML node API, so comments and key
// order are kept, and all file IO is confined to the checkout.
// Idempotent: setting the same tag or version twice produces the same result.
type helmSetImageStep struct{}

func (s *helmSetImageStep) Name() string { return "helm-set-image" }

func (s *helmSetImageStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	chart := state.Bundle.Chart
	if len(state.Bundle.Images) == 0 && chart == nil {
		if state.Bundle.Type == "chart" {
			err := parentsteps.Permanent(fmt.Errorf("the chart Bundle has no spec.chart"))
			return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
		}
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}
	fail := func(err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, permanentIfEscape(err)
	}

	valuesFile := "values.yaml"
	pathTemplate := ".image.tag"
	chartFile := "Chart.yaml"
	chartPath := ".dependencies.0.version"
	if h := state.Environment.Update.Helm; h != nil {
		if h.ValuesFile != "" {
			valuesFile = h.ValuesFile
		}
		if h.ImagePathTemplate != "" {
			pathTemplate = h.ImagePathTemplate
		}
		if h.ChartVersionFile != "" {
			chartFile = h.ChartVersionFile
		}
		if h.ChartVersionPath != "" {
			chartPath = h.ChartVersionPath
		}
	}

	envRel, err := envSubdir(state)
	if err != nil {
		return fail(err)
	}
	root, err := openCheckout(state)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = root.Close() }()

	var messages []string
	outputs := map[string]string{}
	if len(state.Bundle.Images) > 0 {
		// A Bundle or template the step cannot express is refused for good.
		value, err := helmImageValue(state.Bundle.Images)
		if err != nil {
			return fail(parentsteps.Permanent(err))
		}
		keys, err := yamlPathKeys(pathTemplate, "imagePathTemplate")
		if err != nil {
			return fail(parentsteps.Permanent(err))
		}
		valuesRel, err := confinedRel(filepath.Join(envRel, filepath.FromSlash(valuesFile)))
		if err != nil {
			return fail(fmt.Errorf("valuesFile: %w", err))
		}
		if err := editYAMLFile(root, valuesRel, func(doc *yamlDoc) error {
			if err := setNestedScalar(doc.root(), keys, value); err != nil {
				return fmt.Errorf("set %s in %s: %w", pathTemplate, filepath.ToSlash(valuesRel), err)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
		messages = append(messages, fmt.Sprintf("set %s=%s in %s", pathTemplate, value, valuesFile))
		outputs["helmValuesPath"] = filepath.Join(state.WorkDir, valuesRel)
		outputs["imageTag"] = value
	}
	if chart != nil {
		if chart.Version == "" {
			return fail(parentsteps.Permanent(fmt.Errorf("the Bundle's chart %s has no version", chart.Name)))
		}
		keys, err := yamlPathKeys(chartPath, "chartVersionPath")
		if err != nil {
			return fail(parentsteps.Permanent(err))
		}
		chartRel, err := confinedRel(filepath.Join(envRel, filepath.FromSlash(chartFile)))
		if err != nil {
			return fail(fmt.Errorf("chartVersionFile: %w", err))
		}
		if err := editYAMLFile(root, chartRel, func(doc *yamlDoc) error {
			if err := setPathScalar(doc.root(), keys, chart.Version); err != nil {
				return fmt.Errorf("set %s in %s: %w", chartPath, filepath.ToSlash(chartRel), err)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
		messages = append(messages, fmt.Sprintf("set %s=%s in %s", chartPath, chart.Version, chartFile))
		outputs["helmChartPath"] = filepath.Join(state.WorkDir, chartRel)
		outputs["chartVersion"] = chart.Version
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: strings.Join(messages, "; "),
		Outputs: outputs,
	}, nil
}

// yamlPathKeys splits a ".a.b.c" path into its keys; field names the spec
// field in errors.
func yamlPathKeys(path, field string) ([]string, error) {
	keys := strings.Split(strings.TrimPrefix(path, "."), ".")
	for _, k := range keys {
		if k == "" {
			return nil, fmt.Errorf("invalid %s %q", field, path)
		}
	}
	return keys, nil
}

// editYAMLFile reads rel from the checkout, applies edit to the parsed
// document and writes it back.
func editYAMLFile(root *os.Root, rel string, edit func(*yamlDoc) error) error {
	raw, err := root.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.ToSlash(rel), err)
	}
	doc, err := parseYAMLMapping(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", filepath.ToSlash(rel), err)
	}
	if err := edit(doc); err != nil {
		return err
	}
	out, err := doc.encode()
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.ToSlash(rel), err)
	}
	if err := root.WriteFile(rel, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.ToSlash(rel), err)
	}
	return nil
}

// setPathScalar sets the scalar at the key path, where a numeric key indexes
// an existing list element. Missing mapping keys are created; a missing list
// element, or a path that runs into a scalar, is an error.
func setPathScalar(n *yaml.Node, keys []string, value string) error {
	for i, k := range keys {
		last := i == len(keys)-1
		where := strings.Join(keys[:i+1], ".")
		switch n.Kind {
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(k)
			if err != nil || idx < 0 {
				return fmt.Errorf("%s is a list; index it with a number", strings.Join(keys[:i], "."))
			}
			if idx >= len(n.Content) {
				return fmt.Errorf("%s: the list has %d elements", where, len(n.Content))
			}
			if last {
				if n.Content[idx].Kind != yaml.ScalarNode {
					return fmt.Errorf("%s is not a scalar", where)
				}
				*n.Content[idx] = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
				return nil
			}
			n = n.Content[idx]
		case yaml.MappingNode:
			if last {
				if v := mapValue(n, k); v != nil && v.Kind != yaml.ScalarNode {
					return fmt.Errorf("%s is not a scalar", where)
				}
				setMapScalar(n, k, value)
				return nil
			}
			next := mapValue(n, k)
			switch {
			case next == nil:
				if _, err := strconv.Atoi(keys[i+1]); err == nil {
					return fmt.Errorf("%s does not exist", strings.Join(keys[:i+2], "."))
				}
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, next)
			case next.Kind == yaml.ScalarNode && next.Tag == "!!null":
				*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			n = next
		default:
			return fmt.Errorf("%s is not a mapping or list", strings.Join(keys[:i], "."))
		}
	}
	return nil
}

// helmImageValue returns the value to write for the Bundle's single image:
// the tag, or "<tag>@<digest>" when the image is pinned by digest.
func helmImageValue(images []v1alpha1.ImageRef) (string, error) {
	var set []v1alpha1.ImageRef
	for _, img := range images {
		if img.Tag != "" || img.Digest != "" {
			set = append(set, img)
		}
	}
	switch {
	case len(set) == 0:
		return "", fmt.Errorf("the Bundle's images have no tag")
	case len(set) > 1:
		return "", fmt.Errorf("the Bundle has %d images but imagePathTemplate addresses one value; "+
			"use one Bundle per chart image, or the kustomize strategy", len(set))
	case set[0].Tag == "":
		return "", fmt.Errorf("image %s has a digest but no tag; helm-set-image writes the tag "+
			"(as <tag>@<digest>) and cannot write a digest alone", set[0].Repository)
	case set[0].Digest != "":
		return set[0].Tag + "@" + set[0].Digest, nil
	default:
		return set[0].Tag, nil
	}
}

// setNestedScalar sets the scalar at the key path in mapping m, creating
// intermediate mappings as needed. It fails instead of replacing a scalar or
// list on the way, so a mistyped path cannot destroy existing values.
func setNestedScalar(m *yaml.Node, keys []string, value string) error {
	for i, k := range keys[:len(keys)-1] {
		next := mapValue(m, k)
		switch {
		case next == nil:
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, next)
		case next.Kind == yaml.ScalarNode && next.Tag == "!!null":
			*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		case next.Kind != yaml.MappingNode:
			return fmt.Errorf("%s is not a mapping", strings.Join(keys[:i+1], "."))
		}
		m = next
	}
	last := keys[len(keys)-1]
	if v := mapValue(m, last); v != nil && v.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s is not a scalar", strings.Join(keys, "."))
	}
	setMapScalar(m, last, value)
	return nil
}
