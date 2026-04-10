// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package v1alpha1 contains API types for the kardinal.io/v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=kardinal.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group version for all kardinal-promoter CRDs.
	GroupVersion = schema.GroupVersion{Group: "kardinal.io", Version: "v1alpha1"}

	// SchemeBuilder is used to register types with the scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds all v1alpha1 types to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
