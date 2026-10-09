// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"slices"
	"strings"
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
			name: "a rejected bundle is never the target, also when it was Verified (#1451)",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), rejected(bundle("v2", "app", "2", 10)), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "prod", "Verified", 21),
			},
			wantTarget: "v1", wantFrom: "v3", wantTag: "1",
		},
		{
			name: "rolling back from a rejected bundle that reached the environment",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), rejected(bundle("v2", "app", "2", 10)),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
			},
			wantTarget: "v1", wantFrom: "v2", wantTag: "1",
		},
		{
			name: "a bundle carrying the image of a rejected bundle is never the target",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), rejected(bundle("v2", "app", "2", 10)), bundle("v2b", "app", "2", 20),
				bundle("v4", "app", "4", 30),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v2b", "app", "prod", "Verified", 21), step("v4", "app", "prod", "Verified", 31),
			},
			wantTarget: "v1", wantFrom: "v4", wantTag: "1",
		},
		{
			name: "a moving tag: the rejected digest does not block the same tag with another digest (QA #1489)",
			objs: []client.Object{
				digest(bundle("v1", "app", "latest", 0), "sha256:fixed"),
				rejected(digest(bundle("v2", "app", "latest", 10), "sha256:bad")), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "prod", "Verified", 21),
			},
			wantTarget: "v1", wantFrom: "v3", wantTag: "latest",
		},
		{
			name: "--to a bundle carrying the image of a rejected bundle is refused",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), rejected(bundle("v2", "app", "2", 10)), bundle("v2b", "app", "2", 20),
				bundle("v4", "app", "4", 30),
				step("v1", "app", "prod", "Verified", 1), step("v2b", "app", "prod", "Verified", 21),
				step("v4", "app", "prod", "Verified", 31),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v2b"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name: "--to a rejected bundle is refused",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), rejected(bundle("v2", "app", "2", 10)), bundle("v3", "app", "3", 20),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
				step("v3", "app", "prod", "Verified", 21),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v2"},
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name: "only rejected bundles before the deployed one: nothing to roll back to",
			objs: []client.Object{
				phase(rejected(bundle("v1", "app", "1", 0)), "Verified"), bundle("v2", "app", "2", 10),
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
			},
			wantErr: lifecycle.ErrConflict,
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
			assert.Equal(t, "ci", b.Spec.Provenance.Author,
				"provenance keeps the author of the restored build, not the actor")
			assert.Equal(t, "alice", b.Annotations[lifecycle.AnnotationRequestedBy],
				"the actor is recorded as who asked for the rollback")
			assert.Equal(t, "sha-"+tc.wantTag, b.Spec.Provenance.CommitSHA)
			assert.Equal(t, "app-rollback-", b.GenerateName)
			assert.NotEmpty(t, b.Annotations[lifecycle.AnnotationCreatedAt])
		})
	}
}

// TestPlanRollback_NameReason also pins #1288: the rollback Bundle carries no
// kardinal.io/emergency label, which nothing read.
func TestPlanRollback_NameReason(t *testing.T) {
	c := newClient(t, pipeline("app", "prod"),
		bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
		step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "HealthChecking", 11))
	plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", FromBundle: "v2",
		Name: "v2-rollback-alarm", Reason: "AutoRollback",
	})
	require.NoError(t, err)
	assert.Equal(t, "v2-rollback-alarm", plan.Bundle.Name)
	assert.Empty(t, plan.Bundle.GenerateName)
	assert.Equal(t, "AutoRollback", plan.Bundle.Labels[lifecycle.LabelReason])
	assert.Equal(t, map[string]string{
		lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app", lifecycle.LabelReason: "AutoRollback",
	}, plan.Bundle.Labels, "no kardinal.io/emergency label (#1288)")
	assert.Empty(t, plan.Bundle.Annotations[lifecycle.AnnotationCreatedAt], "zero Now stamps nothing")
	assert.Equal(t, "ci", plan.Bundle.Spec.Provenance.Author, "without an actor the target's author is kept")
	assert.NotContains(t, plan.Bundle.Annotations, lifecycle.AnnotationRequestedBy, "no actor, no requested-by")
}

