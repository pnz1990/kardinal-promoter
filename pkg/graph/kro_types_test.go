// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"sigs.k8s.io/yaml"
)

// crdDeclType is the CEL type of one object of the kardinal CRD in file
// (config/crd/bases), as kro's compiler types a node of that kind: the
// CRD's schema plus the ObjectMeta fields Graph expressions read.
func crdDeclType(t *testing.T, file string) *apiservercel.DeclType {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", file))
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	var internal apiextensions.JSONSchemaProps
	require.NoError(t, apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil))
	s, err := structuralschema.NewStructural(&internal)
	require.NoError(t, err)
	str := structuralschema.Structural{Generic: structuralschema.Generic{Type: "string"}}
	strMap := structuralschema.Structural{Generic: structuralschema.Generic{Type: "object"},
		AdditionalProperties: &structuralschema.StructuralOrBool{Structural: &str}}
	s.Properties["metadata"] = structuralschema.Structural{Generic: structuralschema.Generic{Type: "object"},
		Properties: map[string]structuralschema.Structural{"name": str, "namespace": str, "labels": strMap, "annotations": strMap}}
	dt := model.SchemaDeclType(s, false)
	require.NotNil(t, dt)
	return dt
}

// checkKroTypes type-checks expr (without ${}) the way kro's compiler does
// for collection nodes: each name in collections is a list of the CRD's
// object type, named __type_<name> (its items __type_<name>.@idx), so two
// collections are lists of different types. dyn names are untyped.
func checkKroTypes(t *testing.T, expr string, collections map[string]string, dyn ...string) error {
	t.Helper()
	opts := []cel.EnvOption{ext.Lists(), ext.Strings(), cel.OptionalTypes()}
	var lists []*apiservercel.DeclType
	for name, file := range collections {
		l := apiservercel.NewListType(crdDeclType(t, file), -1).MaybeAssignTypeName("__type_" + name)
		lists = append(lists, l)
		opts = append(opts, cel.Variable(name, l.CelType()))
	}
	provider := apiservercel.NewDeclTypeProvider(lists...)
	provider.SetRecognizeKeywordAsFieldName(true)
	wrapped, err := provider.WithTypeProvider(types.NewEmptyRegistry())
	require.NoError(t, err)
	opts = append(opts, cel.CustomTypeProvider(wrapped))
	for _, name := range dyn {
		opts = append(opts, cel.Variable(name, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	require.NoError(t, err)
	_, iss := env.Compile(expr)
	return iss.Err()
}
