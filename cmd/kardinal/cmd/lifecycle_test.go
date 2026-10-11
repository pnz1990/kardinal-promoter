// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

var lcT0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func lcPipeline() *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", UID: "uid-app"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "uat"}, {Name: "prod"},
		}},
	}
}

func lcBundle(name, pipeline, tag string, minute int) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(lcT0.Add(time.Duration(minute) * time.Minute)),
		},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: pipeline,
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: tag}},
		},
		Status: v1alpha1.BundleStatus{Phase: "Superseded"},
	}
}

func lcStep(bundle, env, state string, minute int) *v1alpha1.PromotionStep {
	at := metav1.NewTime(lcT0.Add(time.Duration(minute) * time.Minute))
	s := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundle + "-" + env, Namespace: "default", CreationTimestamp: at,
			Labels: map[string]string{
				"kardinal.io/pipeline": "app", "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
		Status: v1alpha1.PromotionStepStatus{State: state},
	}
	if state == "Verified" {
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}
	}
	return s
}

func lcClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.Pipeline{}).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
}

// newBundles returns the Bundles that are not in the fixture.
func newBundles(t *testing.T, c client.Client, fixture ...string) []v1alpha1.Bundle {
	t.Helper()
	var list v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	known := map[string]bool{}
	for _, n := range fixture {
		known[n] = true
	}
	var out []v1alpha1.Bundle
	for _, b := range list.Items {
		if !known[b.Name] {
			out = append(out, b)
		}
	}
	return out
}

// TestCreateBundle_DigestStoredInDigestField covers C09a-cli-02: an image
// pinned by digest produces ImageRef.Digest, never a sha256 tag.
func TestCreateBundle_DigestStoredInDigestField(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name      string
		image     string
		wantRepo  string
		wantTag   string
		wantDgst  string
		wantError string
	}{
		{name: "digest only", image: "ghcr.io/pnz1990/kardinal-test-app@" + digest,
			wantRepo: "ghcr.io/pnz1990/kardinal-test-app", wantDgst: digest},
		{name: "tag and digest", image: "ghcr.io/pnz1990/kardinal-test-app:v1@" + digest,
			wantRepo: "ghcr.io/pnz1990/kardinal-test-app", wantTag: "v1", wantDgst: digest},
		{name: "tag only", image: "ghcr.io/pnz1990/kardinal-test-app:sha-abc1234",
			wantRepo: "ghcr.io/pnz1990/kardinal-test-app", wantTag: "sha-abc1234"},
		{name: "malformed digest", image: "ghcr.io/pnz1990/kardinal-test-app@not a digest", wantError: "invalid image digest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := lcClient(t, lcPipeline())
			var buf bytes.Buffer
			err := createBundleFn(&buf, c, "default", "app", createBundleOptions{Images: []string{tc.image}, Type: "image"})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Empty(t, newBundles(t, c), "no Bundle is created for a bad image")
				return
			}
			require.NoError(t, err)
			created := newBundles(t, c)
			require.Len(t, created, 1)
			require.Len(t, created[0].Spec.Images, 1)
			img := created[0].Spec.Images[0]
			assert.Equal(t, tc.wantRepo, img.Repository)
			assert.Equal(t, tc.wantTag, img.Tag)
			assert.Equal(t, tc.wantDgst, img.Digest)
			assert.NotEmpty(t, created[0].Annotations[lifecycle.AnnotationCreatedAt],
				"the CLI stamps sub-second creation order for supersession (C02-bundle-04)")
		})
	}
}

