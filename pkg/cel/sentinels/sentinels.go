// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/sentinels.
// Original copyright: The Kubernetes Authors, Apache 2.0.

// Package sentinels provides marker types for CEL template field omission.
package sentinels

// Omit is a marker value that signals the resolver to remove the
// containing field or array element from the rendered object instead
// of writing a value.
type Omit struct{}

// IsOmit returns true if the value is an Omit sentinel.
func IsOmit(v interface{}) bool {
	_, ok := v.(Omit)
	return ok
}
