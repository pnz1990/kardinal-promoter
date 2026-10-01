// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestPlanRollback_BundleTypes covers B51: a mixed Bundle deploys its config
// commit and then its images, so rolling one back restores both, and a mixed
// Bundle is a source of the newest earlier config commit as well as of
// images. Each row is a deployed Bundle type and a target type (image, mixed,
// config). A target is refused only when its type cannot deploy what the
// deployed Bundle changed: a config Bundle cannot deploy the images a mixed
// Bundle changed, an image Bundle cannot deploy the config commit it changed.
// Without --to, a mixed Bundle goes back to the newest earlier images and
// config commit (B61), an image or config Bundle to the newest earlier images
// or config commit, also from a mixed Bundle (B66), and an automatic rollback
// plans what a manual one does.
func TestPlanRollback_BundleTypes(t *testing.T) {
	// withImages adds images "repo:tag" under ghcr.io/x/ to b.
	withImages := func(b *v1alpha1.Bundle, refs ...string) *v1alpha1.Bundle {
		for _, r := range refs {
			repo, tag, _ := strings.Cut(r, ":")
			b.Spec.Images = append(b.Spec.Images, v1alpha1.ImageRef{Repository: "ghcr.io/x/" + repo, Tag: tag})
		}
		return b
	}
	img := func(name string, minute int, refs ...string) *v1alpha1.Bundle {
		return withImages(bundle(name, "app", "", minute), refs...)
	}
	cfg := func(name string, minute int, commit string) *v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		b.Spec.Type = "config"
		b.Spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://g/cfg", CommitSHA: commit}
		return b
	}
	mix := func(name string, minute int, commit string, refs ...string) *v1alpha1.Bundle {
		b := cfg(name, minute, commit)
		b.Spec.Type = "mixed"
		return withImages(b, refs...)
	}
	verified := func(name string, minute int) *v1alpha1.PromotionStep {
		return step(name, "app", "prod", "Verified", minute+1)
	}
	deployed := func(name string, minute int) *v1alpha1.PromotionStep {
		return step(name, "app", "prod", "HealthChecking", minute+1)
	}
	images := func(b *v1alpha1.Bundle) []string {
		var out []string
		for _, im := range b.Spec.Images {
			out = append(out, strings.TrimPrefix(im.Repository, "ghcr.io/x/")+":"+im.Tag)
		}
		slices.Sort(out)
		return out
	}

	tests := []struct {
		name       string
		objs       []client.Object
		to         string
		wantTarget string
		wantType   string
		wantImages []string
		wantConfig string
		wantErr    error
		errHas     []string
	}{
		// Deployed image Bundle.
		{
			name:       "image to image",
			objs:       []client.Object{img("v1", 10, "a:1"), verified("v1", 10), img("v2", 20, "a:2"), deployed("v2", 20)},
			wantTarget: "v1", wantType: "image", wantImages: []string{"a:1"},
		},
		{
			name: "image to mixed deploys the mixed Bundle's images and commit",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), img("v2", 20, "a:2"), deployed("v2", 20)},
			to:         "m1",
			wantTarget: "m1", wantType: "mixed", wantImages: []string{"a:1"}, wantConfig: "c1",
		},
		{
			name: "image to config is refused: a config Bundle deploys no images",
			objs: []client.Object{
				cfg("c1", 10, "c1"), verified("c1", 10), img("v2", 20, "a:2"), deployed("v2", 20)},
			to:      "c1",
			wantErr: lifecycle.ErrInvalid, errHas: []string{"ghcr.io/x/a", "pick an image or mixed Bundle with --to"},
		},
		{
			name: "a mixed Bundle is a source of the images an image target does not name",
			objs: []client.Object{
				mix("m0", 0, "c0", "a:0", "b:0"), verified("m0", 0), img("v1", 10, "a:1"), verified("v1", 10),
				img("v2", 20, "b:2"), deployed("v2", 20)},
			wantTarget: "v1", wantType: "image", wantImages: []string{"a:1", "b:0"},
		},

		// Deployed config Bundle.
		{
			name: "config to config",
			objs: []client.Object{
				cfg("c1", 10, "c1"), verified("c1", 10), cfg("c2", 20, "c2"), deployed("c2", 20)},
			wantTarget: "c1", wantType: "config", wantConfig: "c1",
		},
		{
			name: "config to mixed deploys the mixed Bundle's commit and images",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), cfg("c2", 20, "c2"), deployed("c2", 20)},
			to:         "m1",
			wantTarget: "m1", wantType: "mixed", wantImages: []string{"a:1"}, wantConfig: "c1",
		},
		{
			name: "config to image is refused: an image Bundle deploys no config commit",
			objs: []client.Object{
				img("v1", 10, "a:1"), verified("v1", 10), cfg("c2", 20, "c2"), deployed("c2", 20)},
			to:      "v1",
			wantErr: lifecycle.ErrInvalid, errHas: []string{"https://g/cfg", "pick a config or mixed Bundle with --to"},
		},
		{
			name: "the newest earlier config commit can come from a mixed Bundle",
			objs: []client.Object{
				cfg("c0", 0, "c0"), verified("c0", 0), mix("m1", 5, "c1", "a:1"), verified("m1", 5),
				func() *v1alpha1.Bundle {
					// Made with kubectl: a config Bundle with only an image.
					b := cfg("k1", 10, "")
					b.Spec.ConfigRef = nil
					return withImages(b, "a:1")
				}(), verified("k1", 10),
				cfg("c2", 20, "c2"), deployed("c2", 20)},
			to:         "k1",
			wantTarget: "k1", wantType: "config", wantImages: []string{"a:1"}, wantConfig: "c1",
		},

		// Deployed mixed Bundle.
		{
			name: "mixed to mixed restores the images and the commit",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			wantTarget: "m1", wantType: "mixed", wantImages: []string{"a:1"}, wantConfig: "c1",
		},
		{
			name: "mixed to mixed fills an image the target does not name",
			objs: []client.Object{
				img("v0", 0, "b:0"), verified("v0", 0), mix("m1", 10, "c1", "a:1"), verified("m1", 10),
				mix("m2", 20, "c2", "a:2", "b:2"), deployed("m2", 20)},
			wantTarget: "m1", wantType: "mixed", wantImages: []string{"a:1", "b:0"}, wantConfig: "c1",
		},
		{
			name: "mixed to image when the mixed Bundle kept the newest earlier commit",
			objs: []client.Object{
				cfg("c1", 5, "c1"), verified("c1", 5), img("v1", 10, "a:1"), verified("v1", 10),
				mix("m2", 20, "c1", "a:2"), deployed("m2", 20)},
			to:         "v1",
			wantTarget: "v1", wantType: "image", wantImages: []string{"a:1"},
		},
		{
			name: "mixed to image is refused when the mixed Bundle changed the commit",
			objs: []client.Object{
				cfg("c1", 5, "c1"), verified("c1", 5), img("v1", 10, "a:1"), verified("v1", 10),
				mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			to:      "v1",
			wantErr: lifecycle.ErrInvalid,
			errHas:  []string{"https://g/cfg", "deployed mixed bundle m2 changed from c1 to c2", "pick a config or mixed Bundle with --to"},
		},
		{
			name: "mixed to image is refused when no earlier commit was verified",
			objs: []client.Object{
				img("v1", 10, "a:1"), verified("v1", 10), mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			to:      "v1",
			wantErr: lifecycle.ErrInvalid, errHas: []string{"changed to c2, and no earlier config commit was Verified in prod"},
		},
		{
			name: "mixed to config when the mixed Bundle kept the newest earlier images",
			objs: []client.Object{
				img("v1", 5, "a:1"), verified("v1", 5), cfg("c1", 10, "c1"), verified("c1", 10),
				mix("m2", 20, "c2", "a:1"), deployed("m2", 20)},
			to:         "c1",
			wantTarget: "c1", wantType: "config", wantConfig: "c1",
		},
		{
			name: "mixed to config is refused when the mixed Bundle changed an image",
			objs: []client.Object{
				img("v1", 5, "a:1", "b:1"), verified("v1", 5), cfg("c1", 10, "c1"), verified("c1", 10),
				mix("m2", 20, "c2", "a:1", "b:2"), deployed("m2", 20)},
			to:      "c1",
			wantErr: lifecycle.ErrInvalid,
			errHas:  []string{"cannot restore the images (ghcr.io/x/b) that the deployed mixed bundle m2 changed", "pick an image or mixed Bundle with --to"},
		},
		{
			name: "mixed to config that changes nothing is a conflict",
			objs: []client.Object{
				img("v1", 5, "a:1"), verified("v1", 5), cfg("c1", 10, "c1"), verified("c1", 10),
				mix("m2", 20, "c1", "a:1"), deployed("m2", 20)},
			to:      "c1",
			wantErr: lifecycle.ErrConflict, errHas: []string{"restores the same artifacts as the deployed bundle m2"},
		},
		// sameDeployed compares only what the rollback Bundle deploys with
		// what the environment runs: cur's artifacts and, for what cur did
		// not deploy, the newest earlier version in the history.
		{
			name: "image to mixed that changes nothing is a conflict: the config commit is the one deployed",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:2"), verified("m1", 10), img("v2", 20, "a:2"), deployed("v2", 20)},
			to:      "m1",
			wantErr: lifecycle.ErrConflict, errHas: []string{"rolling back to bundle m1 restores the same artifacts as the deployed bundle v2"},
		},
		{
			name: "image to image that changes nothing is a conflict: the other repository is at the target's version",
			objs: []client.Object{
				img("v1", 10, "a:2", "b:1"), verified("v1", 10), img("v2", 20, "a:2"), deployed("v2", 20)},
			to:      "v1",
			wantErr: lifecycle.ErrConflict, errHas: []string{"restores the same artifacts as the deployed bundle v2"},
		},
		{
			name: "config to mixed that changes nothing is a conflict: the images are the ones deployed",
			objs: []client.Object{
				mix("m1", 10, "c2", "a:1"), verified("m1", 10), cfg("k2", 20, "c2"), deployed("k2", 20)},
			to:      "m1",
			wantErr: lifecycle.ErrConflict, errHas: []string{"restores the same artifacts as the deployed bundle k2"},
		},
		{
			name: "image to image that changes only a repository the deployed Bundle does not name",
			objs: []client.Object{
				img("v1", 5, "a:2", "b:1"), verified("v1", 5), img("v1b", 10, "b:3"), verified("v1b", 10),
				img("v2", 20, "a:2"), deployed("v2", 20)},
			to:         "v1",
			wantTarget: "v1", wantType: "image", wantImages: []string{"a:2", "b:1"},
		},
		{
			// The only Bundle that names b, v1, was rolled back from, so the
			// history does not know which b the environment runs: deploying
			// b:1 counts as a change, not as the deployed image.
			name: "image to image with a repository the history has no deployed version of is not a conflict",
			objs: []client.Object{
				img("v1", 10, "a:2", "b:1"), verified("v1", 10),
				func() *v1alpha1.Bundle {
					b := img("rb-v1", 15, "a:2")
					b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"}
					b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "v1"}
					b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
					return b
				}(),
				img("v2", 20, "a:2"), deployed("v2", 20)},
			to:         "v1",
			wantTarget: "v1", wantType: "image", wantImages: []string{"a:2", "b:1"},
		},
		{
			name: "without --to a candidate that changes nothing is skipped",
			objs: []client.Object{
				img("v0", 5, "a:1"), verified("v0", 5), img("v1", 10, "a:2", "b:1"), verified("v1", 10),
				img("v2", 20, "a:2"), deployed("v2", 20)},
			wantTarget: "v0", wantType: "image", wantImages: []string{"a:1"},
		},

		// Deployed mixed Bundle, without --to (B61): the rollback goes back to
		// the newest earlier images and config commit, whichever Bundles
		// deployed them. It used to consider only mixed Bundles, so it skipped
		// newer image and config Bundles, or found nothing.
		{
			name: "without --to a mixed Bundle takes the image of a newer image Bundle",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), img("v2", 12, "a:2"), verified("v2", 12),
				mix("m3", 20, "c3", "a:3"), deployed("m3", 20)},
			wantTarget: "v2", wantType: "mixed", wantImages: []string{"a:2"}, wantConfig: "c1",
		},
		{
			name: "without --to a mixed Bundle takes the commit of a newer config Bundle",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), cfg("k2", 12, "c2"), verified("k2", 12),
				mix("m3", 20, "c3", "a:3"), deployed("m3", 20)},
			wantTarget: "k2", wantType: "mixed", wantImages: []string{"a:1"}, wantConfig: "c2",
		},
		{
			name: "without --to the newest earlier image and commit come from two Bundles",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), img("v1", 12, "a:5"), verified("v1", 12),
				cfg("c3", 14, "c3"), verified("c3", 14), mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			wantTarget: "c3", wantType: "mixed", wantImages: []string{"a:5"}, wantConfig: "c3",
		},
		{
			name: "without --to a mixed Bundle that kept the commit goes back to an image Bundle",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), img("v2", 12, "a:2"), verified("v2", 12),
				mix("m3", 20, "c1", "a:3"), deployed("m3", 20)},
			wantTarget: "v2", wantType: "image", wantImages: []string{"a:2"},
		},
		{
			name: "without --to a mixed Bundle that kept the images goes back to a config Bundle",
			objs: []client.Object{
				img("v1", 5, "a:1"), verified("v1", 5), cfg("k1", 10, "c1"), verified("k1", 10),
				mix("m2", 20, "c2", "a:1"), deployed("m2", 20)},
			wantTarget: "k1", wantType: "config", wantConfig: "c1",
		},
		{
			name: "without --to a mixed Bundle with no earlier mixed Bundle",
			objs: []client.Object{
				cfg("k0", 5, "c0"), verified("k0", 5), img("v1", 10, "a:1"), verified("v1", 10),
				mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			wantTarget: "v1", wantType: "mixed", wantImages: []string{"a:1"}, wantConfig: "c0",
		},
		{
			name: "without --to a mixed Bundle with no earlier config commit is refused",
			objs: []client.Object{
				img("v1", 10, "a:1"), verified("v1", 10), mix("m2", 20, "c2", "a:2"), deployed("m2", 20)},
			wantErr: lifecycle.ErrConflict,
			errHas:  []string{"no Bundle other than m2 with a config commit of https://g/cfg was Verified in prod"},
		},

		// Deployed image or config Bundle, without --to (B66): an image Bundle
		// goes back to the newest earlier images, from an image or mixed
		// Bundle, and a config Bundle to the newest earlier config commit, from
		// a config or mixed Bundle. A mixed target gives only what the
		// deployed type deploys, so the rest stays as deployed. It used to
		// consider only Bundles of the deployed type, so it skipped a newer
		// mixed Bundle for an older one, or found nothing.
		{
			name: "without --to an image Bundle takes the images of a newer mixed Bundle",
			objs: []client.Object{
				img("i0", 5, "a:0"), verified("i0", 5), mix("m1", 10, "c1", "a:1"), verified("m1", 10),
				img("i2", 20, "a:2"), deployed("i2", 20)},
			wantTarget: "m1", wantType: "image", wantImages: []string{"a:1"},
		},
		{
			name: "without --to an image Bundle with no earlier image Bundle goes back to a mixed one",
			objs: []client.Object{
				mix("m1", 10, "c1", "a:1"), verified("m1", 10), img("i2", 20, "a:2"), deployed("i2", 20)},
			wantTarget: "m1", wantType: "image", wantImages: []string{"a:1"},
		},
		{
			name: "without --to an image rollback to a mixed Bundle fills an image the target does not name",
			objs: []client.Object{
				img("i0", 5, "a:0", "b:0"), verified("i0", 5), mix("m1", 10, "c1", "a:1"), verified("m1", 10),
				img("i2", 20, "a:2", "b:2"), deployed("i2", 20)},
			wantTarget: "m1", wantType: "image", wantImages: []string{"a:1", "b:0"},
		},
		{
			name: "without --to an image rollback compares only images: a mixed Bundle with the deployed images is skipped",
			objs: []client.Object{
				img("i0", 0, "a:0"), verified("i0", 0), mix("m0", 5, "c0", "a:1"), verified("m0", 5),
				mix("m1", 10, "c1", "a:3"), verified("m1", 10), cfg("k2", 15, "c2"), verified("k2", 15),
				img("i3", 20, "a:3"), deployed("i3", 20)},
			wantTarget: "m0", wantType: "image", wantImages: []string{"a:1"},
		},
		{
			name: "without --to a config Bundle takes the commit of a newer mixed Bundle",
			objs: []client.Object{
				cfg("k0", 5, "c0"), verified("k0", 5), mix("m1", 10, "c1", "a:1"), verified("m1", 10),
				cfg("k2", 20, "c2"), deployed("k2", 20)},
			wantTarget: "m1", wantType: "config", wantConfig: "c1",
		},
		{
			name: "without --to a config rollback compares only the commit: a mixed Bundle with the deployed commit is skipped",
			objs: []client.Object{
				cfg("k0", 0, "c0"), verified("k0", 0), mix("m0", 5, "c1", "a:0"), verified("m0", 5),
				mix("m1", 10, "c3", "a:1"), verified("m1", 10), img("i2", 15, "a:2"), verified("i2", 15),
				cfg("k3", 20, "c3"), deployed("k3", 20)},
			wantTarget: "m0", wantType: "config", wantConfig: "c1",
		},
	}
	for _, tc := range tests {
		// Without --to, an automatic rollback (RollbackPolicy,
		// onHealthFailure=rollback) plans the same rollback.
		modes := []bool{false}
		if tc.to == "" {
			modes = append(modes, true)
		}
		for _, automatic := range modes {
			name := tc.name
			if automatic {
				name += " (automatic)"
			}
			t.Run(name, func(t *testing.T) {
				c := newClient(t, append(tc.objs, pipeline("app", "test", "prod"))...)
				plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
					Namespace: ns, Pipeline: "app", Environment: "prod", ToBundle: tc.to, Automatic: automatic,
				})
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
					for _, s := range tc.errHas {
						assert.Contains(t, err.Error(), s)
					}
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.wantTarget, plan.Target.Name)
				assert.Equal(t, tc.wantTarget, plan.Bundle.Spec.Provenance.RollbackOf)
				assert.Equal(t, tc.wantType, plan.Bundle.Spec.Type)
				assert.Equal(t, tc.wantImages, images(plan.Bundle))
				commit := ""
				if ref := plan.Bundle.Spec.ConfigRef; ref != nil {
					commit = ref.CommitSHA
				}
				assert.Equal(t, tc.wantConfig, commit)
			})
		}
	}
}
