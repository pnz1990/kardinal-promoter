// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/json.go.
// Original copyright: The Kubernetes Authors, Apache 2.0.

package library

import (
	"encoding/json"
	"math"
	"reflect"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/conversion"
)

// JSON returns a CEL library that provides JSON parsing functions.
//
// Functions:
//   - json.unmarshal(jsonString string) → dyn  — parses JSON string into CEL value
//   - json.marshal(value dyn) → string         — converts CEL value to JSON string
func JSON(options ...JSONOption) cel.EnvOption {
	lib := &jsonLibrary{version: math.MaxUint32}
	for _, o := range options {
		lib = o(lib)
	}
	return cel.Lib(lib)
}

// JSONOption is a functional option for configuring the json library.
type JSONOption func(*jsonLibrary) *jsonLibrary

// JSONVersion configures the version of the json library.
func JSONVersion(version uint32) JSONOption {
	return func(lib *jsonLibrary) *jsonLibrary {
		lib.version = version
		return lib
	}
}

type jsonLibrary struct {
	version uint32
}

func (l *jsonLibrary) LibraryName() string {
	return "json"
}

func (l *jsonLibrary) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("json.unmarshal",
			cel.Overload("json.unmarshal_string",
				[]*cel.Type{cel.StringType},
				cel.DynType,
				cel.UnaryBinding(unmarshalJSON),
			),
		),
		cel.Function("json.marshal",
			cel.Overload("json.marshal_dyn",
				[]*cel.Type{cel.DynType},
				cel.StringType,
				cel.UnaryBinding(marshalJSON),
			),
		),
	}
}

func (l *jsonLibrary) ProgramOptions() []cel.ProgramOption {
	return nil
}

func unmarshalJSON(jsonString ref.Val) ref.Val {
	native, err := jsonString.ConvertToNative(reflect.TypeOf(""))
	if err != nil {
		return types.NewErr("json.unmarshal argument must be a string")
	}
	str, ok := native.(string)
	if !ok {
		return types.NewErr("json.unmarshal argument must be a string")
	}
	var result interface{}
	if err := json.Unmarshal([]byte(str), &result); err != nil {
		return types.NewErr("json.unmarshal failed to parse JSON: %s", err.Error())
	}
	return types.DefaultTypeAdapter.NativeToValue(result)
}

func marshalJSON(value ref.Val) ref.Val {
	native, err := conversion.GoNativeType(value)
	if err != nil {
		return types.NewErr("json.marshal failed to convert value: %s", err.Error())
	}
	jsonBytes, err := json.Marshal(native)
	if err != nil {
		return types.NewErr("json.marshal failed to encode value: %s", err.Error())
	}
	return types.String(string(jsonBytes))
}
