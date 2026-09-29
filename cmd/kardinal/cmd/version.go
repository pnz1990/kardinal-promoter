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
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// CLIVersion is the static CLI version string, overridable at build time via ldflags.
var CLIVersion = "v0.1.0-dev"

func newVersionCmd() *cobra.Command {
	var controllerNS string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI, controller, and kro (Graph) versions",
		Long: `Print the CLI version, the controller version (the kardinal-version ConfigMap
the controller writes to its namespace) and the kro version (the image tag of
the kro controller in kro-system). Cluster versions show as unknown when the
cluster cannot be reached.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			controllerVer, graphVer := "unknown", ""
			// Best-effort: without a cluster the CLI version is still printed.
			if c, _, err := buildClient(); err == nil {
				controllerVer, graphVer = clusterVersions(context.Background(), c, controllerNS)
			}
			return versionFn(cmd.OutOrStdout(), controllerVer, graphVer)
		},
	}
	cmd.Flags().StringVar(&controllerNS, "controller-namespace", defaultControllerNamespace,
		"Namespace kardinal-promoter is installed in")
	return cmd
}

// clusterVersions returns the controller version from the kardinal-version
// ConfigMap in controllerNS ("unknown" when absent) and the kro version the
// way doctor finds it ("" when kro is not found).
func clusterVersions(ctx context.Context, c sigs_client.Reader, controllerNS string) (string, string) {
	controllerVer := controllerVersion(ctx, c, controllerNS)
	graphVer, _, _ := kroVersion(ctx, c)
	if graphVer != "" {
		graphVer = "kro " + graphVer
	}
	return controllerVer, graphVer
}

// controllerVersion reads the kardinal-version ConfigMap the controller
// writes to its namespace at start-up; "unknown" when it is absent.
func controllerVersion(ctx context.Context, c sigs_client.Reader, controllerNS string) string {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: controllerNS, Name: "kardinal-version"}, &cm); err == nil {
		if v := cm.Data["version"]; v != "" {
			return v
		}
	}
	return "unknown"
}

// versionFn is the testable implementation of the version command.
// It writes CLI, Controller, and Graph version lines to w.
// controllerVer is the controller version resolved from the ConfigMap ("unknown" if absent).
// graphVer is the kro version (empty string when unavailable).
func versionFn(w interface{ Write([]byte) (int, error) }, controllerVer, graphVer string) error {
	cliVer := buildInfoVersion()

	if controllerVer == "" {
		controllerVer = "unknown"
	}
	graphDisplay := graphVer
	if graphDisplay == "" {
		graphDisplay = "(unknown)"
	}

	if _, err := fmt.Fprintf(w, "CLI:        %s\n", cliVer); err != nil {
		return fmt.Errorf("write version cli: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Controller: %s\n", controllerVer); err != nil {
		return fmt.Errorf("write version controller: %w", err)
	}
	if _, err := fmt.Fprintf(w, "Graph:      %s\n", graphDisplay); err != nil {
		return fmt.Errorf("write version graph: %w", err)
	}
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
