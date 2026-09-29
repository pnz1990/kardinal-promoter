// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/random.go.
// Original copyright: The Kubernetes Authors, Apache 2.0.

package library

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

const alphanumericChars = "0123456789abcdefghijklmnopqrstuvwxyz"

// maxSeededStringLength bounds random.seededString, so one gate expression
// cannot make the controller allocate an arbitrarily large string.
const maxSeededStringLength = 1024

// Random returns a CEL library that provides deterministic random generation.
//
//   - random.seededInt(min int, max int, seed string) → int
//   - random.seededString(length int, seed string) → string (length 1 to 1024)
func Random() cel.EnvOption {
	return cel.Lib(&randomLibrary{})
}

type randomLibrary struct{}

func (l *randomLibrary) LibraryName() string {
	return "random"
}

func (l *randomLibrary) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("random.seededString",
			cel.Overload("random.seededString_int_string",
				[]*cel.Type{cel.IntType, cel.StringType},
				cel.StringType,
				cel.BinaryBinding(generateDeterministicString),
			),
		),
		cel.Function("random.seededInt",
			cel.Overload("random.seededInt_int_int_string",
				[]*cel.Type{cel.IntType, cel.IntType, cel.StringType},
				cel.IntType,
				cel.FunctionBinding(generateDeterministicInt),
			),
		),
	}
}

func (l *randomLibrary) ProgramOptions() []cel.ProgramOption {
	return nil
}

func generateDeterministicInt(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("random.seededInt requires exactly 3 arguments")
	}
	minVal, maxVal, seed := args[0], args[1], args[2]
	if minVal.Type() != types.IntType {
		return types.NewErr("random.seededInt min must be an integer")
	}
	if maxVal.Type() != types.IntType {
		return types.NewErr("random.seededInt max must be an integer")
	}
	if seed.Type() != types.StringType {
		return types.NewErr("random.seededInt seed must be a string")
	}
	minInt := minVal.(types.Int).Value().(int64)
	maxInt := maxVal.(types.Int).Value().(int64)
	if minInt >= maxInt {
		return types.NewErr("random.seededInt min must be less than max")
	}
	seedStr := seed.(types.String).Value().(string)
	hash := sha256.Sum256([]byte(seedStr))
	v := binary.BigEndian.Uint64(hash[:8])
	// Go integer arithmetic wraps, so max-min is the exact range size modulo
	// 2^64 and min+offset is exact too: the result is in [min, max) even when
	// the range is wider than MaxInt64.
	rangeSize := uint64(maxInt - minInt)
	return types.Int(minInt + int64(v%rangeSize))
}

func generateDeterministicString(length ref.Val, seed ref.Val) ref.Val {
	if length.Type() != types.IntType {
		return types.NewErr("random.seededString length must be an integer")
	}
	if length.(types.Int) <= 0 {
		return types.NewErr("random.seededString length must be positive")
	}
	if length.(types.Int) > maxSeededStringLength {
		return types.NewErr("random.seededString length must be at most %d", maxSeededStringLength)
	}
	if seed.Type() != types.StringType {
		return types.NewErr("random.seededString seed must be a string")
	}
	n := int(length.(types.Int).Value().(int64))
	seedStr := seed.(types.String).Value().(string)
	// Each 32-byte hash yields 8 characters (4 bytes each). The first block is
	// sha256(seed), so strings of up to 8 characters are unchanged; every later
	// block is the hash of the previous one. Reusing one hash made the output
	// repeat every 8 characters (C04-gates-29).
	hash := sha256.Sum256([]byte(seedStr))
	result := make([]byte, n)
	charsLen := uint32(len(alphanumericChars))
	for i := 0; i < n; i++ {
		off := (i * 4) % len(hash)
		if i > 0 && off == 0 {
			hash = sha256.Sum256(hash[:])
		}
		idx := binary.BigEndian.Uint32(hash[off : off+4])
		result[i] = alphanumericChars[idx%charsLen]
	}
	return types.String(string(result))
}