// TestPlanRollback_RestoresEveryImageTheDeployedBundleChanged is #1315: the
// rollback Bundle must put back every image the deployed Bundle names, not
// only the target's. An image the target does not name comes from the newest
// Bundle, other than the deployed one, that was Verified in the environment.
// When there is none the rollback is refused, naming the image, instead of
// leaving the failing version in place.
func TestPlanRollback_RestoresEveryImageTheDeployedBundleChanged(t *testing.T) {
	// imgs builds a Verified image Bundle of pipeline app with images
	// "repo:tag" under ghcr.io/x/.
	imgs := func(name string, minute int, refs ...string) *v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		for _, r := range refs {
			repo, tag, _ := strings.Cut(r, ":")
			b.Spec.Images = append(b.Spec.Images, v1alpha1.ImageRef{Repository: "ghcr.io/x/" + repo, Tag: tag})
		}
		return b
	}
	cfg := func(name string, minute int) *v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		b.Spec.Type = "config"
		b.Spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://g/cfg", CommitSHA: "commit-" + name}
		return b
	}
	healthFailed := func(s *v1alpha1.PromotionStep) *v1alpha1.PromotionStep {
		expiry := metav1.NewTime(t0)
		s.Status.HealthCheckExpiry = &expiry
		return s
	}
	rolledBackFrom := func(b *v1alpha1.Bundle, from string) *v1alpha1.Bundle {
		b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
		b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: from}
		b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
		return b
	}
	images := func(b *v1alpha1.Bundle) []string {
		out := make([]string, 0, len(b.Spec.Images))
		for _, img := range b.Spec.Images {
			out = append(out, strings.TrimPrefix(img.Repository, "ghcr.io/x/")+":"+img.Tag)
		}
		slices.Sort(out)
		return out
	}

	tests := []struct {
		name       string
		objs       []client.Object
		req        lifecycle.RollbackRequest
		wantTarget string
		wantImages []string
		wantConfig string
		wantErr    error
		errHas     string
	}{
		{
			name: "an image the target does not name comes from the newest earlier verified bundle",
			objs: []client.Object{
				imgs("v0", 0, "a:0", "b:0"), imgs("v1", 10, "a:1"), imgs("v2", 20, "b:2"),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				healthFailed(step("v2", "app", "prod", "Failed", 21)),
			},
			wantTarget: "v1", wantImages: []string{"a:1", "b:0"},
		},
		{
			name: "no earlier version of an image the deployed bundle changed: refuse, naming it",
			objs: []client.Object{
				imgs("v1", 10, "a:1"), imgs("v2", 20, "b:2"),
				step("v1", "app", "prod", "Verified", 11), healthFailed(step("v2", "app", "prod", "Failed", 21)),
			},
			wantErr: lifecycle.ErrConflict, errHas: "ghcr.io/x/b",
		},
		{
			name: "an automatic rollback is refused the same way",
			objs: []client.Object{
				imgs("v1", 10, "a:1"), imgs("v2", 20, "b:2"),
				step("v1", "app", "prod", "Verified", 11), healthFailed(step("v2", "app", "prod", "Failed", 21)),
			},
			req:     lifecycle.RollbackRequest{FromBundle: "v2", Automatic: true},
			wantErr: lifecycle.ErrConflict, errHas: "ghcr.io/x/b",
		},
		{
			name: "--to fills the images it does not name the same way",
			objs: []client.Object{
				imgs("v0", 0, "a:0", "b:0"), imgs("v1", 10, "a:1"), imgs("v2", 20, "a:2"), imgs("v3", 30, "b:3"),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), step("v3", "app", "prod", "HealthChecking", 31),
			},
			req:        lifecycle.RollbackRequest{ToBundle: "v1"},
			wantTarget: "v1", wantImages: []string{"a:1", "b:0"},
		},
		{
			name: "images the deployed bundle does not name are left to the target",
			objs: []client.Object{
				imgs("v1", 10, "a:1", "c:1"), imgs("v2", 20, "a:2"),
				step("v1", "app", "prod", "Verified", 11), step("v2", "app", "prod", "Verified", 21),
			},
			wantTarget: "v1", wantImages: []string{"a:1", "c:1"},
		},
		{
			name: "a bundle an earlier rollback rolled back from is not a source",
			objs: []client.Object{
				imgs("v0", 0, "a:0", "b:0"), imgs("v1", 10, "a:1"), imgs("v2", 20, "b:2"),
				rolledBackFrom(imgs("r1", 30, "a:1"), "v2"), imgs("v3", 40, "b:3"),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), step("r1", "app", "prod", "Verified", 31),
				healthFailed(step("v3", "app", "prod", "Failed", 41)),
			},
			wantTarget: "r1", wantImages: []string{"a:1", "b:0"},
		},
		{
			name: "a target whose filled image set is what runs now is skipped",
			objs: []client.Object{
				imgs("v0", 0, "a:0", "b:0"), imgs("v1", 10, "b:2"), imgs("v2", 20, "a:1"), imgs("v3", 30, "a:1", "b:2"),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), step("v3", "app", "prod", "HealthChecking", 31),
			},
			wantTarget: "v0", wantImages: []string{"a:0", "b:0"},
		},
		{
			name: "--to whose filled image set is what runs now is a conflict",
			objs: []client.Object{
				imgs("v0", 0, "a:0", "b:0"), imgs("v1", 10, "b:2"), imgs("v2", 20, "a:1"), imgs("v3", 30, "a:1", "b:2"),
				step("v0", "app", "prod", "Verified", 1), step("v1", "app", "prod", "Verified", 11),
				step("v2", "app", "prod", "Verified", 21), step("v3", "app", "prod", "HealthChecking", 31),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v2"},
			wantErr: lifecycle.ErrConflict, errHas: "same artifacts",
		},
		{
			name: "a config bundle is not a source of images",
			objs: []client.Object{
				imgs("v1", 10, "a:1"), func() *v1alpha1.Bundle {
					b := cfg("c1", 15)
					b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/x/b", Tag: "1"}}
					return b
				}(), imgs("v2", 20, "b:2"),
				step("v1", "app", "prod", "Verified", 11), step("c1", "app", "prod", "Verified", 16),
				healthFailed(step("v2", "app", "prod", "Failed", 21)),
			},
			wantErr: lifecycle.ErrConflict, errHas: "ghcr.io/x/b",
		},
		{
			name: "--to a config bundle cannot restore the images an image bundle changed",
			objs: []client.Object{
				cfg("c1", 5), imgs("v1", 10, "a:1"), imgs("v2", 20, "a:2"),
				step("c1", "app", "prod", "Verified", 6), step("v1", "app", "prod", "Verified", 11),
				healthFailed(step("v2", "app", "prod", "Failed", 21)),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "c1"},
			wantErr: lifecycle.ErrInvalid, errHas: "ghcr.io/x/a",
		},
		{
			name: "--to an image bundle cannot restore the config commit a config bundle changed",
			objs: []client.Object{
				imgs("v1", 5, "a:1"), cfg("c1", 10), cfg("c2", 20),
				step("v1", "app", "prod", "Verified", 6), step("c1", "app", "prod", "Verified", 11),
				healthFailed(step("c2", "app", "prod", "Failed", 21)),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "v1"},
			wantErr: lifecycle.ErrInvalid, errHas: "https://g/cfg",
		},
		{
			name: "a config target without a commit gets the newest earlier verified config commit",
			objs: []client.Object{
				cfg("c0", 0), func() *v1alpha1.Bundle {
					// Made with kubectl: a config Bundle with only an image.
					b := cfg("c1", 10)
					b.Spec.ConfigRef = nil
					b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/x/a", Tag: "1"}}
					return b
				}(), cfg("c2", 20),
				step("c0", "app", "prod", "Verified", 1), step("c1", "app", "prod", "Verified", 11),
				healthFailed(step("c2", "app", "prod", "Failed", 21)),
			},
			req:        lifecycle.RollbackRequest{ToBundle: "c1"},
			wantTarget: "c1", wantImages: []string{"a:1"}, wantConfig: "commit-c0",
		},
		{
			name: "a config target without a commit and no earlier config commit: refuse, naming the repo",
			objs: []client.Object{
				func() *v1alpha1.Bundle {
					b := cfg("c1", 10)
					b.Spec.ConfigRef = nil
					b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/x/a", Tag: "1"}}
					return b
				}(), cfg("c2", 20),
				step("c1", "app", "prod", "Verified", 11), healthFailed(step("c2", "app", "prod", "Failed", 21)),
			},
			req:     lifecycle.RollbackRequest{ToBundle: "c1"},
			wantErr: lifecycle.ErrConflict, errHas: "https://g/cfg",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, append(tc.objs, pipeline("app", "test", "uat", "prod"))...)
			req := tc.req
			req.Namespace, req.Pipeline, req.Environment = ns, "app", "prod"
			plan, err := lifecycle.PlanRollback(context.Background(), c, req)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Contains(t, err.Error(), tc.errHas)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTarget, plan.Target.Name)
			assert.Equal(t, tc.wantTarget, plan.Bundle.Spec.Provenance.RollbackOf)
			assert.Equal(t, tc.wantImages, images(plan.Bundle))
			if tc.wantConfig != "" {
				require.NotNil(t, plan.Bundle.Spec.ConfigRef)
				assert.Equal(t, tc.wantConfig, plan.Bundle.Spec.ConfigRef.CommitSHA)
			}
			assert.Equal(t, plan.Target.Spec.Type, plan.Bundle.Spec.Type, "the rollback keeps the target's type")
		})
	}
}

