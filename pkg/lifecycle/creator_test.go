// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestCreateBundleAs: the Bundle is created with kardinal.io/created-by; when
// the bundle-creator admission policy refuses this controller the creator
// (another controller instance), it is created without one, which no gate
// with excludeAuthor passes; any other refusal is returned.
func TestCreateBundleAs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	bundles := schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}
	for _, tc := range []struct {
		name    string
		refuse  error
		want    string
		wantErr bool
	}{
		{name: "admitted", want: "bundle-api"},
		{name: "creator refused", refuse: apierrors.NewForbidden(bundles, "b",
			errorString(`ValidatingAdmissionPolicy 'kardinal-promoter-bundle-creator' denied request: the kardinal.io/created-by annotation must be your own username`)), want: ""},
		{name: "other refusal", refuse: apierrors.NewForbidden(bundles, "b", errorString("quota")), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, stamped := obj.GetAnnotations()[lifecycle.AnnotationCreatedBy]; stamped && tc.refuse != nil {
						return tc.refuse
					}
					if tc.wantErr {
						return tc.refuse
					}
					return cl.Create(ctx, obj, opts...)
				}}).Build()
			b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}
			err := lifecycle.CreateBundleAs(context.Background(), c, b, "bundle-api")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			var got v1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(b), &got))
			assert.Equal(t, tc.want, got.Annotations[lifecycle.AnnotationCreatedBy])
		})
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
