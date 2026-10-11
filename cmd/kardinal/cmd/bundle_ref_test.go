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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	chart := refBundle("demo-chart", "demo", "Verified", 15)
	chart.Spec.Type, chart.Spec.Chart = "chart", &v1alpha1.ChartRef{Name: "mychart", Version: "7.7.7"}
	cfg := refBundle("demo-cfg", "demo", "Verified", 12)
	cfg.Spec.Type, cfg.Spec.ConfigRef = "config", &v1alpha1.ConfigRef{CommitSHA: "abc1234def5678abc1234def5678abc1234def56"}
	other := refBundle("x-a", "x", "Verified", 5, "1.4.0")
	other.Namespace = "team-b"
	c := policyClient(t,
		refBundle("demo-a", "demo", "Verified", 50, "1.4.0", "web-a"),
		refBundle("demo-b", "demo", "Superseded", 40, "1.5.0"),
		refBundle("demo-c", "demo", "Promoting", 30, "1.5.0"),
		refBundle("demo-d", "demo", "Verified", 20, "2.0.0", "sidecar-7"),
		refBundle("web-a", "web", "Verified", 10, "1.4.0"),
		chart, cfg, other,
	)
	cases := []refScope{
		{ns: "default"}, {ns: "default", pipeline: "demo"}, {ns: "default"}, {ns: "default", pipeline: "demo"},
		{ns: "default", pipeline: "demo"}, {ns: "default", pipeline: "demo"}, {ns: "default", pipeline: "other"},
		{ns: "default"}, {ns: "default", strict: true}, {ns: "default", pipeline: "demo"},
		{ns: "default", pipeline: "demo"}, {ns: "default", pipeline: "demo"}, {ns: ""}, {ns: ""},
		// --name: the Bundle of that name, never a version.
		{ns: "default", strict: true, exact: true}, {ns: "default", strict: true, exact: true},
	}
	refs := []string{"demo-a", "1.4.0", "1.4.0", "1.5.0", "sidecar-7", "3.0.0", "1.4.0",
		"web-a", "web-a", "web-a", "7.7.7", "abc1234", "x-a", "1.4.0", "web-a", "1.4.0"}
	var golden strings.Builder
	for i, sc := range cases {
		got, ns, err := resolveBundleRefIn(context.Background(), c, sc, refs[i])
		if err != nil {
			require.True(t, errors.Is(err, errBundleRef), err)
			got = "error: " + err.Error()
		} else {
			got = ns + "/" + got
		}
		fmt.Fprintf(&golden, "ns=%q pipeline=%q strict=%v exact=%v ref=%q\n  %s\n", sc.ns, sc.pipeline, sc.strict, sc.exact, refs[i], got)
	}

	// May get but not list Bundles: a missing name is not found, not
	// "forbidden".
	forbidden := fake.NewClientBuilder().WithScheme(rootScheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectList, ...sigs_client.ListOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, "", errors.New("no list"))
		},
	}).Build()
	_, _, err := resolveBundleRefIn(context.Background(), forbidden, refScope{ns: "default"}, "nope")
	require.Error(t, err)
	fmt.Fprintf(&golden, "list forbidden, ref=\"nope\"\n  error: %s\n", err)

	// Strict, and the List fails: a name that may also be a version is
	// refused, not guessed.
	getOnly := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(refBundle("web-a", "web", "Verified", 10, "1.4.0")).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectList, ...sigs_client.ListOption) error {
				return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, "", errors.New("no list"))
			},
		}).Build()
	_, _, err = resolveBundleRefIn(context.Background(), getOnly, refScope{ns: "default", strict: true}, "web-a")
	require.ErrorIs(t, err, errBundleRef)
	fmt.Fprintf(&golden, "strict, list forbidden, ref=\"web-a\"\n  error: %s\n", err)
	got, _, err := resolveBundleRefIn(context.Background(), getOnly, refScope{ns: "default", strict: true, exact: true}, "web-a")
	require.NoError(t, err)
	fmt.Fprintf(&golden, "strict, list forbidden, --name web-a\n  default/%s\n", got)
	path := filepath.Join("testdata", "bundle-ref.golden")
	if *updateBundleRef {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())
}
