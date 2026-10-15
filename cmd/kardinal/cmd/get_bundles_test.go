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
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestGetBundles_ListErrorIsReturned (C09b-cli-23): a failed list of one
// Pipeline's Bundles is returned with the Pipeline as context; there is no
// unfiltered fallback.
func TestGetBundles_ListErrorIsReturned(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectList, ...sigs_client.ListOption) error {
			return errors.New("bundles.kardinal.io is forbidden")
		},
	}).Build()

	err := getBundlesFn(&bytes.Buffer{}, c, "default", []string{"demo"}, false)
	require.Error(t, err)
	assert.Equal(t, "list bundles of pipeline demo: bundles.kardinal.io is forbidden", err.Error())
}