// TestRollbackCmd_RestoresPreviousVerifiedBundle covers C09b-cli-02 and
// C09b-cli-03: the CLI rolls back to the Bundle verified before the deployed
// one, copies its images, validates --to, and creates nothing on refusal.
func TestRollbackCmd_RestoresPreviousVerifiedBundle(t *testing.T) {
	history := []client.Object{
		lcPipeline(),
		lcBundle("app-v1", "app", "1", 0), lcBundle("app-v2", "app", "2", 10),
		lcBundle("other-v1", "other", "9", 0),
		lcStep("app-v1", "prod", "Verified", 5), lcStep("app-v2", "prod", "Verified", 15),
	}
	fixture := []string{"app-v1", "app-v2", "other-v1"}
	tests := []struct {
		name       string
		objs       []client.Object
		to         string
		wantTarget string
		wantTag    string
		wantErr    error
	}{
		{name: "default target is the bundle verified before the deployed one",
			objs: history, wantTarget: "app-v1", wantTag: "1"},
		{name: "--to an earlier bundle of the pipeline",
			objs: history, to: "app-v1", wantTarget: "app-v1", wantTag: "1"},
		{name: "--to the deployed bundle is refused",
			objs: history, to: "app-v2", wantErr: lifecycle.ErrConflict},
		{name: "--to a bundle of another pipeline is refused",
			objs: history, to: "other-v1", wantErr: lifecycle.ErrInvalid},
		{name: "--to a missing bundle is refused",
			objs: history, to: "app-v0", wantErr: lifecycle.ErrNotFound},
		{name: "only one verified bundle: nothing earlier to roll back to",
			objs:    []client.Object{lcPipeline(), lcBundle("app-v1", "app", "1", 0), lcStep("app-v1", "prod", "Verified", 5)},
			wantErr: lifecycle.ErrConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := lcClient(t, tc.objs...)
			var buf bytes.Buffer
			err := rollbackFn(&buf, c, "default", "app", "prod", tc.to)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, newBundles(t, c, fixture...), "a refused rollback creates no Bundle")
				return
			}
			require.NoError(t, err)
			created := newBundles(t, c, fixture...)
			require.Len(t, created, 1)
			rb := created[0]
			assert.Equal(t, tc.wantTarget, rb.Spec.Provenance.RollbackOf)
			require.Len(t, rb.Spec.Images, 1, "the rollback Bundle carries the target's images")
			assert.Equal(t, tc.wantTag, rb.Spec.Images[0].Tag)
			assert.Equal(t, "prod", rb.Spec.Intent.TargetEnvironment)
			assert.Equal(t, "true", rb.Labels[lifecycle.LabelRollback])
			assert.Equal(t, "app-v2", rb.Annotations[lifecycle.AnnotationRollbackFrom])
			assert.Contains(t, buf.String(), "from app-v2 to "+tc.wantTarget)
			assert.Contains(t, buf.String(), "ghcr.io/org/app:"+tc.wantTag)
		})
	}
}

// TestRollbackCmd_EmergencyDeprecated is #1288: --emergency still parses, so
// a runbook that passes it keeps working, prints a deprecation warning that
// points to kardinal override, and is hidden from the help.
func TestRollbackCmd_EmergencyDeprecated(t *testing.T) {
	cmd := newRollbackCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.ParseFlags([]string{"--env", "prod", "--emergency"}))
	assert.Contains(t, out.String(),
		"Flag --emergency has been deprecated, it has no effect and is removed in v0.11; use kardinal override to pass a blocking gate")
	assert.NotContains(t, cmd.Flags().FlagUsages(), "emergency", "a deprecated flag is not in the help")
}

// TestPromoteCmd_CopiesBundleVerifiedUpstream covers C09b-cli-04: promote
// copies the artifacts of the Bundle verified upstream and never creates an
// image-less Bundle.
func TestPromoteCmd_CopiesBundleVerifiedUpstream(t *testing.T) {
	tests := []struct {
		name    string
		objs    []client.Object
		env     string
		wantSrc string
		wantErr error
	}{
		{name: "copies the bundle verified in uat",
			objs: []client.Object{lcPipeline(), lcBundle("app-v1", "app", "1", 0), lcStep("app-v1", "uat", "Verified", 5)},
			env:  "prod", wantSrc: "app-v1"},
		{name: "nothing verified upstream is refused",
			objs: []client.Object{lcPipeline(), lcBundle("app-v1", "app", "1", 0), lcStep("app-v1", "uat", "Promoting", 5)},
			env:  "prod", wantErr: lifecycle.ErrConflict},
		{name: "the first environment has nothing upstream",
			objs: []client.Object{lcPipeline()}, env: "test", wantErr: lifecycle.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := lcClient(t, tc.objs...)
			var buf bytes.Buffer
			err := promoteFn(&buf, c, "default", "app", tc.env)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, newBundles(t, c, "app-v1"), "a refused promote creates no Bundle")
				return
			}
			require.NoError(t, err)
			created := newBundles(t, c, "app-v1")
			require.Len(t, created, 1)
			b := created[0]
			require.Len(t, b.Spec.Images, 1, "a promote Bundle always carries images")
			assert.Equal(t, "1", b.Spec.Images[0].Tag)
			assert.Equal(t, tc.env, b.Spec.Intent.TargetEnvironment)
			assert.Equal(t, tc.wantSrc, b.Annotations[lifecycle.AnnotationPromotedFrom])
			assert.Contains(t, buf.String(), tc.wantSrc)
		})
	}
}

