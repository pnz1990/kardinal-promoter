// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/random.go.
// Original copyright: The Kubernetes Authors, Apache 2.0.

package library

import (
	"crypto/sha256"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

const alphanumericChars = "0123456789abcdefghijklmnopqrstuvwxyz"

// Random returns a CEL library that provides deterministic random generation.
//
//   - random.seededInt(min int, max int, seed string) → int
//   - random.seededString(length int, seed string) → string
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
	v := uint64(hash[0])<<56 | uint64(hash[1])<<48 | uint64(hash[2])<<40 | uint64(hash[3])<<32 |
		uint64(hash[4])<<24 | uint64(hash[5])<<16 | uint64(hash[6])<<8 | uint64(hash[7])
	rangeSize := uint64(maxInt - minInt)
	result := minInt + int64(v%rangeSize)
	return types.Int(result)
}

func generateDeterministicString(length ref.Val, seed ref.Val) ref.Val {
	if length.Type() != types.IntType {
		return types.NewErr("random.seededString length must be an integer")
	}
	if length.(types.Int) <= 0 {
		return types.NewErr("random.seededString length must be positive")
	}
	if seed.Type() != types.StringType {
		return types.NewErr("random.seededString seed must be a string")
	}
	n := int(length.(types.Int).Value().(int64))
	seedStr := seed.(types.String).Value().(string)
	hash := sha256.Sum256([]byte(seedStr))
	result := make([]byte, n)
	charsLen := len(alphanumericChars)
	for i := 0; i < n; i++ {
		start := (i * 4) % len(hash)
		end := start + 4
		if end > len(hash) {
			newHash := sha256.Sum256(append(hash[:], result[:i]...))
			hash = newHash
			start = 0
		}
		idx := uint32(hash[start])<<24 | uint32(hash[start+1])<<16 | uint32(hash[start+2])<<8 | uint32(hash[start+3])
		result[i] = alphanumericChars[idx%uint32(charsLen)]
	}
	return types.String(string(result))
}
