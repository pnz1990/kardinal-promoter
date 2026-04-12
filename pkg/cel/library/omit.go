// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/omit.go.
// Original copyright: The Kubernetes Authors, Apache 2.0.

package library

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/sentinels"
)

// omitVal is the CEL ref.Val wrapper for the omit sentinel.
type omitVal struct{}

var omitInstance = &omitVal{}

func (v *omitVal) ConvertToNative(typeDesc reflect.Type) (any, error) {
	if typeDesc == reflect.TypeOf(sentinels.Omit{}) {
		return sentinels.Omit{}, nil
	}
	if typeDesc.Kind() == reflect.Interface {
		return sentinels.Omit{}, nil
	}
	return nil, fmt.Errorf("unsupported native conversion from omit to %v", typeDesc)
}

func (v *omitVal) ConvertToType(typeVal ref.Type) ref.Val {
	if typeVal == types.TypeType {
		return types.NewObjectType("kro.omit")
	}
	return types.NewErr("unsupported conversion from omit to %v", typeVal)
}

func (v *omitVal) Equal(other ref.Val) ref.Val {
	return types.NewErr("omit() is a field-removal sentinel and cannot be compared")
}

func (v *omitVal) Type() ref.Type {
	return types.NewObjectType("kro.omit")
}

func (v *omitVal) Value() any {
	return sentinels.Omit{}
}

// Omit returns a cel.EnvOption that registers the omit() function.
// omit() is a zero-argument function that returns a sentinel value causing
// field omission in Graph templates.
func Omit() cel.EnvOption {
	return cel.Lib(&omitLib{})
}

type omitLib struct{}

func (l *omitLib) LibraryName() string {
	return "kro.omit"
}

func (l *omitLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("omit",
			cel.Overload("omit_void",
				[]*cel.Type{},
				cel.DynType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					return omitInstance
				}),
			),
		),
	}
}

func (l *omitLib) ProgramOptions() []cel.ProgramOption {
	return nil
}
