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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPromote_CreatesBundleForEnvironment verifies that promoteFn creates a Bundle
// targeting the specified environment.
func TestPromote_CreatesBundleForEnvironment(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	err := promoteFn(&buf, c, "default", "nginx-demo", "prod")
	require.NoError(t, err)

	// Verify Bundle was created
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, "nginx-demo", bundles.Items[0].Spec.Pipeline)
	require.NotNil(t, bundles.Items[0].Spec.Intent, "Intent should not be nil")
	assert.Equal(t, "prod", bundles.Items[0].Spec.Intent.TargetEnvironment)

	out := buf.String()
	assert.Contains(t, out, "nginx-demo")
	assert.Contains(t, out, "prod")
}

// TestPromote_PipelineNotFound returns an error when the pipeline does not exist.
func TestPromote_PipelineNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := promoteFn(&buf, c, "default", "no-such-pipeline", "prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-pipeline")
}

// TestPromote_EnvironmentNotInPipeline returns an error when the env is not in the pipeline.
func TestPromote_EnvironmentNotInPipeline(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	err := promoteFn(&buf, c, "default", "nginx-demo", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "not found")
}
