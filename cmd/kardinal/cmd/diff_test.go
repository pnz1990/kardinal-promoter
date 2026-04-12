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

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestDiff_ShowsImageDifferences verifies that diffFn outputs image tag differences.
func TestDiff_ShowsImageDifferences(t *testing.T) {
	s := cliTestScheme(t)
	bA := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-v1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/myorg/my-app", Tag: "1.28.0", Digest: "sha256:def456abcdef456"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "def456abcdef456",
				Author:    "dependabot[bot]",
			},
		},
	}
	bB := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-v2", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/myorg/my-app", Tag: "1.29.0", Digest: "sha256:abc123xyz789abc"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "abc123xyz789abc",
				Author:    "engineer-name",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(bA, bB).Build()

	var buf bytes.Buffer
	err := diffFn(&buf, c, "default", "bundle-v1", "bundle-v2")
	require.NoError(t, err)

	out := buf.String()
	// Headers
	assert.Contains(t, out, "ARTIFACT")
	assert.Contains(t, out, "bundle-v1")
	assert.Contains(t, out, "bundle-v2")
	// Image row
	assert.Contains(t, out, "ghcr.io/myorg/my-app")
	assert.Contains(t, out, "1.28.0")
	assert.Contains(t, out, "1.29.0")
	// Digest sub-row (truncated)
	assert.Contains(t, out, "digest")
	assert.Contains(t, out, "sha256:def456ab...")
	assert.Contains(t, out, "sha256:abc123xy...")
	// Commit + author
	assert.Contains(t, out, "commit")
	assert.Contains(t, out, "author")
	assert.Contains(t, out, "dependabot[bot]")
	assert.Contains(t, out, "engineer-name")
}

// TestDiff_NoBundleReturnsError verifies that diffFn returns an error for missing bundles.
func TestDiff_NoBundleReturnsError(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := diffFn(&buf, c, "default", "missing-a", "missing-b")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing-a")
}

// TestFormatDiffTable_SameImagesShowsNoChanges verifies diff output when both bundles
// have the same image (no change to highlight, but table should still render).
func TestFormatDiffTable_SameImages(t *testing.T) {
	bA := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-a"},
		Spec: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "nginx", Tag: "1.25"},
			},
		},
	}
	bB := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-b"},
		Spec: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "nginx", Tag: "1.25"},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, FormatDiffTable(&buf, bA, bB))

	out := buf.String()
	assert.Contains(t, out, "nginx")
	assert.Contains(t, out, "1.25")
}

// TestFormatDiffTable_ImageAbsentInB verifies that an image absent in bundle-b shows "(absent)".
func TestFormatDiffTable_ImageAbsentInB(t *testing.T) {
	bA := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-a"},
		Spec: v1alpha1.BundleSpec{
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/removed/svc", Tag: "1.0.0"},
			},
		},
	}
	bB := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-b"},
		Spec:       v1alpha1.BundleSpec{},
	}

	var buf bytes.Buffer
	require.NoError(t, FormatDiffTable(&buf, bA, bB))

	out := buf.String()
	assert.Contains(t, out, "ghcr.io/removed/svc")
	assert.Contains(t, out, "1.0.0")
	assert.Contains(t, out, "(absent)")
}

// TestTruncDigest verifies digest truncation.
func TestTruncDigest(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"sha256:abc123456789012345", "sha256:abc12345..."},
		{"sha256:abc", "sha256:abc"},
		{"", ""},
	}
	for _, tc := range cases {
		got := truncDigest(tc.input)
		assert.Equal(t, tc.expected, got, "input: %q", tc.input)
	}
}
