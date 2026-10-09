// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func holdObjects() []client.Object {
	return []client.Object{
		pipeline("app", "test", "uat", "prod"),
		bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
		step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Verified", 11),
	}
}

func holdRequest() lifecycle.HoldRequest {
	return lifecycle.HoldRequest{
		RollbackRequest: lifecycle.RollbackRequest{Namespace: ns, Pipeline: "app", Environment: "prod",
			Actor: "alice", Now: t0.Add(time.Hour)},
		HoldReason: "INC-42: v2 leaks connections",
	}
}

func getPipeline(t *testing.T, c client.Client) *v1alpha1.Pipeline {
	t.Helper()
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "app"}, &p))
	return &p
}

// TestRollbackAndHold (#1528): the rollback Bundle is created with a fixed
// name, and the Pipeline holds the environment on it, with who and why.
func TestRollbackAndHold(t *testing.T) {
	c := newClient(t, holdObjects()...)
	plan, hold, err := lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
	require.NoError(t, err)
	assert.Equal(t, "v1", plan.Target.Name)
	assert.True(t, strings.HasPrefix(plan.Bundle.Name, "app-rollback-"), plan.Bundle.Name)
	assert.Len(t, plan.Bundle.Name, len("app-rollback-")+6)

	var b v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: plan.Bundle.Name}, &b),
		"the rollback Bundle exists")
	assert.Equal(t, "true", b.Labels[lifecycle.LabelRollback])

	p := getPipeline(t, c)
	require.Len(t, p.Spec.Holds, 1)
	h := p.Spec.Holds[0]
	assert.Equal(t, hold.Bundle, h.Bundle, "the returned hold is the one written")
	assert.Equal(t, "prod", h.Environment)
	assert.Equal(t, plan.Bundle.Name, h.Bundle)
	assert.Equal(t, "INC-42: v2 leaks connections", h.Reason)
	assert.Equal(t, "alice", h.CreatedBy)
	assert.Equal(t, lifecycle.ArtifactDigest(b.Spec), h.Artifacts, "the hold records the rollback's artifact digest")
	assert.Nil(t, h.ExpiresAt, "no expiry unless asked")
	require.NotNil(t, h.CreatedAt)
	assert.True(t, h.CreatedAt.Time.Equal(t0.Add(time.Hour)))

	assert.Equal(t, &h, lifecycle.HoldOf(p, "prod"))
	assert.Nil(t, lifecycle.HoldOf(p, "uat"))
	assert.Equal(t, &h, lifecycle.HoldNaming(p, plan.Bundle.Name))
	assert.Nil(t, lifecycle.HeldFrom(p, "prod", plan.Bundle.Name), "the hold's own Bundle is not held out")
	assert.Equal(t, &h, lifecycle.HeldFrom(p, "prod", "v3"), "every other Bundle is")
	assert.Nil(t, lifecycle.HeldFrom(p, "uat", "v3"), "other environments are not held")
	assert.Equal(t, "environment prod is held on rollback "+h.Bundle+" (INC-42: v2 leaks connections) — release with: kardinal release-hold app --env prod",
		lifecycle.HeldMessage("app", &h))
	ex := lifecycle.GateExemption(&h, "expression false")
	assert.Equal(t, "EXEMPT: rollback "+h.Bundle+" holds prod (by alice: INC-42: v2 leaks connections); without the hold: expression false", ex)
	assert.True(t, lifecycle.IsHoldExemption(ex))
	assert.False(t, lifecycle.IsHoldExemption("expression false"))
}

// TestRollbackAndHold_Refused (#1528): a hold needs a reason, an environment
// is held at most once, and a failed rollback leaves no hold behind.
func TestRollbackAndHold_Refused(t *testing.T) {
	t.Run("no reason", func(t *testing.T) {
		c := newClient(t, holdObjects()...)
		req := holdRequest()
		req.HoldReason = "  "
		_, _, err := lifecycle.RollbackAndHold(context.Background(), c, req)
		require.ErrorIs(t, err, lifecycle.ErrInvalid)
		assert.Empty(t, getPipeline(t, c).Spec.Holds)
	})
	t.Run("already held", func(t *testing.T) {
		c := newClient(t, holdObjects()...)
		plan, first, err := lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
		require.NoError(t, err)
		// The rollback is deployed now: the refusal names the hold, not the
		// missing rollback target.
		require.NoError(t, c.Create(context.Background(), step(plan.Bundle.Name, "app", "prod", "Verified", 30)))
		_, _, err = lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
		require.ErrorIs(t, err, lifecycle.ErrConflict)
		assert.Contains(t, err.Error(), "kardinal release-hold app --env prod")
		p := getPipeline(t, c)
		require.Len(t, p.Spec.Holds, 1)
		assert.Equal(t, first.Bundle, p.Spec.Holds[0].Bundle, "the first hold stays")
		var bundles v1alpha1.BundleList
		require.NoError(t, c.List(context.Background(), &bundles))
		assert.Len(t, bundles.Items, 3, "the refused rollback created no Bundle")
	})
	t.Run("nothing to roll back to", func(t *testing.T) {
		c := newClient(t, pipeline("app", "prod"), bundle("v1", "app", "1", 0), step("v1", "app", "prod", "Verified", 1))
		_, _, err := lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
		require.Error(t, err)
		assert.Empty(t, getPipeline(t, c).Spec.Holds, "no hold without a rollback")
	})
	t.Run("bundle create fails", func(t *testing.T) {
		base := newClient(t, holdObjects()...)
		c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.Bundle); ok {
					return errors.New("admission webhook denied")
				}
				return cl.Create(ctx, obj, opts...)
			},
		})
		_, _, err := lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
		require.ErrorContains(t, err, "admission webhook denied")
		assert.Empty(t, getPipeline(t, base).Spec.Holds, "the hold is removed again")
	})
}

