// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func TestPlanRollback(t *testing.T) {
	healthFailed := func(s *v1alpha1.PromotionStep) *v1alpha1.PromotionStep {
		expiry := metav1.NewTime(t0)
		s.Status.HealthCheckExpiry = &expiry
		return s
	}
	configBundle := func(name string, minute int) *v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		b.Spec.Type = "config"
		b.Spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://g/x", CommitSHA: name}
		return b
	}
	other := bundle("other-v1", "other", "o1", 0)
	// rollbackOf builds a rollback Bundle of pipeline app in env that restored
	// tag after rolling back from the Bundle from.
	rollbackOf := func(name, from, env, tag string, minute int) *v1alpha1.Bundle {
		b := bundle(name, "app", tag, minute)
		b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
		b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: from}
		b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: env}
		return b
	}

	tests := []struct {
		name       string
		objs       []client.Object
		req        lifecycle.RollbackRequest
		wantTarget string
		wantFrom   string
		wantTag    string
		wantErr    error
	}{
		{
			name: "default target is the previous verified bundle, not the one deployed now",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
			},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "a step that failed its health check is what is deployed",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				step("v1", "app", "prod", "Verified", 1), healthFailed(step("v2", "app", "prod", "Failed", 11)),
			},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "a step whose PR is still open is not deployed",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "prod", "WaitingForMerge", 21),
			},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "a bundle with the deployed (failing) images is never the target",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v2-rebuild", "app", "2", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v2-rebuild", "app", "prod", "HealthChecking", 21),
			},
			wantTarget: "v1", wantFrom: "v2-rebuild", wantTag: "1",
		},
		{
			name: "a bundle verified in only one region is not a target",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				named(step("v1", "app", "prod", "Verified", 1), "v1-prod-eu"), named(step("v1", "app", "prod", "Verified", 1), "v1-prod-us"),
				named(step("v2", "app", "prod", "Verified", 11), "v2-prod-eu"), named(step("v2", "app", "prod", "Failed", 11), "v2-prod-us"),
				named(step("v3", "app", "prod", "Verified", 21), "v3-prod-eu"), named(step("v3", "app", "prod", "Verified", 21), "v3-prod-us"),
			},
			wantTarget: "v1", wantFrom: "v3", wantTag: "1",
		},
		{
			name: "a bundle of another type is skipped",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), configBundle("c1", 10), bundle("v2", "app", "2", 20),
				step("v1", "app", "prod", "Verified", 1), step("c1", "app", "prod", "Verified", 11), step("v2", "app", "prod", "Verified", 21),
			},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "a bundle pruned by historyLimit is skipped",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11), step("v3", "app", "prod", "Verified", 21),
			},
			wantTarget: "v1", wantFrom: "v3", wantTag: "1",
		},
		{
			name: "FromBundle names the failing bundle for automatic rollbacks",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Promoting", 11),
			},
			req:        lifecycle.RollbackRequest{FromBundle: "v2"},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "--to picks an explicit bundle and copies its images",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11), step("v3", "app", "prod", "Verified", 21),
			},
			req:        lifecycle.RollbackRequest{ToBundle: "v1"},
			wantTarget: "v1", wantFrom: "v3", wantTag: "1",
		},
		{
			name: "a bundle an earlier rollback rolled back from is not the target",
			objs: []client.Object{
				bundle("v0", "app", "0", 0), bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), step("r1", "app", "prod", "Verified", 31),
			},
			wantTarget: "v0", wantFrom: "r1", wantTag: "0",
		},
		{
			name: "after v2 was rolled back to v1, rolling back again never returns to v2",
			objs: []client.Object{
				bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v1", "app", "prod", "Verified", 11), step("v2", "app", "prod", "Verified", 21),
				step("r1", "app", "prod", "Verified", 31),
			},
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "a rollback in another environment does not exclude the bundle",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				rollbackOf("r-uat", "v2", "uat", "1", 15),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "prod", "Verified", 21),
			},
			wantTarget: "v2", wantFrom: "v3", wantTag: "2",
		},
		{
			name: "a verified rollback bundle is a target like any other",
			objs: []client.Object{
				bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20), bundle("v3", "app", "3", 40),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v1", "app", "prod", "Verified", 11), step("v2", "app", "prod", "Verified", 21),
				step("r1", "app", "prod", "Verified", 31), step("v3", "app", "prod", "Verified", 41),
			},
			wantTarget: "r1", wantFrom: "v3", wantTag: "1",
		},
		{
			name: "--to can name a bundle an earlier rollback rolled back from",
			objs: []client.Object{
				bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v1", "app", "prod", "Verified", 11), step("v2", "app", "prod", "Verified", 21),
				step("r1", "app", "prod", "Verified", 31),
			},
			req:        lifecycle.RollbackRequest{ToBundle: "v2"},
			wantTarget: "v2", wantFrom: "r1", wantTag: "2",
		},
		{
			name: "an automatic rollback of a bundle that is not a rollback goes ahead",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				step("v1", "app", "prod", "Verified", 1), healthFailed(step("v2", "app", "prod", "Failed", 11)),
			},
			req:        lifecycle.RollbackRequest{FromBundle: "v2", Automatic: true},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "an automatic rollback of a failing rollback is refused",
			objs: []client.Object{
				bundle("v0", "app", "0", 0), bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), healthFailed(step("r1", "app", "prod", "Failed", 31)),
			},
			req:     lifecycle.RollbackRequest{FromBundle: "r1", Automatic: true},
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "a manual rollback of a failing rollback is allowed",
			objs: []client.Object{
				bundle("v0", "app", "0", 0), bundle("v1", "app", "1", 10), bundle("v2", "app", "2", 20),
				rollbackOf("r1", "v2", "prod", "1", 30),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), healthFailed(step("r1", "app", "prod", "Failed", 31)),
			},
			req:        lifecycle.RollbackRequest{FromBundle: "r1"},
			wantTarget: "v0", wantFrom: "r1", wantTag: "0",
		},
		{
			name: "--to must have been Verified in the environment",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "uat", "Verified", 21),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v3"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name: "--to must have been Verified in every region of the environment",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				named(step("v1", "app", "prod", "Verified", 1), "v1-prod-eu"), named(step("v1", "app", "prod", "Failed", 1), "v1-prod-us"),
				named(step("v2", "app", "prod", "Verified", 11), "v2-prod-eu"), named(step("v2", "app", "prod", "Verified", 11), "v2-prod-us"),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v1"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name:    "--to must exist",
			objs:    []client.Object{bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)},
			req:     lifecycle.RollbackRequest{ToBundle: "typo"},
			wantErr: lifecycle.ErrNotFound,
		},
		{
			name:    "--to must belong to the pipeline",
			objs:    []client.Object{bundle("v1", "app", "1", 0), other, step("v1", "app", "prod", "Verified", 1)},
			req:     lifecycle.RollbackRequest{ToBundle: "other-v1"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name:    "--to must not be the deployed bundle",
			objs:    []client.Object{bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)},
			req:     lifecycle.RollbackRequest{ToBundle: "v1"},
			wantErr: lifecycle.ErrConflict,
		},
		{
			name:    "--to must carry artifacts",
			objs:    []client.Object{bundle("v1", "app", "1", 0), bundle("empty", "app", "", 5), step("v1", "app", "prod", "Verified", 1)},
			req:     lifecycle.RollbackRequest{ToBundle: "empty"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name:    "no earlier verified bundle is a conflict, not a no-op rollback",
			objs:    []client.Object{bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1)},
			wantErr: lifecycle.ErrConflict,
		},
		{
			name:    "nothing deployed yet",
			objs:    []client.Object{bundle("v1", "app", "1", 0), step("v1", "app", "test", "Verified", 1)},
			wantErr: lifecycle.ErrConflict,
		},
		{
			name:    "unknown environment",
			req:     lifecycle.RollbackRequest{Environment: "staging"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name:    "unknown pipeline",
			req:     lifecycle.RollbackRequest{Pipeline: "nope"},
			wantErr: lifecycle.ErrNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, append(tc.objs, pipeline("app", "test", "uat", "prod"))...)
			req := tc.req
			req.Namespace = ns
			if req.Pipeline == "" {
				req.Pipeline = "app"
			}
			if req.Environment == "" {
				req.Environment = "prod"
			}
			req.Actor = "alice"
			req.Now = t0.Add(time.Hour)

			plan, err := lifecycle.PlanRollback(context.Background(), c, req)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			b := plan.Bundle
			assert.Equal(t, tc.wantTarget, plan.Target.Name)
			assert.Equal(t, tc.wantTarget, b.Spec.Provenance.RollbackOf, "rollbackOf names the bundle restored")
			assert.Equal(t, tc.wantFrom, b.Annotations[lifecycle.AnnotationRollbackFrom])
			require.Len(t, b.Spec.Images, 1, "the rollback bundle carries the target's images")
			assert.Equal(t, tc.wantTag, b.Spec.Images[0].Tag)
			assert.Equal(t, "prod", b.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "app", b.Spec.Pipeline)
			assert.Equal(t, "true", b.Labels[lifecycle.LabelRollback])
			assert.Equal(t, "app", b.Labels[lifecycle.LabelPipeline])
			assert.Equal(t, "alice", b.Spec.Provenance.Author)
			assert.Equal(t, "sha-"+tc.wantTag, b.Spec.Provenance.CommitSHA)
			assert.Equal(t, "app-rollback-", b.GenerateName)
			assert.NotEmpty(t, b.Annotations[lifecycle.AnnotationCreatedAt])
		})
	}
}

func TestPlanRollback_NameReasonEmergency(t *testing.T) {
	c := newClient(t, pipeline("app", "prod"),
		bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
		step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "HealthChecking", 11))
	plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", FromBundle: "v2",
		Name: "v2-rollback-alarm", Reason: "AutoRollback", Emergency: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "v2-rollback-alarm", plan.Bundle.Name)
	assert.Empty(t, plan.Bundle.GenerateName)
	assert.Equal(t, "AutoRollback", plan.Bundle.Labels[lifecycle.LabelReason])
	assert.Equal(t, "true", plan.Bundle.Labels[lifecycle.LabelEmergency])
	assert.Empty(t, plan.Bundle.Annotations[lifecycle.AnnotationCreatedAt], "zero Now stamps nothing")
	assert.Equal(t, "ci", plan.Bundle.Spec.Provenance.Author, "without an actor the target's author is kept")
}
