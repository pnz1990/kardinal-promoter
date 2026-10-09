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
	chartPath := ""
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
		if chartPath == "" {
			// The umbrella chart's dependency of that name, not the first one.
			chartPath = fmt.Sprintf(".dependencies[name=%s].version", chart.Name)
		}
		segs, err := parseChartVersionPath(chartPath)
		if err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("invalid chartVersionPath %q: %w", chartPath, err)))
		}
		chartRel, err := confinedRel(filepath.Join(envRel, filepath.FromSlash(chartFile)))
		if err != nil {
			return fail(fmt.Errorf("chartVersionFile: %w", err))
		}
		if err := editYAMLFile(root, chartRel, func(doc *yamlDoc) error {
			if err := setPathScalar(doc.root(), segs, chart.Version); err != nil {
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

// yamlPathSeg is one segment of a chart version path: a mapping key, a list
// index, or a list element selected by one of its fields ([name=podinfo]).
type yamlPathSeg struct {
	key        string
	index      int // >= 0 for a list index
	matchField string
	matchValue string
}

func (g yamlPathSeg) String() string {
	switch {
	case g.matchField != "":
		return "[" + g.matchField + "=" + g.matchValue + "]"
	case g.index >= 0:
		return strconv.Itoa(g.index)
	default:
		return g.key
	}
}

// parseChartVersionPath parses ".a.b.0.c" and ".dependencies[name=podinfo].version":
// segments separated by dots; a numeric segment indexes a list; "[f=v]"
// after a segment selects the list element whose field f is v.
func parseChartVersionPath(path string) ([]yamlPathSeg, error) {
	p := strings.TrimPrefix(path, ".")
	var segs []yamlPathSeg
	for p != "" {
		if strings.HasPrefix(p, "[") {
			end := strings.Index(p, "]")
			if end < 0 {
				return nil, fmt.Errorf("unclosed [")
			}
			field, value, ok := strings.Cut(p[1:end], "=")
			if !ok || field == "" || value == "" {
				return nil, fmt.Errorf("a selector is [field=value]")
			}
			segs = append(segs, yamlPathSeg{index: -1, matchField: field, matchValue: value})
			p = strings.TrimPrefix(p[end+1:], ".")
			continue
		}
		end := strings.IndexAny(p, ".[")
		if end < 0 {
			end = len(p)
		}
		part := p[:end]
		if part == "" {
			return nil, fmt.Errorf("empty segment")
		}
		if n, err := strconv.Atoi(part); err == nil && n >= 0 {
			segs = append(segs, yamlPathSeg{index: n})
		} else {
			segs = append(segs, yamlPathSeg{key: part, index: -1})
		}
		p = p[end:]
		p = strings.TrimPrefix(p, ".")
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	if last := segs[len(segs)-1]; last.key == "" && last.index < 0 {
		return nil, fmt.Errorf("the path must end in a key or an index")
	}
	return segs, nil
}

// setPathScalar sets the scalar at segs. A list segment (index or selector)
// must find an existing element; missing mapping keys are created; a path
// through a scalar is an error.
func setPathScalar(n *yaml.Node, segs []yamlPathSeg, value string) error {
	where := func(i int) string {
		parts := make([]string, 0, i+1)
		for _, g := range segs[:i+1] {
			parts = append(parts, g.String())
		}
		return strings.Join(parts, ".")
	}
	for i, g := range segs {
		last := i == len(segs)-1
		var next *yaml.Node
		switch {
		case g.key == "" && n.Kind != yaml.SequenceNode:
			return fmt.Errorf("%s is not a list", where(i-1))
		case g.matchField != "":
			for _, el := range n.Content {
				if el.Kind == yaml.MappingNode && scalarValue(el, g.matchField) == g.matchValue {
					next = el
					break
				}
			}
			if next == nil {
				return fmt.Errorf("%s has no element with %s %q", where(i-1), g.matchField, g.matchValue)
			}
		case g.key == "":
			if g.index >= len(n.Content) {
				return fmt.Errorf("%s: the list has %d elements", where(i), len(n.Content))
			}
			next = n.Content[g.index]
		case n.Kind == yaml.SequenceNode:
			return fmt.Errorf("%s is a list; index it with a number or [field=value]", where(i-1))
		case n.Kind != yaml.MappingNode:
			return fmt.Errorf("%s is not a mapping or list", where(i-1))
		case last:
			if v := mapValue(n, g.key); v != nil && v.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s is not a scalar", where(i))
			}
			setMapScalar(n, g.key, value)
			return nil
		default:
			next = mapValue(n, g.key)
			switch {
			case next == nil:
				if nxt := segs[i+1]; nxt.key == "" {
					return fmt.Errorf("%s does not exist", where(i))
				}
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: g.key}, next)
			case next.Kind == yaml.ScalarNode && next.Tag == "!!null":
				*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
		}
		if last {
			if next.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s is not a scalar", where(i))
			}
			*next = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return nil
		}
		n = next
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
