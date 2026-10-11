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
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var updateBundleRef = flag.Bool("update-bundle-ref", false, "rewrite testdata/bundle-ref.golden")

// refBundle is a Bundle of pipeline shipping images (repo:tag pairs), created
// minutes ago.
func refBundle(name, pipeline, phase string, minutes int, tags ...string) *v1alpha1.Bundle {
	b := explainBundle(name, phase, time.Now().Add(-time.Duration(minutes)*time.Minute))
	b.Spec.Pipeline = pipeline
	for i, tag := range tags {
		b.Spec.Images = append(b.Spec.Images, v1alpha1.ImageRef{Repository: fmt.Sprintf("ghcr.io/acme/app%d", i), Tag: tag})
	}
	return b
}

// TestResolveBundleRef_Golden (CLI UX 3): a Bundle name, else a version or
// image tag, names one Bundle; none or several is an error that lists the
// candidates, newest first.
//
// Covers CLI-VERSIONREF-01.
func TestResolveBundleRef_Golden(t *testing.T) {
	c := policyClient(t,
		refBundle("demo-a", "demo", "Verified", 50, "1.4.0"),
		refBundle("demo-b", "demo", "Superseded", 40, "1.5.0"),
		refBundle("demo-c", "demo", "Promoting", 30, "1.5.0"),
		refBundle("demo-d", "demo", "Verified", 20, "2.0.0", "sidecar-7"),
		refBundle("web-a", "web", "Verified", 10, "1.4.0"),
	)
	cases := []struct{ pipeline, ref string }{
		{"", "demo-a"},
		{"demo", "1.4.0"},
		{"", "1.4.0"},
		{"demo", "1.5.0"},
		{"demo", "sidecar-7"},
		{"demo", "3.0.0"},
		{"other", "1.4.0"},
	}
	var golden strings.Builder
	for _, tc := range cases {
		got, err := resolveBundleRef(context.Background(), c, "default", tc.pipeline, tc.ref)
		if err != nil {
			require.True(t, errors.Is(err, errBundleRef), err)
			got = "error: " + err.Error()
		}
		fmt.Fprintf(&golden, "pipeline=%q ref=%q\n  %s\n", tc.pipeline, tc.ref, got)
	}
	path := filepath.Join("testdata", "bundle-ref.golden")
	if *updateBundleRef {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())
}
