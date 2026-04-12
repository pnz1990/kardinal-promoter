// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/lists.go.
// Original copyright: The Kubernetes Authors, Apache 2.0.

package library

import (
	"math"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// Lists returns a CEL library that provides index-mutation functions for lists.
// All functions are pure — they return a new list and do not modify the input.
//
//   - lists.setAtIndex(list(T), int, T) -> list(T)
//   - lists.insertAtIndex(list(T), int, T) -> list(T)
//   - lists.removeAtIndex(list(T), int) -> list(T)
func Lists(options ...ListsOption) cel.EnvOption {
	l := &listsLibrary{version: math.MaxUint32}
	for _, o := range options {
		l = o(l)
	}
	return cel.Lib(l)
}

// ListsOption is a functional option for configuring the lists library.
type ListsOption func(*listsLibrary) *listsLibrary

// ListsVersion configures the version of the lists library.
func ListsVersion(version uint32) ListsOption {
	return func(lib *listsLibrary) *listsLibrary {
		lib.version = version
		return lib
	}
}

type listsLibrary struct {
	version uint32
}

func (l *listsLibrary) LibraryName() string {
	return "kro.lists"
}

func (l *listsLibrary) CompileOptions() []cel.EnvOption {
	listType := cel.ListType(cel.TypeParamType("T"))
	return []cel.EnvOption{
		cel.Function("lists.setAtIndex",
			cel.Overload("lists.setAtIndex_list_int_T",
				[]*cel.Type{listType, cel.IntType, cel.TypeParamType("T")},
				listType,
				cel.FunctionBinding(listsSetAtIndex),
			),
		),
		cel.Function("lists.insertAtIndex",
			cel.Overload("lists.insertAtIndex_list_int_T",
				[]*cel.Type{listType, cel.IntType, cel.TypeParamType("T")},
				listType,
				cel.FunctionBinding(listsInsertAtIndex),
			),
		),
		cel.Function("lists.removeAtIndex",
			cel.Overload("lists.removeAtIndex_list_int",
				[]*cel.Type{listType, cel.IntType},
				listType,
				cel.BinaryBinding(listsRemoveAtIndex),
			),
		),
	}
}

func (l *listsLibrary) ProgramOptions() []cel.ProgramOption {
	return nil
}

func listsSetAtIndex(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("lists.setAtIndex: expected 3 arguments (arr, index, value)")
	}
	lister, ok := args[0].(traits.Lister)
	if !ok {
		return types.NewErr("lists.setAtIndex: first argument must be a list")
	}
	if args[1].Type() != types.IntType {
		return types.NewErr("lists.setAtIndex: index must be an integer")
	}
	idx := int64(args[1].(types.Int))
	size := int64(lister.Size().(types.Int))
	if idx < 0 || idx >= size {
		return types.NewErr("lists.setAtIndex: index %d out of bounds [0, %d)", idx, size)
	}
	elems := make([]ref.Val, size)
	for i := int64(0); i < size; i++ {
		if i == idx {
			elems[i] = args[2]
		} else {
			elems[i] = lister.Get(types.Int(i))
		}
	}
	return types.NewRefValList(types.DefaultTypeAdapter, elems)
}

func listsInsertAtIndex(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("lists.insertAtIndex: expected 3 arguments (arr, index, value)")
	}
	lister, ok := args[0].(traits.Lister)
	if !ok {
		return types.NewErr("lists.insertAtIndex: first argument must be a list")
	}
	if args[1].Type() != types.IntType {
		return types.NewErr("lists.insertAtIndex: index must be an integer")
	}
	idx := int64(args[1].(types.Int))
	size := int64(lister.Size().(types.Int))
	if idx < 0 || idx > size {
		return types.NewErr("lists.insertAtIndex: index %d out of bounds [0, %d]", idx, size)
	}
	elems := make([]ref.Val, size+1)
	for i := int64(0); i < idx; i++ {
		elems[i] = lister.Get(types.Int(i))
	}
	elems[idx] = args[2]
	for i := idx; i < size; i++ {
		elems[i+1] = lister.Get(types.Int(i))
	}
	return types.NewRefValList(types.DefaultTypeAdapter, elems)
}

func listsRemoveAtIndex(arrVal, idxVal ref.Val) ref.Val {
	lister, ok := arrVal.(traits.Lister)
	if !ok {
		return types.NewErr("lists.removeAtIndex: first argument must be a list")
	}
	if idxVal.Type() != types.IntType {
		return types.NewErr("lists.removeAtIndex: index must be an integer")
	}
	idx := int64(idxVal.(types.Int))
	size := int64(lister.Size().(types.Int))
	if idx < 0 || idx >= size {
		return types.NewErr("lists.removeAtIndex: index %d out of bounds [0, %d)", idx, size)
	}
	elems := make([]ref.Val, size-1)
	for i := int64(0); i < idx; i++ {
		elems[i] = lister.Get(types.Int(i))
	}
	for i := idx + 1; i < size; i++ {
		elems[i-1] = lister.Get(types.Int(i))
	}
	return types.NewRefValList(types.DefaultTypeAdapter, elems)
}