// TestPlanRollback_ToHintOnlyForPeople: the "pick one with --to" hint is CLI
// wording. An automatic rollback (RollbackPolicy, onHealthFailure=rollback)
// shows the refusal in a status condition or Event, where it does not apply.
func TestPlanRollback_ToHintOnlyForPeople(t *testing.T) {
	for _, tc := range []struct {
		name      string
		automatic bool
		wantHint  bool
	}{
		{name: "kardinal rollback", wantHint: true},
		{name: "automatic rollback", automatic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1),
				pipeline("app", "test", "uat", "prod"))
			_, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
				Namespace: ns, Pipeline: "app", Environment: "prod", FromBundle: "v1",
				Automatic: tc.automatic, Actor: "alice", Now: t0.Add(time.Hour),
			})
			require.ErrorIs(t, err, lifecycle.ErrConflict)
			assert.Contains(t, err.Error(), "no earlier Bundle")
			if tc.wantHint {
				assert.Contains(t, err.Error(), "pick one with --to")
			} else {
				assert.NotContains(t, err.Error(), "--to")
			}
		})
	}
}

// TestPlanRollback_DropsConfigRefOfImageTarget: an image Bundle stored before
// the CRD refused a configRef on it may carry one it never deployed. The
// rollback Bundle made from it carries no configRef, so the API server, which
// now refuses one on an image Bundle, accepts it (#1353).
func TestPlanRollback_DropsConfigRefOfImageTarget(t *testing.T) {
	v1 := bundle("v1", "app", "1", 0)
	v1.Spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://git.example/cfg", CommitSHA: "abc"}
	c := newClient(t, pipeline("app", "prod"), v1, bundle("v2", "app", "2", 10),
		step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "HealthChecking", 11))
	plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", FromBundle: "v2",
	})
	require.NoError(t, err)
	assert.Equal(t, "image", plan.Bundle.Spec.Type)
	assert.Nil(t, plan.Bundle.Spec.ConfigRef)
	require.Len(t, plan.Bundle.Spec.Images, 1)
	assert.Equal(t, "1", plan.Bundle.Spec.Images[0].Tag)
}
