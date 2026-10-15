// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apivalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// Admission validates objects the way the API server admits a custom
// resource: the generated CRD in config/crd/bases is defaulted, pruned,
// checked against its OpenAPI schema and then against its
// x-kubernetes-validations CEL rules, with the API server's own libraries
// (k8s.io/apiextensions-apiserver). A fake client does none of this, so a
// scenario that builds an object the API server would refuse would test a
// state no cluster can reach; the scenario suite admits every kardinal object
// it creates here first.
type Admission struct {
	mu    sync.Mutex
	kinds map[string]*crdSchema
	root  string
}

type crdSchema struct {
	structural *structuralschema.Structural
	schema     apivalidation.SchemaValidator
	cel        *cel.Validator
}

// NewAdmission reads CRDs from root/config/crd/bases on first use.
func NewAdmission(root string) *Admission {
	return &Admission{root: root, kinds: map[string]*crdSchema{}}
}

// crdFiles maps a kind to its generated CRD file.
var crdFiles = map[string]string{
	"Pipeline":      "kardinal.io_pipelines.yaml",
	"Bundle":        "kardinal.io_bundles.yaml",
	"PolicyGate":    "kardinal.io_policygates.yaml",
	"ChangeWindow":  "kardinal.io_changewindows.yaml",
	"MetricCheck":   "kardinal.io_metricchecks.yaml",
	"Approval":      "kardinal.io_approvals.yaml",
	"PromotionStep": "kardinal.io_promotionsteps.yaml",
	"ScmProvider":   "kardinal.io_scmproviders.yaml",
	"AgentIdentity": "kardinal.io_agentidentities.yaml",
	"Verdict":       "kardinal.io_verdicts.yaml",
	"StepTemplate":  "kardinal.io_steptemplates.yaml",
}

func (a *Admission) load(kind string) (*crdSchema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.kinds[kind]; ok {
		return s, nil
	}
	file, ok := crdFiles[kind]
	if !ok {
		return nil, fmt.Errorf("admission: no CRD file for kind %s", kind)
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "config", "crd", "bases", file))
	if err != nil {
		return nil, fmt.Errorf("admission: read CRD: %w", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		return nil, fmt.Errorf("admission: decode %s: %w", file, err)
	}
	if len(crd.Spec.Versions) == 0 || crd.Spec.Versions[0].Schema == nil {
		return nil, fmt.Errorf("admission: %s has no schema", file)
	}
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil); err != nil {
		return nil, fmt.Errorf("admission: convert %s: %w", file, err)
	}
	st, err := structuralschema.NewStructural(&internal)
	if err != nil {
		return nil, fmt.Errorf("admission: structural %s: %w", file, err)
	}
	sv, _, err := apivalidation.NewSchemaValidator(&internal)
	if err != nil {
		return nil, fmt.Errorf("admission: schema validator %s: %w", file, err)
	}
	s := &crdSchema{structural: st, schema: sv, cel: cel.NewValidator(st, true, celconfig.PerCallLimit)}
	a.kinds[kind] = s
	return s, nil
}

// Admit defaults obj (a typed kardinal object) in place, as the API server
// does on create, and returns the reasons it would refuse it, or nil.
// old, when set, is the stored object for an update (transition rules).
func (a *Admission) Admit(obj, old runtime.Object) ([]string, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("admission: to unstructured: %w", err)
	}
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if kind == "" {
		kind = kindOf(obj)
	}
	s, err := a.load(kind)
	if err != nil {
		return nil, err
	}
	var oldU map[string]interface{}
	if old != nil {
		if oldU, err = runtime.DefaultUnstructuredConverter.ToUnstructured(old); err != nil {
			return nil, fmt.Errorf("admission: old to unstructured: %w", err)
		}
	}
	// status is a subresource: a create or update of the main resource
	// does not write it, and its rules are not checked here.
	delete(u, "status")
	if oldU != nil {
		delete(oldU, "status")
		// The API server defaults what it reads from etcd too: the old
		// object a transition rule sees is defaulted.
		pruning.Prune(oldU, s.structural, true)
		defaulting.PruneNonNullableNullsWithoutDefaults(oldU, s.structural)
		defaulting.Default(oldU, s.structural)
	}
	// The API server's order: prune unknown fields, drop nulls of
	// non-nullable fields, then default.
	pruning.Prune(u, s.structural, true)
	defaulting.PruneNonNullableNullsWithoutDefaults(u, s.structural)
	defaulting.Default(u, s.structural)

	var errs field.ErrorList
	errs = append(errs, apivalidation.ValidateCustomResource(nil, u, s.schema)...)
	if s.cel != nil {
		var oldObj interface{}
		if oldU != nil {
			oldObj = oldU
		}
		celErrs, _ := s.cel.Validate(context.Background(), nil, s.structural, u, oldObj, celconfig.RuntimeCELCostBudget)
		errs = append(errs, celErrs...)
	}
	// Write the defaults back into the typed object.
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u, obj); err != nil {
		return nil, fmt.Errorf("admission: from unstructured: %w", err)
	}
	if len(errs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out, nil
}

// Default prunes and defaults u, an object of kind as JSON-shaped data, in
// place, as the API server does before admission plugins see it. A kind
// without a CRD file is left as it is.
func (a *Admission) Default(kind string, u map[string]interface{}) error {
	if _, ok := crdFiles[kind]; !ok || u == nil {
		return nil
	}
	s, err := a.load(kind)
	if err != nil {
		return err
	}
	pruning.Prune(u, s.structural, true)
	defaulting.PruneNonNullableNullsWithoutDefaults(u, s.structural)
	defaulting.Default(u, s.structural)
	return nil
}

// kindOf is the Go type name of a typed object without TypeMeta.
func kindOf(obj runtime.Object) string {
	t := fmt.Sprintf("%T", obj)
	return t[strings.LastIndex(t, ".")+1:]
}
