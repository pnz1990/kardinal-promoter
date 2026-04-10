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
	"fmt"
	"os"
	"path/filepath"

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
	}

	// Persistent flags available to all subcommands.
	root.PersistentFlags().StringVarP(&globalNamespace, "namespace", "n", "",
		"Kubernetes namespace (default: current context namespace)")
	root.PersistentFlags().StringVar(&globalKubeconfig, "kubeconfig", defaultKubeconfig(),
		"Path to kubeconfig file")
	root.PersistentFlags().StringVar(&globalContext, "context", "",
		"Kubeconfig context override")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newExplainCmd())

	return root
}

// buildClient constructs a controller-runtime client from the persistent flags.
func buildClient() (sigs_client.Client, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		loadingRules.ExplicitPath = globalKubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if globalContext != "" {
		overrides.CurrentContext = globalContext
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, overrides,
	).ClientConfig()
	if err != nil {
		// Fall back to in-cluster config.
		cfg, err = rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf("build kubeconfig: %w", err)
		}
	}

	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: rootScheme})
	if err != nil {
		return nil, "", fmt.Errorf("create k8s client: %w", err)
	}

	// Resolve namespace.
	ns := globalNamespace
	if ns == "" {
		ns, _, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, overrides,
		).Namespace()
		if err != nil || ns == "" {
			ns = "default"
		}
	}

	return c, ns, nil
}

// defaultKubeconfig returns $KUBECONFIG if set, otherwise ~/.kube/config.
func defaultKubeconfig() string {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return kc
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}
