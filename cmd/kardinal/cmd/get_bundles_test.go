// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestGetBundles_FallbackReturnsItsOwnError (C09b-cli-23): when the
// field-selector list and the unfiltered fallback both fail, the error is the
// fallback's, with the first failure as context.
func TestGetBundles_FallbackReturnsItsOwnError(t *testing.T) {
	calls := 0
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectList, ...sigs_client.ListOption) error {
			calls++
			if calls == 1 {
				return apierrors.NewBadRequest("field label not supported: spec.pipeline") // Kubernetes 1.30
			}
			return errors.New("bundles.kardinal.io is forbidden")
		},
	}).Build()

	err := getBundlesFn(&bytes.Buffer{}, c, "default", []string{"demo"}, false)
	require.Error(t, err)
	assert.Equal(t, "list bundles of pipeline demo: bundles.kardinal.io is forbidden", err.Error())
}