// TestReleaseHold (#1528): release removes only that environment's hold and
// returns it; an environment that is not held is ErrNotFound.
func TestReleaseHold(t *testing.T) {
	p := pipeline("app", "uat", "prod")
	p.Spec.Holds = []v1alpha1.EnvironmentHold{
		{Environment: "uat", Bundle: "app-rollback-aaaaaa", Reason: "a"},
		{Environment: "prod", Bundle: "app-rollback-bbbbbb", Reason: "b", CreatedBy: "bob"},
	}
	c := newClient(t, p)
	h, err := lifecycle.ReleaseHold(context.Background(), c, ns, "app", "prod")
	require.NoError(t, err)
	assert.Equal(t, "app-rollback-bbbbbb", h.Bundle)
	assert.Equal(t, "bob", h.CreatedBy)
	got := getPipeline(t, c)
	require.Len(t, got.Spec.Holds, 1)
	assert.Equal(t, "uat", got.Spec.Holds[0].Environment)

	_, err = lifecycle.ReleaseHold(context.Background(), c, ns, "app", "prod")
	require.ErrorIs(t, err, lifecycle.ErrNotFound)
	_, err = lifecycle.ReleaseHold(context.Background(), c, ns, "nope", "prod")
	require.ErrorIs(t, err, lifecycle.ErrNotFound)
}

// TestRollbackAndHold_ExpiresIn (#1528 QA): ExpiresIn sets expiresAt.
func TestRollbackAndHold_ExpiresIn(t *testing.T) {
	c := newClient(t, holdObjects()...)
	req := holdRequest()
	req.ExpiresIn = 4 * time.Hour
	_, h, err := lifecycle.RollbackAndHold(context.Background(), c, req)
	require.NoError(t, err)
	require.NotNil(t, h.ExpiresAt)
	assert.True(t, h.ExpiresAt.Time.Equal(t0.Add(5*time.Hour)))
}

// TestArtifactDigest (#1528 QA): the digest changes with every artifact
// (type, an image's repository, tag or digest, the config commit, the chart)
// and with nothing else.
func TestArtifactDigest(t *testing.T) {
	base := v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
		Images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "1"}}}
	d := lifecycle.ArtifactDigest(base)
	assert.True(t, strings.HasPrefix(d, "sha256:"))
	same := base
	same.Provenance = &v1alpha1.BundleProvenance{Author: "x"}
	same.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod"}
	assert.Equal(t, d, lifecycle.ArtifactDigest(same), "provenance and intent are not artifacts")
	for name, edit := range map[string]func(s *v1alpha1.BundleSpec){
		"type": func(s *v1alpha1.BundleSpec) { s.Type = "mixed" },
		"tag":  func(s *v1alpha1.BundleSpec) { s.Images = []v1alpha1.ImageRef{{Repository: "r/app", Tag: "2"}} },
		"digest": func(s *v1alpha1.BundleSpec) {
			s.Images = []v1alpha1.ImageRef{{Repository: "r/app", Tag: "1", Digest: "sha256:aa"}}
		},
		"repo":   func(s *v1alpha1.BundleSpec) { s.Images = []v1alpha1.ImageRef{{Repository: "r/other", Tag: "1"}} },
		"config": func(s *v1alpha1.BundleSpec) { s.ConfigRef = &v1alpha1.ConfigRef{CommitSHA: "abcd"} },
		"chart":  func(s *v1alpha1.BundleSpec) { s.Chart = &v1alpha1.ChartRef{Name: "c", Version: "1"} },
	} {
		s := base
		edit(&s)
		assert.NotEqual(t, d, lifecycle.ArtifactDigest(s), name)
	}
}

// TestHoldOf_Expired (#1528 QA): an expired hold counts as absent before the
// controller removes it, UntilExpiry requeues at expiresAt, and a new hold
// of the environment replaces the expired entry.
func TestHoldOf_Expired(t *testing.T) {
	defer func(f func() time.Time) { lifecycle.HoldNow = f }(lifecycle.HoldNow)
	now := t0
	lifecycle.HoldNow = func() time.Time { return now }
	exp := metav1.NewTime(t0.Add(time.Minute))
	p := pipeline("app", "prod")
	p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "rb", Reason: "r", ExpiresAt: &exp}}
	require.NotNil(t, lifecycle.HoldOf(p, "prod"))
	assert.Equal(t, time.Minute, lifecycle.UntilExpiry(lifecycle.HoldOf(p, "prod"), time.Hour))
	assert.Equal(t, time.Hour, lifecycle.UntilExpiry(&v1alpha1.EnvironmentHold{}, time.Hour))
	now = t0.Add(time.Minute)
	assert.Nil(t, lifecycle.HoldOf(p, "prod"), "expired")
	assert.Nil(t, lifecycle.HoldNaming(p, "rb"), "expired")
	assert.Nil(t, lifecycle.HeldFrom(p, "prod", "other"))

	objs := holdObjects()
	objs[0] = p
	p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
	c := newClient(t, objs...)
	_, h, err := lifecycle.RollbackAndHold(context.Background(), c, holdRequest())
	require.NoError(t, err, "an expired hold does not block a new one")
	got := getPipeline(t, c)
	require.Len(t, got.Spec.Holds, 1, "one entry per environment")
	assert.Equal(t, h.Bundle, got.Spec.Holds[0].Bundle)
}
