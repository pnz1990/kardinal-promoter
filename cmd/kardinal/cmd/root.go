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
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var (
	rootScheme = runtime.NewScheme()

	globalNamespace  string
	globalKubeconfig string
	globalContext    string
	globalOutput     string // output format: "" (table), "json", "yaml"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(rootScheme))
	utilruntime.Must(v1alpha1.AddToScheme(rootScheme))
}

// NewRootCmd constructs and returns the root cobra command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "kardinal",
		Short: "kardinal manages promotion pipelines on Kubernetes",
		Long: `kardinal is the CLI for kardinal-promoter.
It communicates with the Kubernetes API server to read and write CRDs.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return checkOutputFlag(cmd)
		},
	}

	// Persistent flags available to all subcommands.
	root.PersistentFlags().StringVarP(&globalNamespace, "namespace", "n", "",
		"Kubernetes namespace (default: current context namespace)")
	root.PersistentFlags().StringVar(&globalKubeconfig, "kubeconfig", "",
		"Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)")
	root.PersistentFlags().StringVar(&globalContext, "context", "",
		"Kubeconfig context override")
	root.PersistentFlags().StringVarP(&globalOutput, "output", "o", "",
		"Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newExplainCmd())
	root.AddCommand(newCreateCmd())
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newPauseCmd())
	root.AddCommand(newResumeCmd())
	root.AddCommand(newOverrideCmd())
	root.AddCommand(newPolicyCmd())
	root.AddCommand(newHistoryCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newMetricsCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newRefreshCmd())
	root.AddCommand(newDashboardCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newAuditCmd())
	root.AddCommand(newValidateCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newCompletionCmd())

	requireSubcommand(root)
	return root
}

// outputAnnotation marks a command that honours -o json|yaml.
const outputAnnotation = "kardinal.io/structured-output"

// checkOutputFlag rejects an unknown -o value, and json or yaml on a command
// that only prints tables.
func checkOutputFlag(cmd *cobra.Command) error {
	switch OutputFormat() {
	case "", "table":
		return nil
	case "json", "yaml":
		if cmd.Annotations[outputAnnotation] == "true" {
			return nil
		}
		return fmt.Errorf("-o %s is not supported by %q; it prints a table", globalOutput, cmd.CommandPath())
	default:
		return fmt.Errorf("invalid -o %q: must be table, json or yaml", globalOutput)
	}
}

// requireSubcommand gives every group command (get, create, policy, ...) a
// RunE that fails on an unknown subcommand. Without it cobra prints the group
// help and exits 0 for a typo such as "get bundel".
func requireSubcommand(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		requireSubcommand(sub)
	}
	if !cmd.HasParent() || !cmd.HasSubCommands() || cmd.Runnable() {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return c.Help()
		}
		if c.SuggestionsMinimumDistance <= 0 {
			c.SuggestionsMinimumDistance = 2
		}
		msg := fmt.Sprintf("unknown command %q for %q", args[0], c.CommandPath())
		if suggestions := c.SuggestionsFor(args[0]); len(suggestions) > 0 {
			msg += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
		}
		return errors.New(msg)
	}
}

// buildClient constructs a controller-runtime client from the persistent flags.
// Returns actionable error messages with hints for common failures (#688).
func buildClient() (sigs_client.Client, string, error) {
	cfg, ns, err := buildRestConfig()
	if err != nil {
		return nil, "", err
	}
	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: rootScheme})
	if err != nil {
		return nil, "", fmt.Errorf(
			"failed to create Kubernetes client — check cluster connectivity: %w", err)
	}
	return c, ns, nil
}

// buildRestConfig resolves the rest config and namespace from the persistent
// flags and the kubeconfig, falling back to in-cluster config only when there
// is no kubeconfig at all.
func buildRestConfig() (*rest.Config, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		loadingRules.ExplicitPath = globalKubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if globalContext != "" {
		overrides.CurrentContext = globalContext
	}
	return resolveRestConfig(clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides),
		globalKubeconfig != "", globalNamespace)
}

func resolveRestConfig(clientConfig clientcmd.ClientConfig, explicitKubeconfig bool,
	namespace string) (*rest.Config, string, error) {
	cfg, err := clientConfig.ClientConfig()
	if err != nil {
		// A broken file or an unknown context is reported as is; only a
		// missing kubeconfig falls back to the pod's service account.
		if explicitKubeconfig || !clientcmd.IsEmptyConfig(err) {
			return nil, "", fmt.Errorf("load kubeconfig — run 'kardinal doctor' to diagnose: %w", err)
		}
		cfg, err = rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf(
				"cannot connect to cluster: no kubeconfig and not running in a pod — run 'kardinal doctor' to diagnose\n"+
					"  (underlying error: %w)", err)
		}
	}

	// Namespace: the flag, else the context's namespace, else default.
	ns := namespace
	if ns == "" {
		ns, _, err = clientConfig.Namespace()
		if err != nil && !clientcmd.IsEmptyConfig(err) {
			return nil, "", fmt.Errorf("resolve namespace from kubeconfig: %w", err)
		}
		if ns == "" {
			ns = "default"
		}
	}
	return cfg, ns, nil
}
