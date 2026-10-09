// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// whoAmIInterceptor answers SelfSubjectReview creates as the API server does
// for user, or fails them with err.
func whoAmIInterceptor(user string, groups []string, err error) interceptor.Funcs {
	return interceptor.Funcs{Create: func(ctx context.Context, c sigs_client.WithWatch, obj sigs_client.Object, opts ...sigs_client.CreateOption) error {
		review, ok := obj.(*authenticationv1.SelfSubjectReview)
		if !ok {
			return c.Create(ctx, obj, opts...)
		}
		if err != nil {
			return err
		}
		review.Status.UserInfo = authenticationv1.UserInfo{Username: user, Groups: groups}
		return nil
	}}
}

func rejectScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestReject(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 30, 15, 500, time.UTC)
	fresh := func() *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default"},
			Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
			Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
		}
	}
	cases := []struct {
		name      string
		bundle    func() *v1alpha1.Bundle
		reason    string
		user      string
		whoErr    error
		wantErr   string
		wantOut   string
		wantNoSet bool
	}{
		{name: "rejects with the authenticated username", bundle: fresh, reason: "  CVE in base image ", user: "oidc:alice@example.com",
			wantOut: "Bundle app-v2 rejected by oidc:alice@example.com (was Promoting): CVE in base image"},
		{name: "reason is required", bundle: fresh, reason: "  ", user: "alice", wantErr: "--reason is required", wantNoSet: true},
		{name: "unknown bundle", reason: "x", user: "alice", wantErr: "bundle app-v2 not found in namespace default"},
		{name: "already rejected", reason: "again", user: "bob", wantNoSet: true,
			bundle: func() *v1alpha1.Bundle {
				b := fresh()
				b.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "first"}
				return b
			}, wantErr: "bundle app-v2 was already rejected by alice: first"},
		{name: "identity unavailable", bundle: fresh, reason: "x", wantNoSet: true,
			whoErr:  apierrors.NewForbidden(schema.GroupResource{Group: "authentication.k8s.io", Resource: "selfsubjectreviews"}, "", errors.New("no")),
			wantErr: "read your identity (SelfSubjectReview)"},
		{name: "server returns no username", bundle: fresh, reason: "x", wantNoSet: true, wantErr: errNoIdentity.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithInterceptorFuncs(whoAmIInterceptor(tc.user, []string{"devs"}, tc.whoErr))
			if tc.bundle != nil {
				b = b.WithObjects(tc.bundle())
			}
			c := b.Build()
			var out bytes.Buffer
			err := rejectFn(context.Background(), &out, c, "default", "app-v2", tc.reason, now)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Contains(t, out.String(), tc.wantOut)
			}
			if tc.bundle == nil {
				return
			}
			var got v1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app-v2"}, &got))
			if tc.wantNoSet {
				assert.Equal(t, tc.bundle().Spec.Rejected, got.Spec.Rejected, "spec.rejected unchanged")
				return
			}
			require.NotNil(t, got.Spec.Rejected)
			assert.Equal(t, tc.user, got.Spec.Rejected.By)
			assert.Equal(t, "CVE in base image", got.Spec.Rejected.Reason)
			require.NotNil(t, got.Spec.Rejected.At)
			assert.True(t, got.Spec.Rejected.At.Time.Equal(now.Truncate(time.Second)))
		})
	}
}

// TestReject_ConflictIsReported: the patch carries the resourceVersion the
// CLI read, so a Bundle changed in between is reported, not overwritten.
func TestReject_ConflictIsReported(t *testing.T) {
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
	}
	funcs := whoAmIInterceptor("alice", nil, nil)
	funcs.Patch = func(_ context.Context, _ sigs_client.WithWatch, obj sigs_client.Object, _ sigs_client.Patch, _ ...sigs_client.PatchOption) error {
		return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, obj.GetName(), errors.New("modified"))
	}
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(bundle).WithInterceptorFuncs(funcs).Build()
	err := rejectFn(context.Background(), &bytes.Buffer{}, c, "default", "app-v2", "bad", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed while rejecting it")
}

func TestWhoAmI(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).
		WithInterceptorFuncs(whoAmIInterceptor("system:serviceaccount:ci:deployer", []string{"system:serviceaccounts"}, nil)).Build()
	id, err := whoAmI(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, Identity{Username: "system:serviceaccount:ci:deployer", Groups: []string{"system:serviceaccounts"}}, id)
}
