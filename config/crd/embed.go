// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package crd holds the kardinal.io CustomResourceDefinitions that make
// manifests generates in bases/ (and copies into the chart's crds/),
// embedded so the CLI can check documents against them offline (kardinal
// validate). This file sits outside bases/, which holds the generated YAML
// only.
package crd

import (
	"embed"
	"io/fs"
)

//go:embed bases/*.yaml
var files embed.FS

// Bases returns the generated CRD files, bases/*.yaml, at its root.
func Bases() fs.FS {
	sub, err := fs.Sub(files, "bases")
	if err != nil {
		panic(err) // a constant, valid directory name
	}
	return sub
}
