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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildDeleteBundleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestDeleteBundle_Success verifies that a bundle is deleted and output is correct.
func TestDeleteBundle_Success(t *testing.T) {
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pipeline-abc123",
			Namespace: "default",
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "my-pipeline",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildDeleteBundleScheme(t)).
		WithObjects(bundle).
		Build()

	var buf bytes.Buffer
	err := deleteBundleFn(&buf, c, "default", "my-pipeline-abc123")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "my-pipeline-abc123")
	assert.Contains(t, buf.String(), "deleted")

	// Verify bundle is gone
	got := &v1alpha1.Bundle{}
	err = c.Get(context.Background(), types.NamespacedName{Name: "my-pipeline-abc123", Namespace: "default"}, got)
	assert.Error(t, err, "bundle should be deleted from cluster")
}

// TestDeleteBundle_NotFound verifies error when bundle does not exist.
func TestDeleteBundle_NotFound(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(buildDeleteBundleScheme(t)).
		Build()

	var buf bytes.Buffer
	err := deleteBundleFn(&buf, c, "default", "nonexistent-bundle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestDeleteBundleCmd_ArgsRequired verifies the command requires exactly one arg.
func TestDeleteBundleCmd_ArgsRequired(t *testing.T) {
	cmd := newDeleteBundleCmd()
	assert.Equal(t, "bundle <name>", cmd.Use)
	assert.NotNil(t, cmd.Args, "cobra.ExactArgs(1) should be set")
}
