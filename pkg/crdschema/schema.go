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

// Package crdschema checks objects against the OpenAPI schemas of the
// kardinal.io CRDs offline, as the API server does before it runs a CRD's
// CEL rules (x-kubernetes-validations): the fields it would drop (pruning)
// and the structural schema's types, enums, patterns, formats, bounds and
// required fields.
package crdschema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apivalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

// Schema is one CRD version's schema.
type Schema struct {
	structural *structuralschema.Structural
	validator  apivalidation.SchemaValidator
}

// Load reads every CRD (*.yaml) in fsys and returns the schema of version
// of each, by kind.
func Load(fsys fs.FS, version string) (map[string]Schema, error) {
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, fmt.Errorf("list CRDs: %w", err)
	}
	out := map[string]Schema{}
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		var crd apiextv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return nil, fmt.Errorf("%s: %w", path.Base(f), err)
		}
		for _, v := range crd.Spec.Versions {
			if v.Name != version || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
				continue
			}
			internal := &apiext.JSONSchemaProps{}
			if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema.OpenAPIV3Schema, internal, nil); err != nil {
				return nil, fmt.Errorf("%s: convert the schema: %w", f, err)
			}
			s, err := structuralschema.NewStructural(internal)
			if err != nil {
				return nil, fmt.Errorf("%s: structural schema: %w", f, err)
			}
			val, _, err := apivalidation.NewSchemaValidator(internal)
			if err != nil {
				return nil, fmt.Errorf("%s: schema validator: %w", f, err)
			}
			out[crd.Spec.Names.Kind] = Schema{structural: s, validator: val}
		}
	}
	return out, nil
}

// Check returns what the API server would do to obj (a decoded document)
// beyond accepting it: the paths of the fields pruning would drop, and the
// schema's validation errors on what is left, as the API server validates
// the pruned object. obj is not changed.
func (s Schema) Check(obj map[string]any) (unknown []string, invalid field.ErrorList, err error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("encode the document: %w", err)
	}
	var cp map[string]any
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, nil, fmt.Errorf("decode the document: %w", err)
	}
	unknown = pruning.PruneWithOptions(cp, s.structural, true,
		structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true})
	return unknown, apivalidation.ValidateCustomResource(nil, cp, s.validator), nil
}

// Structural is the structural schema, for walking the fields it defines.
func (s Schema) Structural() *structuralschema.Structural { return s.structural }
