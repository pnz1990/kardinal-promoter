// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// #2163: kardinal rollback says when the rollback is promoted through the
// environments upstream of its target because its images are tags only,
// and how to change the target alone. With images pinned by digest it says
// nothing: the controller then carries the upstream verification, if there
// is one, and changes only the target. A fleet target's sibling targets are
// not upstream of it.
//
// Covers RB-MULTI-03.
func TestRollback_SaysWhenTagsPromoteThroughUpstream(t *testing.T) {
	pinned := func(b *v1alpha1.Bundle) *v1alpha1.Bundle {
		b.Spec.Images[0].Digest = "sha256:" + string(bytes.Repeat([]byte(b.Spec.Images[0].Tag), 64))
		return b
	}
	history := func(pin bool, p *v1alpha1.Pipeline, env string) []client.Object {
		v1, v2 := lcBundle("app-v1", "app", "1", 0), lcBundle("app-v2", "app", "2", 10)
		if pin {
			v1, v2 = pinned(v1), pinned(v2)
		}
		return []client.Object{p, v1, v2, lcStep("app-v1", env, "Verified", 5), lcStep("app-v2", env, "Verified", 15)}
	}
	fleet := lcPipeline()
	fleet.Spec.Environments[2].Fleet = &v1alpha1.FleetSpec{MaxConcurrent: 1,
		Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}

	for _, tc := range []struct {
		name, env, want string
		pin             bool
		pipeline        *v1alpha1.Pipeline
	}{
		{name: "tags", env: "prod", pipeline: lcPipeline(),
			want: "Note: ghcr.io/org/app:1 not pinned by digest, so the rollback is promoted through test, uat first"},
		{name: "pinned", env: "prod", pipeline: lcPipeline(), pin: true},
		{name: "a fleet target, tags", env: "prod-us", pipeline: fleet,
			want: "promoted through test, uat first: a tag can be pushed again with other bytes. To change only prod-us,"},
		{name: "a fleet target, pinned", env: "prod-us", pipeline: fleet, pin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lcClient(t, history(tc.pin, tc.pipeline, tc.env)...)
			var buf bytes.Buffer
			require.NoError(t, rollbackFn(&buf, c, "default", "app", tc.env, ""))
			if tc.want == "" {
				assert.NotContains(t, buf.String(), "Note:")
				return
			}
			assert.Contains(t, buf.String(), tc.want)
			assert.NotContains(t, buf.String(), "prod-eu", "a sibling target is not upstream")
		})
	}
}
