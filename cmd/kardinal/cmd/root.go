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
		Long: `kardinal promotes versions of your application through environments, with
gates, pull requests and health checks, on Kubernetes.

Getting started:
  kardinal init                                   write a Pipeline for your repository
  kardinal create bundle my-app --image app:1.4   start a promotion
  kardinal status my-app                          see where each version is, and why`,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			useStyle(cmd)
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
		"Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)")

	withReference(root, rootReference)

	root.AddGroup(
		&cobra.Group{ID: groupStart, Title: "Get started:"},
		&cobra.Group{ID: groupShip, Title: "Ship and observe:"},
		&cobra.Group{ID: groupGate, Title: "Approve and gate:"},
		&cobra.Group{ID: groupIncident, Title: "Incident:"},
		&cobra.Group{ID: groupAdmin, Title: "Admin and debug:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add(groupStart, newInitCmd(), newDoctorCmd(), newDashboardCmd(), newVersionCmd())
	add(groupShip, newCreateCmd(), newStatusCmd(), newExplainCmd(), newHistoryCmd(), newLogsCmd(), newDiffCmd(), newGetCmd())
	add(groupGate, newApproveCmd(), newOverrideCmd(), newPromoteCmd(), newPolicyCmd())
	add(groupIncident, newPauseCmd(), newResumeCmd(), newRollbackCmd(), newReleaseHoldCmd(), newRejectCmd())
	add(groupAdmin, newValidateCmd(), newAuditCmd(), newMetricsCmd(), newRefreshCmd(), newDeleteCmd(), newCompletionCmd())
	root.SetHelpCommandGroupID(groupAdmin)
	root.SetCompletionCommandGroupID(groupAdmin)

	requireSubcommand(root)
	return root
}

// The groups of kardinal --help.
const (
	groupStart    = "start"
	groupShip     = "ship"
	groupGate     = "gate"
	groupIncident = "incident"
	groupAdmin    = "admin"
)

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

// buildClient constructs a controller-runtime client from the persistent
// flags (clusterClient); a test replaces it with a fake one.
var buildClient = clusterClient

// clusterClient constructs a controller-runtime client from the persistent flags.
// Returns actionable error messages with hints for common failures (#688).
func clusterClient() (sigs_client.Client, string, error) {
	cfg, ns, err := buildRestConfig()
	if err != nil {
		return nil, "", err
	}
	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: rootScheme})
	if err != nil {
		return nil, "", fmt.Errorf(
			"failed to create Kubernetes client (check cluster connectivity): %w", err)
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
			return nil, "", fmt.Errorf("load kubeconfig (run 'kardinal doctor' to diagnose): %w", err)
		}
		cfg, err = rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf(
				"cannot connect to cluster: no kubeconfig and not running in a pod; run 'kardinal doctor' to diagnose\n"+
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

// rootReference is the detail of kardinal for its reference page: how the
// output looks where it goes, and the JSON it prints.
const rootReference = `Output follows where it goes. On a terminal, each state reads as a glyph and
its label in the status colours of the UI, from its design tokens
(web/tokens/tokens.json): Live (spruce), Promoting and Checking health
(thread red), Waiting for PR merge and gates not ready yet (dusk blue), Held
by gate (ochre), Failed (on garnet, the only filled state), Replaced by newer
version and Not reached (grey); Paused and Held by rollback are bold.
Without COLORTERM=truecolor and a COLORFGBG that names the background, they
are the nearest of the 16 ANSI colours, which the terminal's theme adapts.
Without a UTF-8 locale the glyphs are ASCII (` + "`* Live`, `~ Waiting for PR merge`" + `).

Piped to a file or another program, or with NO_COLOR (any value) or
TERM=dumb, output is plain words: the labels without glyphs or colour, and
ASCII only, also for what the cluster says (a dash is "-", an arrow "->",
any other non-ASCII character "?"). On Windows, colour needs a console with
virtual-terminal processing; without it output has no colour.

-o json and -o yaml print the API's values (Verified, WaitingForMerge) and the
cluster's text unchanged. Warnings and hints go to stderr, so stdout stays
valid JSON. get prints the objects as the API server lists them. The others
print:

  create bundle, promote, rollback:
    bundle, pipeline, namespace, type, version, url; promote adds
    environment and from (the Bundle copied); rollback adds environment,
    rollbackOf (the Bundle restored), replaces (the one replaced) and, with
    --hold, hold {environment, bundle, reason, expiresAt}
  create bundle --dry-run:
    pipeline, namespace, type, version, nodes, compact,
    environments [{name, gates}] in promotion order
  release-hold:
    pipeline, namespace, environment, bundle (the rollback it was held on),
    createdBy, reason, url
  status:
    controller, pipelines, degraded [namespace/name], bundles, activeBundles
  status <pipeline>:
    pipeline, namespace, phase, paused, summary, source ("controller" or
    "cli"), environments (status.environments: name, bundle, version,
    incomingBundle, incomingVersion, state, since, blockedBy, prURL, held),
    blockingGates [{name, instance, environment, bundle, expression, message,
    reason, lastEvaluatedAt}], warnings
  explain:
    pipeline, namespace, entries [{environment, bundle, type (Step or
    PolicyGate), name, state, expression, reason}], holds [{environment,
    bundle, createdBy, reason, bundleMissing}]
  history:
    a list of {bundle, action (promote or rollback), environment, state,
    prURL, duration, createdAt}

url is the pipeline in the UI: KARDINAL_UI_URL, else the address kardinal
dashboard opens (http://localhost:8082/ui/). For example
` + "`kardinal create bundle my-app --image app:1.4 -o json | jq -r .bundle`" + `
names the new Bundle.`
