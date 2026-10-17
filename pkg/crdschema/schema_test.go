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

package crdschema_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	crdbases "github.com/kardinal-promoter/kardinal-promoter/config/crd/bases"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/crdschema"
)

// TestLoad_EmbedsEveryCRD (#1287): the CLI's embedded CRDs are exactly the
// generated ones in config/crd/bases (which the chart's crds/ copies), and
// each gives its kind a schema.
//
// Covers CLI-VALIDATE-SCHEMA-01.
func TestLoad_EmbedsEveryCRD(t *testing.T) {
	onDisk, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	require.NoError(t, err)
	embedded, err := fs.Glob(crdbases.FS, "*.yaml")
	require.NoError(t, err)
	require.Len(t, embedded, len(onDisk))
	for _, f := range onDisk {
		want, err := os.ReadFile(f)
		require.NoError(t, err)
		got, err := fs.ReadFile(crdbases.FS, filepath.Base(f))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), f)
	}
	schemas, err := crdschema.Load(crdbases.FS, "v1alpha1")
	require.NoError(t, err)
	assert.Len(t, schemas, len(onDisk), "one kind per CRD")
	for _, kind := range []string{"Pipeline", "PolicyGate", "Bundle", "Subscription", "ScmProvider"} {
		assert.Contains(t, schemas, kind)
	}
	none, err := crdschema.Load(crdbases.FS, "v9")
	require.NoError(t, err)
	assert.Empty(t, none, "only the version asked for")
}

// TestSchema_Check (#1287): the dropped fields with their paths, and the
// schema's errors on what is left; the document itself is not changed.
//
// Covers CLI-VALIDATE-SCHEMA-01.
func TestSchema_Check(t *testing.T) {
	schemas, err := crdschema.Load(crdbases.FS, "v1alpha1")
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(`apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: {name: web, labels: {a: b}}
spec:
  git: {url: https://github.com/o/r}
  policyGates: [x]
  environments:
  - {name: test, approval: manual, approvalMode: pr-review}
`), &obj))
	unknown, invalid, err := schemas["Pipeline"].Check(obj)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"spec.policyGates", "spec.environments[0].approvalMode"}, unknown)
	require.Len(t, invalid, 1)
	assert.Equal(t, "spec.environments[0].approval", invalid[0].Field)
	assert.Contains(t, obj["spec"], "policyGates", "the caller's document is not pruned")

	require.NoError(t, yaml.Unmarshal([]byte("apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata: {name: web}\n"+
		"spec: {git: {url: https://github.com/o/r}, environments: [{name: test}]}\n"), &obj))
	unknown, invalid, err = schemas["Pipeline"].Check(obj)
	require.NoError(t, err)
	assert.Empty(t, unknown)
	assert.Empty(t, invalid)
}
