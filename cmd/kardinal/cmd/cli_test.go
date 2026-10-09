// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// cliTestScheme is the scheme of the fake clients the command tests use.
func cliTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}
