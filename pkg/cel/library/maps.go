// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// This file is adapted from github.com/kubernetes-sigs/kro/pkg/cel/library/maps.go.
// Original copyright: Google LLC, Apache 2.0.

package library

import (
	"math"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// Maps returns a cel.EnvOption to configure extended functions for map manipulation.
//
// # Merge
//
//	map(string, T).merge(map(string, T)) -> map(string, T)
//
// Merges two maps. Keys from the second map overwrite keys in the first map.
func Maps(options ...MapsOption) cel.EnvOption {
	l := &mapsLib{version: math.MaxUint32}
	for _, o := range options {
		l = o(l)
	}
	return cel.Lib(l)
}

// MapsOption is a functional option for configuring the maps library.
type MapsOption func(*mapsLib) *mapsLib

// MapsVersion configures the version of the maps library.
func MapsVersion(version uint32) MapsOption {
	return func(lib *mapsLib) *mapsLib {
		lib.version = version
		return lib
	}
}

type mapsLib struct {
	version uint32
}

func (mapsLib) LibraryName() string {
	return "cel.lib.ext.maps"
}

func (lib mapsLib) CompileOptions() []cel.EnvOption {
	mapType := cel.MapType(cel.TypeParamType("K"), cel.TypeParamType("V"))
	return []cel.EnvOption{
		cel.Function("merge",
			cel.MemberOverload("map_merge",
				[]*cel.Type{mapType, mapType},
				mapType,
				cel.BinaryBinding(mergeVals),
			),
		),
	}
}

func (lib mapsLib) ProgramOptions() []cel.ProgramOption {
	return []cel.ProgramOption{}
}

func mergeVals(lhs, rhs ref.Val) ref.Val {
	left, lok := lhs.(traits.Mapper)
	right, rok := rhs.(traits.Mapper)
	if !lok || !rok {
		return types.ValOrErr(lhs, "no such overload: %v.merge(%v)", lhs.Type(), rhs.Type())
	}
	return mergeMaps(left, right)
}

func mergeMaps(self, other traits.Mapper) traits.Mapper {
	result := mapperToMutable(other)
	for i := self.Iterator(); i.HasNext().(types.Bool); {
		k := i.Next()
		if !result.Contains(k).(types.Bool) {
			result.Insert(k, self.Get(k))
		}
	}
	return result.ToImmutableMap()
}

func mapperToMutable(m traits.Mapper) traits.MutableMapper {
	vals := make(map[ref.Val]ref.Val, m.Size().(types.Int))
	for it := m.Iterator(); it.HasNext().(types.Bool); {
		k := it.Next()
		vals[k] = m.Get(k)
	}
	return types.NewMutableMap(types.DefaultTypeAdapter, vals)
}
