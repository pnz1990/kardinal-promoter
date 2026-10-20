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
	"math"
	"os"
	"slices"
	"strconv"
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
  kardinal quickstart --repo <your fork>                 try it on a local cluster
  kardinal init --name my-app --repo <gitops repo>       write a Pipeline for your repository
  kardinal ship my-app --image app:1.4 --wait            ship a version and follow it
  kardinal why my-app prod                               why it waits, and what moves it on`,
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
		"Output format: table (default), json, yaml") // the commands that take each are filled in by outputUsage

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
	add(groupStart, newQuickstartCmd(), newInitCmd(), newDoctorCmd(), newDashboardCmd(), newVersionCmd())
	add(groupShip, newShipCmd(), newCreateCmd(), newStatusCmd(), newWhyCmd(), newExplainCmd(), newHistoryCmd(), newLogsCmd(), newDiffCmd(), newGetCmd())
	add(groupGate, newInboxCmd(), newApproveCmd(), newVerifyCmd(), newOverrideCmd(), newPromoteCmd(), newPolicyCmd())
	add(groupIncident, newPauseCmd(), newResumeCmd(), newFreezeCmd(), newUnfreezeCmd(), newRollbackCmd(), newHoldCmd(), newReleaseHoldCmd(), newRejectCmd())
	add(groupAdmin, newValidateCmd(), newAuditCmd(), newVerifyReceiptCmd(), newMetricsCmd(), newRefreshCmd(), newDeleteCmd(), newMCPCmd(), newCompletionCmd())
	root.SetHelpCommandGroupID(groupAdmin)
	root.SetCompletionCommandGroupID(groupAdmin)

	requireSubcommand(root)
	addNameCompletion(root)
	root.PersistentFlags().Lookup("output").Usage = outputUsage(root)
	return root
}

// outputUsage is -o's help, with the commands that take json and yaml
// (outputAnnotation) and wide (wideAnnotation), from the commands
// themselves.
func outputUsage(root *cobra.Command) string {
	var structured, wide []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden {
				continue
			}
			path := strings.TrimPrefix(sub.CommandPath(), root.Name()+" ")
			if sub.Annotations[outputAnnotation] == "true" {
				structured = append(structured, path)
			}
			if sub.Annotations[wideAnnotation] == "true" {
				wide = append(wide, path)
			}
			walk(sub)
		}
	}
	walk(root)
	slices.Sort(structured)
	slices.Sort(wide)
	u := "Output format: table (default), json, yaml"
	if len(wide) > 0 {
		u += ", wide"
	}
	u += " (json and yaml: " + strings.Join(structured, ", ")
	if len(wide) > 0 {
		u += "; wide: " + strings.Join(wide, ", ")
	}
	return u + ")"
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

// wideAnnotation marks a command whose table has more columns with -o wide.
const wideAnnotation = "kardinal.io/wide-output"

// checkOutputFlag rejects an unknown -o value, and json or yaml on a command
// that only prints tables.
func checkOutputFlag(cmd *cobra.Command) error {
	switch OutputFormat() {
	case "", "table":
		return nil
	case "wide":
		if cmd.Annotations[wideAnnotation] == "true" {
			return nil
		}
		return fmt.Errorf("-o wide is not supported by %q; it prints one table", cmd.CommandPath())
	case "json", "yaml":
		if cmd.Annotations[outputAnnotation] == "true" {
			return nil
		}
		return fmt.Errorf("-o %s is not supported by %q; it prints a table", globalOutput, cmd.CommandPath())
	default:
		return fmt.Errorf("invalid -o %q: must be table, json, yaml or wide", globalOutput)
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

// newClient constructs a controller-runtime client from the persistent
// flags (clusterClient); a test replaces it with a fake one.
var newClient = clusterClient

// buildClient is the client of a command that talks to the cluster
// (newClient); the first one a command builds also checks the controller's
// version against the CLI's (versionSkew).
func buildClient() (sigs_client.Client, string, error) {
	c, ns, err := newClient()
	if err == nil {
		skewCheck.check(c)
	}
	return c, ns, err
}

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
	// -n, else KARDINAL_NAMESPACE or .kardinal.yaml, else the kubeconfig's.
	ns := globalNamespace
	if ns == "" {
		def, source, err := defaultNamespace()
		if err != nil {
			return nil, "", err
		}
		noticeDefault("namespace", def, source)
		ns = def
	}
	return resolveRestConfig(clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides),
		globalKubeconfig != "", ns)
}

// cliQPS and cliBurst are the CLI's client rate limit (resolveRestConfig),
// unless KARDINAL_KUBE_QPS and KARDINAL_KUBE_BURST set it.
const (
	cliQPS   = 50
	cliBurst = 300
)

// cliRateLimit is the CLI's client rate limit: cliQPS and cliBurst, or the
// positive numbers of KARDINAL_KUBE_QPS (requests a second, may have a
// fraction) and KARDINAL_KUBE_BURST (requests) in getenv.
func cliRateLimit(getenv func(string) string) (float32, int, error) {
	qps, burst := float32(cliQPS), cliBurst
	if v := getenv("KARDINAL_KUBE_QPS"); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil || !(f > 0) || math.IsInf(f, 0) {
			return 0, 0, fmt.Errorf("KARDINAL_KUBE_QPS=%q: want the requests a second the CLI may send, a positive number such as 20", v)
		}
		qps = float32(f)
	}
	if v := getenv("KARDINAL_KUBE_BURST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, fmt.Errorf("KARDINAL_KUBE_BURST=%q: want the most requests the CLI may send at once, a whole number from 1", v)
		}
		burst = n
	}
	return qps, burst, nil
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

	// client-go's default rate limit (5 requests a second, bursts of 10)
	// throttles a paged read: kardinal audit evidence-pack of 100,000
	// AuditEvents lists 200 pages of 500, at least 38 s at 5 a second, and
	// took 39.6 s live (#2047). The API server's priority and fairness
	// protects it from clients; 50 a second with bursts of 300 lets a CLI
	// page at the server's pace. KARDINAL_KUBE_QPS and KARDINAL_KUBE_BURST
	// set another limit, for a small or busy control plane.
	if cfg.QPS == 0 && cfg.RateLimiter == nil {
		qps, burst, err := cliRateLimit(os.Getenv)
		if err != nil {
			return nil, "", err
		}
		cfg.QPS, cfg.Burst = qps, burst
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
const rootReference = `Defaults for a repository: in an application's repository the pipeline and the
namespace need not be repeated. A command that takes <pipeline> [<env>]
(why, explain, history, logs, ship, approve, promote, rollback, hold, unhold,
pause, resume, override, verify) takes, in order: the argument, then
KARDINAL_PIPELINE, then the pipeline of .kardinal.yaml; the namespace is -n,
then KARDINAL_NAMESPACE, then the file's namespace, then the kubeconfig
context's. The file is the nearest .kardinal.yaml from the working
directory up to the root of the git repository (outside a repository, the
working directory's only):

  pipeline: my-app
  namespace: team-a

With a default pipeline, no argument is that pipeline, and one argument is
an environment when the default pipeline has an environment of that name
(kardinal why prod), else a pipeline (kardinal why other-app); when the
cluster cannot say (the default pipeline cannot be read), the command
refuses rather than guess. Each default a command uses is said once on
stderr with its source ("using namespace team-a (from
/src/app/.kardinal.yaml)"), so a value the command line does not show is
never silent. The file may set pipeline and namespace only: it never names
a context or a cluster, and any other key is an error; -n (or
KARDINAL_NAMESPACE) and an explicit pipeline go without a broken file.
kardinal mcp's tools never read the file: the agent's working directory
may be a repository it does not trust.

Output follows where it goes. On a terminal, each state reads as a glyph and
its label in the status colours of the UI, from its design tokens
(web/tokens/tokens.json): Live (spruce), Promoting and Checking health
(thread red), Waiting for PR merge and gates not ready yet (dusk blue), Held
by gate (ochre), Failed (on garnet, the only filled state), Replaced by newer
version and Not reached (grey); Paused and Held by rollback are bold.
Without COLORTERM=truecolor and a COLORFGBG that names the background, they
are the nearest of the 16 ANSI colours, which the terminal's theme adapts.
Without a UTF-8 locale the glyphs are ASCII (` + "`* Live`, `~ Waiting for PR merge`" + `).

Piped to a file or another program, or with NO_COLOR (any value) or
TERM=dumb, output is plain words: the labels without glyphs or colour. The
cluster's text (a step's message, a Pipeline's summary) passes through as it
is when the locale is UTF-8 (LC_ALL, LC_CTYPE or LANG); without a UTF-8
locale, or with TERM=dumb, everything is ASCII (a dash is "-", an arrow
"->", any other non-ASCII character "?"). On Windows, colour needs a console
with virtual-terminal processing; without it output has no colour.

-o json and -o yaml print the API's values (Verified, WaitingForMerge) and the
cluster's text unchanged, and no table placeholders ("-", "--", "..."): a
field with nothing to say is left out. With -w, -o json prints one document
per refresh and -o yaml separates them with "---". Hints, prompts and the
CLI's own warnings go to stderr, so stdout stays valid JSON. The notices a
report shows about what it read (the ERROR: and WARNING: lines of status,
get pipelines and explain) are part of its output, on stdout, so
kardinal status my-app | less shows them; with -o json they are fields. get prints the objects as the API server
lists them. The others print these fields (in brackets the ones left out
when empty):

  create bundle, promote, rollback:
    bundle, pipeline, namespace, url, [type, version]; promote adds
    environment and from (the Bundle copied); rollback adds environment,
    rollbackOf (the Bundle restored), [replaces] (the one replaced) and,
    with --hold, hold {environment, bundle, [reason, expiresAt]}
  create bundle --dry-run:
    pipeline, namespace, nodes, environments [{name, [gates]}] in promotion
    order, [type, version, compact]
  unhold (release-hold):
    pipeline, namespace, environment, bundle (the version it was held on),
    url, [createdBy, reason]
  status:
    controller, pipelines, bundles, activeBundles, [degraded]
    (namespace/name)
  status <pipeline>:
    pipeline, namespace, source ("controller" or "cli"), environments,
    blockingGates, [phase, paused, summary, notices, warnings, regions];
    an environment is status.environments' entry: name, [bundle, version,
    incomingBundle, incomingVersion, state, since, blockedBy, prURL, held];
    a gate: name, instance, environment, bundle, expression, [message,
    reason, lastEvaluatedAt]
  explain:
    pipeline, namespace, entries [{environment, bundle, type (Step or
    PolicyGate), name, state, [expression, reason]}], [holds
    [{environment, bundle, [createdBy, reason, bundleMissing]}]]
  history:
    a list of {bundle, action (promote or rollback), environment, state,
    createdAt, [prURL, durationSeconds, running]}: durationSeconds once the
    step ended, running while it runs
  ship:
    the create bundle fields and exitCode, with --wait also [result,
    message, environments]

The CLI sends at most 50 requests a second to the API server, in bursts of
up to 300; KARDINAL_KUBE_QPS and KARDINAL_KUBE_BURST set other numbers, for
a small or busy control plane (KARDINAL_KUBE_QPS=10 KARDINAL_KUBE_BURST=20).

url is the pipeline in the UI: KARDINAL_UI_URL, else the address kardinal
dashboard opens (http://localhost:8082/ui/). For example
` + "`kardinal create bundle my-app --image app:1.4 -o json | jq -r .bundle`" + `
names the new Bundle.`
