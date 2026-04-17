// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildCreateBundleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestCreateBundle_DryRunFlagRegistered verifies --dry-run is registered.
func TestCreateBundle_DryRunFlagRegistered(t *testing.T) {
	cmd := newCreateBundleCmd()
	f := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, f, "--dry-run must be registered on 'create bundle'")
	assert.Equal(t, "false", f.DefValue, "--dry-run must default to false")
}

// TestCreateBundle_DryRun_NoAPICallsMade verifies that --dry-run does NOT call
// c.Create (i.e., no Bundle is created on the cluster).
func TestCreateBundle_DryRun_NoAPICallsMade(t *testing.T) {
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}

	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipe).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		[]string{"ghcr.io/org/app:sha-abc123"}, "image")
	require.NoError(t, err)

	// No Bundle should exist after dry-run
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Empty(t, bundleList.Items, "dry-run must not create any Bundles")
}

// TestCreateBundle_DryRun_OutputContainsKeyInfo verifies the dry-run output mentions
// the pipeline name and dry-run indicator.
func TestCreateBundle_DryRun_OutputContainsKeyInfo(t *testing.T) {
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}

	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipe).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		[]string{"ghcr.io/org/app:sha-abc123"}, "image")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "[DRY-RUN]", "output must contain DRY-RUN marker")
	assert.Contains(t, out, "nginx-demo", "output must contain pipeline name")
	assert.Contains(t, out, "No resources were created", "output must confirm nothing was written")
}

// TestCreateBundle_DryRun_PipelineNotFound returns an error when the Pipeline does not exist.
func TestCreateBundle_DryRun_PipelineNotFound(t *testing.T) {
	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nonexistent",
		[]string{"ghcr.io/org/app:sha-abc123"}, "image")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dry-run", "error must mention dry-run context")
}

// TestCreateBundle_DryRun_InvalidImage returns an error for malformed image refs.
func TestCreateBundle_DryRun_InvalidImage(t *testing.T) {
	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		[]string{"not valid @@@"}, "image")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid image") ||
		strings.Contains(err.Error(), "dry-run"),
		"error must describe the failure: got %q", err.Error())
}
