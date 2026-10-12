// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// TestPipelineReconciler_GitCredentialMissing (P4): a Pipeline whose HTTP(S)
// remote needs a token and whose spec.git.secretRef is unset, names a Secret
// that does not exist, or a Secret with no token is Ready=False with reason
// GitCredentialMissing right after it is applied, before any Bundle, and
// the message is the command that fixes it. An unlabelled Secret with a
// token is not used: Ready=False with reason SecretNotReferenceable and the
// label command (#1506). A GitHub App Secret,
// an ssh remote or a password in the URL need no token key. The condition is
// re-checked every minute (Secrets are not watched), a second reconcile
// writes nothing, and creating the Secret makes the Pipeline Ready.
//
// Covers PIPE-GITCRED-02.
func TestPipelineReconciler_GitCredentialMissing(t *testing.T) {
	secret := func(labels map[string]string, data map[string]string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "team-git", Namespace: "default", Labels: labels},
			Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	labelled := map[string]string{"kardinal.io/referenceable": "true"}
	tests := []struct {
		name    string
		url     string
		ref     string
		secret  *corev1.Secret
		missing bool
		want    string // the Ready message, or "" for Ready=True
		reason  string // the Ready reason when want is set; "" is GitCredentialMissing
	}{{
		name:    "secretRef unset",
		missing: true,
		want: "spec.git.secretRef is not set, so git has no token for https://github.com/myorg/gitops.git; create the Secret " +
			"(kubectl create secret generic git-token -n default --from-literal=token=<TOKEN> && " +
			"kubectl label secret git-token -n default kardinal.io/referenceable=true) and set spec.git.secretRef.name: git-token",
	}, {
		name: "Secret missing", ref: "team-git", missing: true,
		want: "git Secret team-git not found in namespace default, so git has no token for https://github.com/myorg/gitops.git; " +
			"create it: kubectl create secret generic team-git -n default --from-literal=token=<TOKEN> && " +
			"kubectl label secret team-git -n default kardinal.io/referenceable=true",
	}, {
		name: "Secret without a token", ref: "team-git", missing: true,
		secret: secret(labelled, map[string]string{"username": "me"}),
		want: "git Secret team-git has no token key, so git has no token for https://github.com/myorg/gitops.git; add it: " +
			`kubectl patch secret team-git -n default --type merge -p '{"stringData":{"token":"<TOKEN>"}}'`,
	}, {
		name: "unlabelled Secret without a token", ref: "team-git", missing: true,
		secret: secret(nil, map[string]string{"token": "  "}),
		want: "git Secret team-git has no token key, so git has no token for https://github.com/myorg/gitops.git; add it: " +
			`kubectl patch secret team-git -n default --type merge -p '{"stringData":{"token":"<TOKEN>"}}' && ` +
			"kubectl label secret team-git -n default kardinal.io/referenceable=true",
	}, {
		name: "unreferenceable Secret with a token", ref: "team-git",
		secret: secret(nil, map[string]string{"token": "t0ken"}),
		want: "git Secret team-git is not labeled kardinal.io/referenceable=true, so git does not use it; " +
			"label it: kubectl label secret team-git -n default kardinal.io/referenceable=true",
		reason: "SecretNotReferenceable",
	}, {
		name: "GitHub App Secret", ref: "team-git",
		secret: secret(labelled, map[string]string{"githubAppPrivateKey": "-----BEGIN RSA PRIVATE KEY-----", "githubAppID": "1", "githubAppInstallationID": "2"}),
	}, {
		name: "ssh remote", url: "ssh://git@github.com/myorg/gitops.git",
	}, {
		name: "password in the URL", url: "https://me:t0ken@github.com/myorg/gitops.git",
	}, {
		name: "labelled Secret with a token", ref: "team-git", secret: secret(labelled, map[string]string{"token": "t0ken"}),
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}})
			p.Spec.Git.SecretRef = nil
			if tc.ref != "" {
				p.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: tc.ref}
			}
			if tc.url != "" {
				p.Spec.Git.URL = tc.url
			}
			objs := []client.Object{p}
			if tc.secret != nil {
				objs = append(objs, tc.secret)
			}
			c := newClientWithIndex(newScheme(), objs...)
			r := &pipeline.Reconciler{Client: c}
			key := types.NamespacedName{Name: "app", Namespace: "default"}
			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			var got kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), key, &got))
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			if tc.want == "" {
				assert.Equal(t, metav1.ConditionTrue, ready.Status, ready.Message)
				assert.Equal(t, "Valid", ready.Reason)
			} else {
				reason := tc.reason
				if reason == "" {
					reason = "GitCredentialMissing"
				}
				assert.Equal(t, metav1.ConditionFalse, ready.Status)
				assert.Equal(t, reason, ready.Reason)
				assert.Equal(t, tc.want, ready.Message)
				assert.Equal(t, 2*time.Second, res.RequeueAfter, "re-checked soon: Secrets are not watched")
			}
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "SecretReferenceable"))

			rv := got.ResourceVersion
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			require.NoError(t, c.Get(context.Background(), key, &got))
			assert.Equal(t, rv, got.ResourceVersion, "a second reconcile writes nothing")

			if !tc.missing {
				return
			}
			// The fix: a labelled Secret with a token, named by secretRef.
			fixed := secret(labelled, map[string]string{"token": "t0ken"})
			if tc.secret != nil {
				require.NoError(t, c.Delete(context.Background(), tc.secret))
			}
			require.NoError(t, c.Create(context.Background(), fixed))
			if tc.ref == "" {
				require.NoError(t, c.Get(context.Background(), key, &got))
				got.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: "team-git"}
				require.NoError(t, c.Update(context.Background(), &got))
			}
			res, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			require.NoError(t, c.Get(context.Background(), key, &got))
			ready = meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionTrue, ready.Status, ready.Message)
			assert.Zero(t, res.RequeueAfter)
		})
	}
}

