// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package conversion_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/conversion"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoNativeType_NilVal(t *testing.T) {
	// O1 — GoNativeType(nil) must return ErrNilCELValue, not (nil, nil).
	result, err := conversion.GoNativeType(nil)
	assert.Nil(t, result, "result must be nil for a nil ref.Val")
	require.Error(t, err, "error must be non-nil for a nil ref.Val")
	assert.True(t, errors.Is(err, conversion.ErrNilCELValue),
		"error must wrap ErrNilCELValue; got: %v", err)
}

func TestGoNativeType_NullType(t *testing.T) {
	// O3 — types.NullValue (CEL null) must return (nil, nil) — no error.
	result, err := conversion.GoNativeType(types.NullValue)
	assert.NoError(t, err, "types.NullValue must not produce an error")
	assert.Nil(t, result, "types.NullValue must map to nil Go value")
}

func TestGoNativeType_Table(t *testing.T) {
	boolTrue := types.True
	boolFalse := types.False
	intVal := types.Int(42)
	uintVal := types.Uint(7)
	doubleVal := types.Double(3.14)
	strVal := types.String("hello")
	bytesVal := types.Bytes([]byte{1, 2, 3})
	dur := types.Duration{Duration: 5 * time.Second}
	ts := types.Timestamp{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	listVal := types.DefaultTypeAdapter.NativeToValue([]interface{}{"a", "b"})
	mapVal := types.DefaultTypeAdapter.NativeToValue(map[string]interface{}{"k": "v"})

	tests := []struct {
		name      string
		input     ref.Val
		wantErr   bool
		wantIsNil bool
		wantVal   interface{}
	}{
		{
			name:      "nil ref.Val → ErrNilCELValue",
			input:     nil,
			wantErr:   true,
			wantIsNil: true,
		},
		{
			name:      "NullType → (nil, nil)",
			input:     types.NullValue,
			wantErr:   false,
			wantIsNil: true,
		},
		{
			name:    "bool true",
			input:   boolTrue,
			wantVal: true,
		},
		{
			name:    "bool false",
			input:   boolFalse,
			wantVal: false,
		},
		{
			name:    "int",
			input:   intVal,
			wantVal: int64(42),
		},
		{
			name:    "uint",
			input:   uintVal,
			wantVal: uint64(7),
		},
		{
			name:    "double",
			input:   doubleVal,
			wantVal: float64(3.14),
		},
		{
			name:    "string",
			input:   strVal,
			wantVal: "hello",
		},
		{
			name:    "bytes",
			input:   bytesVal,
			wantVal: []byte{1, 2, 3},
		},
		{
			name:    "duration",
			input:   dur,
			wantVal: v1.Duration{Duration: 5 * time.Second}.ToUnstructured(),
		},
		{
			name:    "timestamp",
			input:   ts,
			wantVal: v1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}.ToUnstructured(),
		},
		{
			name:  "list",
			input: listVal,
		},
		{
			name:  "map",
			input: mapVal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := conversion.GoNativeType(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				if tt.input == nil {
					assert.True(t, errors.Is(err, conversion.ErrNilCELValue),
						"nil input must produce ErrNilCELValue; got: %v", err)
				}
				return
			}
			require.NoError(t, err)
			if tt.wantIsNil {
				assert.Nil(t, got)
				return
			}
			if tt.wantVal != nil {
				assert.Equal(t, tt.wantVal, got)
			} else {
				// List/map — just check non-nil
				assert.NotNil(t, got)
			}
		})
	}
}

func TestGoNativeType_OptionalEmpty(t *testing.T) {
	// Optional with no value — should return (nil, nil), not ErrNilCELValue.
	opt := types.OptionalNone
	result, err := conversion.GoNativeType(opt)
	assert.NoError(t, err, "empty Optional must not produce an error")
	assert.Nil(t, result, "empty Optional must map to nil Go value")
}

func TestGoNativeType_OptionalPresent(t *testing.T) {
	opt := types.OptionalOf(types.String("present"))
	result, err := conversion.GoNativeType(opt)
	require.NoError(t, err)
	assert.Equal(t, "present", result)
}

// TestGoNativeType_GoNativeMapValues covers C04-gates-28: a map built in Go
// (such as the PolicyGate context) holds values that are not JSON types, for
// example int. Converting it must not panic, so json.marshal(schedule) works.
func TestGoNativeType_GoNativeMapValues(t *testing.T) {
	v := types.DefaultTypeAdapter.NativeToValue(map[string]interface{}{
		"hour":   10,
		"count":  int32(3),
		"names":  []string{"a", "b"},
		"nested": map[string]interface{}{"n": 1, "ok": true},
	})
	var got interface{}
	var err error
	require.NotPanics(t, func() { got, err = conversion.GoNativeType(v) })
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"hour":   int64(10),
		"count":  int64(3),
		"names":  []interface{}{"a", "b"},
		"nested": map[string]interface{}{"n": int64(1), "ok": true},
	}, got)
}

func TestGoNativeType_NilSentinel_ErrorIs(t *testing.T) {
	// Verify errors.Is unwrapping works for ErrNilCELValue.
	_, err := conversion.GoNativeType(nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, conversion.ErrNilCELValue))
	assert.False(t, errors.Is(err, conversion.ErrUnsupportedType),
		"nil should not be ErrUnsupportedType")
}
