// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type failingMapper struct {
	meta.RESTMapper
	err error
}

func (m failingMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, m.err
}

// QA-3 on #2171: only discovery's "no match" means a kind is not served.
// Any other error is returned, so the controller retries a discovery outage
// instead of failing every version with verification as if Argo Rollouts
// were not installed.
//
// Covers ANALYSIS-DISCOVERY-01.
func TestServedKind(t *testing.T) {
	served, err := servedKind(nil, "argoproj.io/v1alpha1", "AnalysisRun")
	require.NoError(t, err)
	assert.True(t, served, "no mapper: assumed served")

	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "argoproj.io", Kind: "AnalysisRun"}}
	served, err = servedKind(failingMapper{err: noMatch}, "argoproj.io/v1alpha1", "AnalysisRun")
	require.NoError(t, err)
	assert.False(t, served, "not installed")

	served, err = servedKind(failingMapper{err: context.DeadlineExceeded}, "argoproj.io/v1alpha1", "AnalysisRun")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, served)
}