// TestRollbackCmd_Hold (#1528): rollback --hold creates the rollback Bundle
// and holds the environment on it with the reason, says how to release it,
// and refuses a hold without a reason; release-hold removes the hold.
func TestRollbackCmd_Hold(t *testing.T) {
	history := []client.Object{
		lcPipeline(),
		lcBundle("app-v1", "app", "1", 0), lcBundle("app-v2", "app", "2", 10),
		lcStep("app-v1", "prod", "Verified", 5), lcStep("app-v2", "prod", "Verified", 15),
	}
	getP := func(c client.Client) *v1alpha1.Pipeline {
		var p v1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "app"}, &p))
		return &p
	}

	c := lcClient(t, history...)
	var buf bytes.Buffer
	require.ErrorContains(t, rollbackHoldFn(&buf, c, "default", "app", "prod", "", " ", "alice", 0), "--reason")
	assert.Empty(t, newBundles(t, c, "app-v1", "app-v2"))

	require.NoError(t, rollbackHoldFn(&buf, c, "default", "app", "prod", "", "INC-42", "alice", 2*time.Hour))
	created := newBundles(t, c, "app-v1", "app-v2")
	require.Len(t, created, 1)
	p := getP(c)
	require.Len(t, p.Spec.Holds, 1)
	assert.Equal(t, created[0].Name, p.Spec.Holds[0].Bundle)
	assert.Equal(t, "INC-42", p.Spec.Holds[0].Reason)
	assert.Equal(t, "alice", p.Spec.Holds[0].CreatedBy, "createdBy is the Kubernetes user the caller passes")
	require.NotNil(t, p.Spec.Holds[0].ExpiresAt)
	assert.Equal(t, 2*time.Hour, p.Spec.Holds[0].ExpiresAt.Sub(p.Spec.Holds[0].CreatedAt.Time))
	assert.Equal(t, lifecycle.ArtifactDigest(created[0].Spec), p.Spec.Holds[0].Artifacts)
	out := buf.String()
	assert.Contains(t, out, "from app-v2 to app-v1")
	assert.Contains(t, out, "Environment prod held on "+created[0].Name)
	assert.Contains(t, out, "kardinal release-hold app --env prod")
	assert.Contains(t, out, "EXEMPT")
	assert.Contains(t, out, "The hold expires at ")

	require.ErrorIs(t, rollbackHoldFn(&buf, c, "default", "app", "prod", "", "again", "alice", 0), lifecycle.ErrConflict)

	buf.Reset()
	require.NoError(t, releaseHoldFn(&buf, c, "default", "app", "prod"))
	assert.Contains(t, buf.String(), "Released the hold of app on prod (rollback "+created[0].Name)
	assert.Empty(t, getP(c).Spec.Holds)
	require.ErrorIs(t, releaseHoldFn(&buf, c, "default", "app", "prod"), lifecycle.ErrNotFound)
}

// TestRollbackCmd_HoldFlags (#1528): --reason without --hold is refused
// before anything is created, and release-hold needs --env.
func TestRollbackCmd_HoldFlags(t *testing.T) {
	for args, want := range map[string]string{
		"--reason x":           "--reason is the reason of a hold; add --hold",
		"--hold":               "rollback --hold needs --reason",
		"--hold --reason":      "rollback --hold needs --reason",
		"--hold-expires-in 1h": "--hold-expires-in is a positive duration of a hold",
	} {
		cmd := newRollbackCmd()
		argv := append([]string{"app", "--env", "prod"}, strings.Fields(args)...)
		if strings.HasSuffix(args, "--reason") {
			argv = append(argv, " ")
		}
		cmd.SetArgs(argv)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		require.ErrorContains(t, cmd.Execute(), want, args)
	}

	var out bytes.Buffer
	rel := newReleaseHoldCmd()
	rel.SetArgs([]string{"app"})
	rel.SetOut(&out)
	rel.SetErr(&out)
	require.ErrorContains(t, rel.Execute(), `required flag(s) "env" not set`)
}
