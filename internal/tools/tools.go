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

//go:build tools

// Package tools pins build-time and runtime dependencies that are not yet
// imported by any Go source file in Stage 0. This file ensures that go mod tidy
// retains the packages required by subsequent stages. It is excluded from
// normal builds via the "tools" build tag.
//
// Packages are added here as they become needed in later stages. Remove this
// file and its imports once each package is used in production code.
package tools

import (
	// Runtime logging — used by all binaries from Stage 2 onward
	_ "github.com/rs/zerolog"
	// CLI framework — used by cmd/kardinal from Stage 8
	_ "github.com/spf13/cobra"
	// Kubernetes controller framework — used by cmd/kardinal-controller from Stage 2
	_ "sigs.k8s.io/controller-runtime/pkg/manager"
	// Kubernetes client — used by CLI and controller from Stage 2 onward
	_ "k8s.io/client-go/tools/clientcmd"
	// Kubernetes API types — used by CRD types from Stage 1
	_ "k8s.io/api/core/v1"
	// Kubernetes API machinery — used by CRD types and reconcilers from Stage 1
	_ "k8s.io/apimachinery/pkg/apis/meta/v1"
	// CEL evaluator — used by pkg/cel from Stage 4
	_ "github.com/google/cel-go/cel"
)
