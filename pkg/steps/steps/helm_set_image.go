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
	"strings"

	"sigs.k8s.io/yaml"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&helmSetImageStep{})
}

// helmSetImageStep updates the image tag in a Helm values.yaml file.
// It reads the values file, sets the image tag at the configured path, and writes it back.
// Idempotent: setting the same tag twice produces the same result.
type helmSetImageStep struct{}

func (s *helmSetImageStep) Name() string { return "helm-set-image" }

func (s *helmSetImageStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}

	// Determine values file path.
	envPath := filepath.Join(state.WorkDir, envSubdir(state))
	valuesFile := "values.yaml"
	pathTemplate := ".image.tag"
	if state.Environment.Update.Helm != nil {
		if state.Environment.Update.Helm.ValuesFile != "" {
			valuesFile = state.Environment.Update.Helm.ValuesFile
		}
		if state.Environment.Update.Helm.ImagePathTemplate != "" {
			pathTemplate = state.Environment.Update.Helm.ImagePathTemplate
		}
	}

	valuesPath := filepath.Join(envPath, valuesFile)
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("read %s: %v", valuesPath, err),
		}, fmt.Errorf("helm-set-image: read values: %w", err)
	}

	// Parse YAML into a generic map.
	var values map[string]interface{}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("parse %s: %v", valuesPath, err),
		}, fmt.Errorf("helm-set-image: parse yaml: %w", err)
	}
	if values == nil {
		values = make(map[string]interface{})
	}

	// Use the first image with a non-empty tag.
	var tag string
	for _, img := range state.Bundle.Images {
		if img.Tag != "" {
			tag = img.Tag
			break
		}
	}
	if tag == "" {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no image tag to set"}, nil
	}

	// Set the value at the dot-path (e.g. ".image.tag" or "image.tag").
	path := strings.TrimPrefix(pathTemplate, ".")
	if err := setNestedValue(values, strings.Split(path, "."), tag); err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("set path %s: %v", pathTemplate, err),
		}, fmt.Errorf("helm-set-image: set value: %w", err)
	}

	// Marshal back to YAML.
	updated, err := yaml.Marshal(values)
	if err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("marshal yaml: %v", err),
		}, fmt.Errorf("helm-set-image: marshal: %w", err)
	}

	if err := os.WriteFile(valuesPath, updated, 0o644); err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("write %s: %v", valuesPath, err),
		}, fmt.Errorf("helm-set-image: write values: %w", err)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("set %s=%s in %s", pathTemplate, tag, valuesFile),
		Outputs: map[string]string{
			"helmValuesPath": valuesPath,
			"imageTag":       tag,
		},
	}, nil
}

// setNestedValue sets a value at a dot-separated key path in a map[string]interface{}.
// Creates intermediate maps as needed. Idempotent.
func setNestedValue(m map[string]interface{}, keys []string, value string) error {
	if len(keys) == 0 {
		return fmt.Errorf("empty key path")
	}
	if len(keys) == 1 {
		m[keys[0]] = value
		return nil
	}
	next, ok := m[keys[0]]
	if !ok {
		next = make(map[string]interface{})
		m[keys[0]] = next
	}
	nested, ok := next.(map[string]interface{})
	if !ok {
		// Overwrite scalar with a map.
		nested = make(map[string]interface{})
		m[keys[0]] = nested
	}
	return setNestedValue(nested, keys[1:], value)
}
