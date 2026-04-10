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

// Package main is the entry point for the kardinal CLI binary.
// The CLI creates and reads kardinal-promoter CRDs via the Kubernetes API.
package main

import (
	"os"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	root := &cobra.Command{
		Use:   "kardinal",
		Short: "kardinal-promoter CLI",
		Long:  "kardinal manages promotion pipelines for Kubernetes-native artifact delivery.",
	}

	// TODO(stage-8): add all subcommands (get, create, explain, rollback, etc.)

	// Load kubeconfig (used by subcommands in Stage 8)
	_ = clientcmd.NewDefaultClientConfigLoadingRules()

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
