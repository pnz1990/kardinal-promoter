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
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&helmSetImageStep{})
}

// helmSetImageStep writes the Bundle's image tag into a Helm values file at
// update.helm.imagePathTemplate (default ".image.tag").
//
// The path template addresses one value, so the Bundle must carry exactly one
// image. A digest is pinned by writing "<tag>@<digest>", which a chart that
// renders "<repository>:<tag>" turns into a digest-pinned reference. A
// digest-only image, a Bundle with several images, or a path that runs into a
// scalar fail the step instead of succeeding without a change (C05-steps-17).
//
// The values file is edited through the YAML node API, so comments and key
// order are kept, and all file IO is confined to the checkout.
// Idempotent: setting the same tag twice produces the same result.
type helmSetImageStep struct{}

func (s *helmSetImageStep) Name() string { return "helm-set-image" }

func (s *helmSetImageStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}
	fail := func(err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("helm-set-image: %v", err)},
			fmt.Errorf("helm-set-image: %w", err)
	}

	valuesFile := "values.yaml"
	pathTemplate := ".image.tag"
	if h := state.Environment.Update.Helm; h != nil {
		if h.ValuesFile != "" {
			valuesFile = h.ValuesFile
		}
		if h.ImagePathTemplate != "" {
			pathTemplate = h.ImagePathTemplate
		}
	}

	value, err := helmImageValue(state.Bundle.Images)
	if err != nil {
		return fail(err)
	}
	keys := strings.Split(strings.TrimPrefix(pathTemplate, "."), ".")
	for _, k := range keys {
		if k == "" {
			return fail(fmt.Errorf("invalid imagePathTemplate %q", pathTemplate))
		}
	}

	envRel, err := envSubdir(state)
	if err != nil {
		return fail(err)
	}
	valuesRel, err := confinedRel(filepath.Join(envRel, filepath.FromSlash(valuesFile)))
	if err != nil {
		return fail(fmt.Errorf("valuesFile: %w", err))
	}
	root, err := openCheckout(state)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = root.Close() }()

	raw, err := root.ReadFile(valuesRel)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", filepath.ToSlash(valuesRel), err))
	}
	doc, err := parseYAMLMapping(raw)
	if err != nil {
		return fail(fmt.Errorf("parse %s: %w", filepath.ToSlash(valuesRel), err))
	}
	if err := setNestedScalar(doc.root(), keys, value); err != nil {
		return fail(fmt.Errorf("set %s in %s: %w", pathTemplate, filepath.ToSlash(valuesRel), err))
	}
	out, err := doc.encode()
	if err != nil {
		return fail(fmt.Errorf("encode %s: %w", filepath.ToSlash(valuesRel), err))
	}
	if err := root.WriteFile(valuesRel, out, 0o644); err != nil {
		return fail(fmt.Errorf("write %s: %w", filepath.ToSlash(valuesRel), err))
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("set %s=%s in %s", pathTemplate, value, valuesFile),
		Outputs: map[string]string{
			"helmValuesPath": filepath.Join(state.WorkDir, valuesRel),
			"imageTag":       value,
		},
	}, nil
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