// TestPipelineReconciler_GitCredentialMissingHidesUserinfo (P4 QA): a URL
// whose user name is a token ("https://<token>@host/...") needs a token, and
// the Ready message names the remote without it.
//
// Covers PIPE-GITCRED-02.
func TestPipelineReconciler_GitCredentialMissingHidesUserinfo(t *testing.T) {
	p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}})
	p.Spec.Git.SecretRef = nil
	p.Spec.Git.URL = "https://ghp_s3cr3tT0ken@github.com/myorg/gitops.git"
	c := newClientWithIndex(newScheme(), p)
	key := types.NamespacedName{Name: "app", Namespace: "default"}
	_, err := (&pipeline.Reconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), key, &got))
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "GitCredentialMissing", ready.Reason)
	assert.NotContains(t, ready.Message, "s3cr3t")
	assert.Contains(t, ready.Message, "git has no token for https://github.com/myorg/gitops.git;")
}

// TestPipelineReconciler_GitCredentialAfterSpecErrors (P4): a spec error is
// reported first; the credential is checked only for a valid spec.
//
// Covers PIPE-GITCRED-02.
func TestPipelineReconciler_GitCredentialAfterSpecErrors(t *testing.T) {
	p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "test"}})
	p.Spec.Git.SecretRef = nil
	c := newClientWithIndex(newScheme(), p)
	key := types.NamespacedName{Name: "app", Namespace: "default"}
	_, err := (&pipeline.Reconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), key, &got))
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "ValidationFailed", ready.Reason)
}

// TestPipelineReconciler_SecretRecheckSchedule: the controller may not watch
// or list Secrets (#1266), so a Pipeline whose git Secret is missing or not
// labelled reads it again soon: every 2s for the first 2 minutes of the
// condition (someone is likely following its fix-it), every 15s until 15
// minutes, then every minute. Once the Secret is created the next reconcile
// makes the Pipeline Ready and stops the rechecks.
func TestPipelineReconciler_SecretRecheckSchedule(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}})
	p.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: "team-token"}
	c := newClientWithIndex(newScheme(), p)
	now := t0
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return now }}
	key := types.NamespacedName{Name: "app", Namespace: "default"}
	reconcileAt := func(at time.Time) ctrl.Result {
		t.Helper()
		now = at
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		return res
	}
	assert.Equal(t, 2*time.Second, reconcileAt(t0).RequeueAfter, "just reported")
	// The condition's lastTransitionTime is the fake client's clock; age it.
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), key, &got))
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	require.Equal(t, "GitCredentialMissing", ready.Reason)
	since := ready.LastTransitionTime.Time
	assert.Equal(t, 2*time.Second, reconcileAt(since.Add(90*time.Second)).RequeueAfter)
	assert.Equal(t, 15*time.Second, reconcileAt(since.Add(5*time.Minute)).RequeueAfter)
	assert.Equal(t, time.Minute, reconcileAt(since.Add(20*time.Minute)).RequeueAfter)

	require.NoError(t, c.Create(context.Background(), gitTokenSecret("team-token")))
	assert.Zero(t, reconcileAt(since.Add(21*time.Minute)).RequeueAfter, "Ready: no more rechecks")
	require.NoError(t, c.Get(context.Background(), key, &got))
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(got.Status.Conditions, "Ready").Status)
}
