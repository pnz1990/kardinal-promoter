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

package cmd

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// CLIVersion is the static CLI version string, overridable at build time via ldflags.
var CLIVersion = "v0.1.0-dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI and controller versions",
		RunE:  runVersion,
	}
}

func runVersion(cmd *cobra.Command, _ []string) error {
	cliVer := buildInfoVersion()
	controllerVer := "unknown"

	// Best-effort: try to read from the kardinal-version ConfigMap.
	client, _, err := buildClient()
	if err == nil {
		var cm corev1.ConfigMap
		if err := client.Get(context.Background(),
			types.NamespacedName{
				Namespace: "kardinal-system",
				Name:      "kardinal-version",
			}, &cm); err == nil {
			if v, ok := cm.Data["version"]; ok && v != "" {
				controllerVer = v
			}
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "CLI:        %s\n", cliVer)
	fmt.Fprintf(cmd.OutOrStdout(), "Controller: %s\n", controllerVer)
	return nil
}

// buildInfoVersion returns the version from embedded build info, falling back
// to the static CLIVersion constant.
func buildInfoVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return CLIVersion
}
