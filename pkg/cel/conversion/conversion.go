// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/conversion.
// Original copyright: The Kubernetes Authors, Apache 2.0.

// Package conversion provides helpers for converting CEL ref.Val values
// to Go native types suitable for JSON marshalling.
package conversion

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/sentinels"
)

// ErrUnsupportedType is returned when the type is not supported.
var ErrUnsupportedType = errors.New("unsupported type")

// GoNativeType transforms CEL output into corresponding Go types.
func GoNativeType(v ref.Val) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	switch v.Type() {
	case types.BoolType:
		return v.Value().(bool), nil
	case types.IntType:
		return v.Value().(int64), nil
	case types.UintType:
		return v.Value().(uint64), nil
	case types.DoubleType:
		return v.Value().(float64), nil
	case types.StringType:
		return v.Value().(string), nil
	case types.BytesType:
		return v.Value().([]byte), nil
	case types.DurationType:
		return v1.Duration{Duration: v.Value().(time.Duration)}.ToUnstructured(), nil
	case types.TimestampType:
		return v1.Time{Time: v.Value().(time.Time)}.ToUnstructured(), nil
	case types.ListType:
		return convertList(v)
	case types.MapType:
		return convertMap(v)
	case types.OptionalType:
		opt := v.(*types.Optional)
		if !opt.HasValue() {
			return nil, nil
		}
		return GoNativeType(opt.GetValue())
	case types.UnknownType:
		return v.Value(), nil
	case types.NullType:
		return nil, nil
	default:
		if _, ok := v.Value().(sentinels.Omit); ok {
			return sentinels.Omit{}, nil
		}
		return v.Value(), fmt.Errorf("%w: %v", ErrUnsupportedType, v.Type())
	}
}

func convertList(v ref.Val) (interface{}, error) {
	lister, ok := v.(traits.Lister)
	if !ok {
		return v.ConvertToNative(reflect.TypeOf([]interface{}{}))
	}
	result := make([]interface{}, 0)
	it := lister.Iterator()
	for it.HasNext() == types.True {
		elem := it.Next()
		native, err := GoNativeType(elem)
		if err != nil {
			return nil, err
		}
		result = append(result, native)
	}
	return result, nil
}

func convertMap(v ref.Val) (interface{}, error) {
	mapper, ok := v.(traits.Mapper)
	if !ok {
		return v.ConvertToNative(reflect.TypeOf(map[string]any{}))
	}
	if rawMap, ok := v.Value().(map[string]interface{}); ok {
		return runtime.DeepCopyJSON(rawMap), nil
	}
	result := make(map[string]interface{})
	it := mapper.Iterator()
	for it.HasNext() == types.True {
		key := it.Next()
		if key == nil {
			continue
		}
		val := mapper.Get(key)
		keyNative, err := GoNativeType(key)
		if err != nil {
			return nil, fmt.Errorf("failed to convert map key: %w", err)
		}
		keyStr, ok := keyNative.(string)
		if !ok {
			return nil, fmt.Errorf("map key must be string, got %T", keyNative)
		}
		valNative, err := GoNativeType(val)
		if err != nil {
			return nil, err
		}
		result[keyStr] = valNative
	}
	return result, nil
}
