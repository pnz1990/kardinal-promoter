//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The tests in this file run CLI commands that need little or no cluster
// state: version, the global flags, completion, init, validate, policy test,
// doctor, dashboard and refresh.

// cliVersionFlag is the -X flag .github/workflows/release.yml stamps the CLI
// version with.
const cliVersionFlag = "-X github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/cmd.CLIVersion="

// noCluster runs the CLI with no kubeconfig at all.
var noCluster = framework.CLIOptions{Kubeconfig: "/dev/null"}

// unreachableKubeconfig points at a port nothing listens on.
const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: down
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: down
  context:
    cluster: down
    user: down
current-context: down
users:
- name: down
  user:
    token: none
`

// kroTag is the image tag of the kro controller in kro-system, where version
// and doctor look for it.
func kroTag(t *testing.T, e *framework.Env) string {
	t.Helper()
	pods, err := e.Kube.CoreV1().Pods("kro-system").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	for _, p := range pods.Items {
		for _, c := range p.Spec.Containers {
			repo, tag, ok := strings.Cut(c.Image[strings.LastIndex(c.Image, "/")+1:], ":")
			if ok && repo == "kro" {
				return tag
			}
		}
	}
	t.Fatal("no kro container in kro-system")
	return ""
}

// controllerVersion is the version the controller wrote to its
// kardinal-version ConfigMap.
func controllerVersion(t *testing.T, e *framework.Env) string {
	t.Helper()
	var cm corev1.ConfigMap
	require.NoError(t, e.Client.Get(context.Background(),
		types.NamespacedName{Namespace: framework.ControllerNamespace, Name: "kardinal-version"}, &cm))
	require.NotEmpty(t, cm.Data["version"], "the controller writes its version at start-up")
	return cm.Data["version"]
}

// barePipeline creates a one-environment Pipeline that never promotes: no
// Bundle is ever created for it.
func barePipeline(t *testing.T, e *framework.Env, ns, name string, edit ...func(*v1alpha1.Pipeline)) *v1alpha1.Pipeline {
	t.Helper()
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			// A repository per namespace: bare Pipelines of other tests at
			// the same path would get PathConflict, a status change.
			Git:          v1alpha1.PipelineGit{URL: "https://git.example/" + ns + "/" + name},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Path: "environments/test"}},
		},
	}
	for _, f := range edit {
		f(p)
	}
	require.NoError(t, e.Client.Create(context.Background(), p))
	return p
}

// waitPipelineValid waits until the controller has accepted the Pipeline:
// Ready=True/Valid at its generation, and phase Unknown, the phase before the
// first Bundle.
func waitPipelineValid(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	framework.Eventually(t, time.Minute, "Pipeline "+name+" valid", func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionTrue && c.Reason == "Valid" && c.ObservedGeneration == p.Generation &&
				p.Status.Phase == "Unknown",
			fmt.Sprintf("phase=%q conditions=%v", p.Status.Phase, p.Status.Conditions)
	})
}

// TestCLI_Version checks the three lines of kardinal version. A CLI built
// with release.yml's -X flag prints that version, with or without a cluster.
// With a cluster, Controller is the version in the kardinal-version ConfigMap
// of --controller-namespace (kardinal-system by default) and Graph is the kro
// controller's image tag. Without a cluster, or without the ConfigMap, they
// read unknown and the command still exits 0.
// Covers CLI-VERSION-01, CLI-VERSION-02.
func TestCLI_Version(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ctx := context.Background()
	const offline = "CLI:        v9.9.9-e2e\nController: unknown\nGraph:      (unknown)\n"

	stamped := framework.BuildCLI(t, cliVersionFlag+"v9.9.9-e2e")
	r := cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Bin: stamped}, "version")
	require.Equal(t, 0, r.Code, "version needs no cluster")
	assert.Equal(t, offline, r.Stdout)
	assert.Empty(t, r.Stderr)

	down := filepath.Join(t.TempDir(), "down.kubeconfig")
	require.NoError(t, os.WriteFile(down, []byte(unreachableKubeconfig), 0o600))
	r = cli.Exec(framework.CLIOptions{Kubeconfig: down, Bin: stamped}, "version")
	require.Equal(t, 0, r.Code, "an unreachable cluster is not an error")
	assert.Equal(t, offline, r.Stdout)

	graph := "kro " + kroTag(t, e)
	r = cli.Exec(framework.CLIOptions{Bin: stamped}, "--context", e.Context, "version")
	require.Equal(t, 0, r.Code)
	assert.Equal(t, fmt.Sprintf("CLI:        v9.9.9-e2e\nController: %s\nGraph:      %s\n",
		controllerVersion(t, e), graph), r.Stdout)

	ns := e.Namespace(t)
	require.NoError(t, e.Client.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kardinal-version"},
		Data:       map[string]string{"version": "v7.7.7-e2e"},
	}))
	lines := strings.Split(cli.Must("", "version", "--controller-namespace", ns), "\n")
	require.Len(t, lines, 4, "three lines, each ending in a newline")
	assert.Regexp(t, `^CLI:        \S+$`, lines[0], "the CLI under test prints its version line")
	assert.Equal(t, "Controller: v7.7.7-e2e", lines[1], "--controller-namespace reads that namespace's ConfigMap")
	assert.Equal(t, "Graph:      "+graph, lines[2])

	empty := e.Namespace(t)
	assert.Contains(t, cli.Must("", "version", "--controller-namespace", empty), "\nController: unknown\n",
		"a namespace without the ConfigMap")
}

// TestCLI_GlobalFlags checks how the global flags pick the cluster and the
// namespace. By default the CLI uses $KUBECONFIG's current context and that
// context's namespace; --context picks another context, -n and --namespace
// win over the context's namespace, --kubeconfig wins over $KUBECONFIG, and a
// context without a namespace means "default". An unknown context, a missing
// kubeconfig file and no kubeconfig at all fail and point to kardinal doctor.
// Covers CLI-GLOBAL-01.
func TestCLI_GlobalFlags(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	nsA, nsB := e.Namespace(t), e.Namespace(t)
	barePipeline(t, e, nsA, "alpha")
	barePipeline(t, e, nsB, "beta")

	dir := t.TempDir()
	contexts := map[string]string{"ctx-a": nsA, "ctx-b": nsB}
	kcA, kcB := filepath.Join(dir, "a.kubeconfig"), filepath.Join(dir, "b.kubeconfig")
	e.WriteKubeconfig(t, kcA, contexts, "ctx-a")
	e.WriteKubeconfig(t, kcB, contexts, "ctx-b")

	pipelines := func(kubeconfig string, args ...string) []string {
		t.Helper()
		r := cli.Exec(framework.CLIOptions{Kubeconfig: kubeconfig}, append(args, "get", "pipelines")...)
		require.Equal(t, 0, r.Code, r.Output())
		var names []string
		for _, row := range framework.ParseTable(r.Stdout) {
			names = append(names, row["PIPELINE"])
		}
		return names
	}
	assert.Equal(t, []string{"alpha"}, pipelines(kcA), "$KUBECONFIG's current context and its namespace")
	assert.Equal(t, []string{"beta"}, pipelines(kcA, "--context", "ctx-b"), "--context")
	assert.Equal(t, []string{"alpha"}, pipelines(kcA, "--context", "ctx-b", "-n", nsA), "-n wins over the context's namespace")
	assert.Equal(t, []string{"beta"}, pipelines(kcA, "--namespace", nsB), "--namespace")
	assert.Equal(t, []string{"beta"}, pipelines(kcA, "--kubeconfig", kcB), "--kubeconfig wins over $KUBECONFIG")

	// The kind-only kubeconfig's context has no namespace.
	r := cli.Run("", "doctor", "--pipeline", "alpha")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stdout, `Pipeline "alpha" not found in namespace "default"`, "no namespace means default")

	const doctorHint = "load kubeconfig — run 'kardinal doctor' to diagnose: "
	r = cli.Exec(framework.CLIOptions{Kubeconfig: kcA}, "--context", "nope", "get", "pipelines")
	assert.Equal(t, 1, r.Code)
	assert.Equal(t, "get pipelines: "+doctorHint+`context "nope" does not exist`+"\n", r.Stderr)
	assert.Empty(t, r.Stdout)

	missing := filepath.Join(dir, "missing.kubeconfig")
	r = cli.Exec(framework.CLIOptions{Kubeconfig: kcA}, "--kubeconfig", missing, "get", "pipelines")
	assert.Equal(t, 1, r.Code)
	assert.True(t, strings.HasPrefix(r.Stderr, "get pipelines: "+doctorHint), r.Stderr)
	assert.Contains(t, r.Stderr, missing)

	r = cli.Exec(noCluster, "get", "pipelines")
	assert.Equal(t, 1, r.Code)
	assert.True(t, strings.HasPrefix(r.Stderr, "get pipelines: cannot connect to cluster: no kubeconfig and not "+
		"running in a pod — run 'kardinal doctor' to diagnose\n"), r.Stderr)
}

// bashCompletionDriver sources the bash script from $1 and completes each
// command line the way bash does on <Tab>, printing the candidates as
// [a][b]. _get_comp_words_by_ref stands in for the bash-completion package
// the script needs.
const bashCompletionDriver = `
_get_comp_words_by_ref() { cur=${COMP_WORDS[COMP_CWORD]}; prev=${COMP_WORDS[COMP_CWORD-1]}; words=("${COMP_WORDS[@]}"); cword=$COMP_CWORD; }
source "$1" || exit 3
complete -p kardinal
comp() {
  COMP_WORDS=("$@"); COMP_CWORD=$(($# - 1)); COMP_LINE="$*"; COMP_POINT=${#COMP_LINE}; COMPREPLY=()
  __start_kardinal 2>/dev/null
  printf '[%s]' "${COMPREPLY[@]}"; echo
}
comp kardinal get ""
comp kardinal pol
comp kardinal get pipelines --out
comp kardinal completion ""
`

// TestCLI_Completion checks kardinal completion. The bash script, sourced as
// the help says (with the bash-completion function it needs), registers
// kardinal and completes commands, subcommands, flags and completion's own
// shells through the CLI's __complete protocol. The zsh script parses, and
// the fish and PowerShell scripts are generated for kardinal and call
// __complete too (neither shell is on the test host, so they are not run).
// An unknown shell or no shell fails.
// Covers CLI-COMPLETION-01.
func TestCLI_Completion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	dir := t.TempDir()

	scripts := map[string]string{}
	for shell, marker := range map[string]string{
		"bash":       "# bash completion V2 for kardinal",
		"zsh":        "#compdef kardinal",
		"fish":       "# fish completion for kardinal",
		"powershell": "# powershell completion for kardinal",
	} {
		r := cli.Exec(noCluster, "completion", shell)
		require.Equal(t, 0, r.Code, "%s: %s", shell, r.Stderr)
		assert.True(t, strings.HasPrefix(r.Stdout, marker), "%s script starts with %q", shell, marker)
		assert.Contains(t, r.Stdout, "__complete", "%s script asks the CLI for candidates", shell)
		scripts[shell] = filepath.Join(dir, "kardinal."+shell)
		require.NoError(t, os.WriteFile(scripts[shell], []byte(r.Stdout), 0o600))
	}
	assert.Contains(t, readFile(t, scripts["fish"]), "complete -c kardinal ")
	assert.Contains(t, readFile(t, scripts["powershell"]), "Register-ArgumentCompleter -CommandName 'kardinal'")

	for _, shell := range []string{"bash", "zsh"} {
		out, err := exec.Command(shell, "-n", scripts[shell]).CombinedOutput()
		assert.NoError(t, err, "%s -n: %s", shell, out)
	}

	// The script runs "kardinal __complete ..." from PATH, as after an install.
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	bin, err := exec.LookPath(cli.Path())
	require.NoError(t, err)
	bin, err = filepath.Abs(bin)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(bin, filepath.Join(binDir, "kardinal")))
	sh := exec.Command("bash", "--norc", "--noprofile", "-c", bashCompletionDriver, "driver", scripts["bash"])
	sh.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "KUBECONFIG=/dev/null")
	out, err := sh.Output()
	require.NoError(t, err, "bash driver: %s", out)
	t.Logf("bash completion:\n%s", out)
	assert.Equal(t, "complete -o default -F __start_kardinal kardinal\n"+
		"[auditevents][bundles][pipelines][steps][subscriptions]\n"+
		"[policy]\n"+
		"[--output]\n"+
		"[bash][zsh][fish][powershell]\n", string(out))

	r := cli.Exec(noCluster, "__complete", "get", "")
	require.Equal(t, 0, r.Code)
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, ":4", lines[len(lines)-1], "ShellCompDirectiveNoFileComp")
	assert.Contains(t, lines, "pipelines\tList Pipelines")

	r = cli.Exec(noCluster, "completion", "tcsh")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, `invalid argument "tcsh" for "kardinal completion"`)
	r = cli.Exec(noCluster, "completion")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "accepts 1 arg(s), received 0")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// TestCLI_Init runs the init wizard with typed answers. --stdout prints the
// Pipeline YAML (the prompts go to stderr), --file writes it (the default is
// pipeline.yaml), and kubectl apply of that file gives a Pipeline the
// controller reconciles to Ready. --scaffold-gitops writes one
// kustomization.yaml per environment under --gitops-dir and keeps existing
// files on a re-run, --demo scaffolds the kardinal-test-app placeholder,
// and helm skips the Kustomize scaffold. A URL that is not https:// is asked
// again; end of input without one fails.
// Covers CLI-INIT-01.
func TestCLI_Init(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ns := e.Namespace(t)
	answers := func(strategy string) string {
		return "web\n" + ns + "\ntest, prod\nhttps://git.example/o/r\nrelease\n" + strategy + "\n"
	}
	init := func(dir, stdin string, args ...string) framework.CLIResult {
		t.Helper()
		return cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Dir: dir, Stdin: stdin}, append([]string{"init"}, args...)...)
	}

	// --stdout: stdout is the YAML only.
	dir := t.TempDir()
	r := init(dir, answers("kustomize"), "--stdout")
	require.Equal(t, 0, r.Code, r.Stderr)
	yaml := r.Stdout
	assert.True(t, strings.HasPrefix(yaml, "# Generated by kardinal init\n"), yaml)
	for _, want := range []string{"  name: web\n", "  namespace: " + ns + "\n", "    url: https://git.example/o/r\n",
		"    branch: release\n", "      name: github-token\n",
		"  - name: test\n    approval: auto\n    path: environments/test\n    update:\n      strategy: kustomize\n",
		"  - name: prod\n    approval: pr-review\n    path: environments/prod\n"} {
		assert.Contains(t, yaml, want)
	}
	for _, prompt := range []string{"Application name [my-app]: ", "Namespace [default]: ",
		"Environments (comma-separated) [test,uat,prod]: ", "Git repository URL: ", "Base branch [main]: ",
		"Update strategy (kustomize/helm) [kustomize]: "} {
		assert.Contains(t, r.Stderr, prompt)
	}
	assert.NoFileExists(t, filepath.Join(dir, "pipeline.yaml"), "--stdout writes no file")

	// The default file, applied as init says, is a valid Pipeline.
	r = init(dir, answers("kustomize"))
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "Pipeline YAML written to pipeline.yaml\nApply with: kubectl apply -f pipeline.yaml\n", r.Stdout)
	assert.Equal(t, yaml, readFile(t, filepath.Join(dir, "pipeline.yaml")), "the file holds the --stdout YAML")
	out, err := cli.Kubectl(dir, "apply", "-f", "pipeline.yaml")
	require.NoError(t, err, out)
	waitPipelineValid(t, e, ns, "web")

	// --file into a new directory, with a scaffold under --gitops-dir.
	r = init(dir, answers("kustomize"), "--file", "deploy/pipeline.yaml", "--scaffold-gitops", "--gitops-dir", "gitops")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "Pipeline YAML written to deploy/pipeline.yaml\nApply with: kubectl apply -f deploy/pipeline.yaml\n"+
		"  created: gitops/environments/test/kustomization.yaml\n"+
		"  created: gitops/environments/prod/kustomization.yaml\n"+
		"GitOps scaffold written to gitops\n", r.Stdout)
	assert.Equal(t, yaml, readFile(t, filepath.Join(dir, "deploy", "pipeline.yaml")))
	for _, env := range []string{"test", "prod"} {
		k := readFile(t, filepath.Join(dir, "gitops", "environments", env, "kustomization.yaml"))
		assert.Contains(t, k, "kind: Kustomization\n")
		assert.Contains(t, k, "images:\n  - name: REPLACE_ME\n    newTag: latest\n")
	}

	// A re-run keeps an edited overlay.
	edited := filepath.Join(dir, "gitops", "environments", "test", "kustomization.yaml")
	require.NoError(t, os.WriteFile(edited, []byte("# mine\n"), 0o600))
	r = init(dir, answers("kustomize"), "--file", "deploy/pipeline.yaml", "--scaffold-gitops", "--gitops-dir", "gitops")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Contains(t, r.Stdout, "  skipped (already exists): gitops/environments/test/kustomization.yaml\n")
	assert.Contains(t, r.Stdout, "  skipped (already exists): gitops/environments/prod/kustomization.yaml\n")
	assert.Equal(t, "# mine\n", readFile(t, edited))

	// --demo alone scaffolds into .gitops; with --stdout the scaffold reports on stderr.
	demo := t.TempDir()
	r = init(demo, answers("kustomize"), "--demo", "--stdout")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, yaml, r.Stdout, "stdout stays the bare YAML")
	assert.Contains(t, r.Stderr, "  created: .gitops/environments/prod/kustomization.yaml\nGitOps scaffold written to .gitops\n")
	assert.Contains(t, readFile(t, filepath.Join(demo, ".gitops", "environments", "prod", "kustomization.yaml")),
		"  - name: ghcr.io/pnz1990/kardinal-test-app\n    newTag: sha-DEMO\n")

	// helm: no Kustomize scaffold.
	helm := t.TempDir()
	r = init(helm, answers("helm"), "--scaffold-gitops")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Contains(t, r.Stdout, "GitOps scaffold skipped: it writes Kustomize overlays, and the update strategy is helm\n")
	assert.Contains(t, readFile(t, filepath.Join(helm, "pipeline.yaml")), "      strategy: helm\n")
	assert.NoDirExists(t, filepath.Join(helm, ".gitops"))

	// Validation: re-prompt, then fail at end of input.
	r = init(t.TempDir(), "\n\n\ngit@example.com:o/r.git\nhttps://git.example/o/r\n\n\n", "--stdout")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, 1, strings.Count(r.Stderr, "  an https:// repository URL is required\n"))
	assert.Contains(t, r.Stdout, "  name: my-app\n  namespace: default\n", "empty answers take the defaults")
	assert.Contains(t, r.Stdout, "  - name: uat\n")
	r = init(t.TempDir(), "\n\n\n\n", "--stdout")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "init wizard: Git repository URL: an https:// repository URL is required\n")
	assert.Empty(t, r.Stdout)
}

// validateCase is one file for TestCLI_Validate.
type validateCase struct {
	name string
	doc  string
	// valid is validate's verdict; want is in its output.
	valid bool
	want  string
	// apiRejects is kubectl apply --dry-run=server's verdict.
	apiRejects bool
	// apiSays is part of validate's message the API server's rejection
	// must contain word for word.
	apiSays string
}

// TestCLI_Validate runs kardinal validate on files the API server rejects,
// on files it accepts but the controller refuses, and on valid ones, and
// compares each verdict with kubectl apply --dry-run=server (what the help
// points to for the schema) and with the controller. Every file the API
// server rejects, validate rejects, except a schema-only error the help says
// validate does not check. For a file the API accepts, validate's error is
// the controller's: the Pipeline's Ready=False message, or the template
// PolicyGate's CEL error. Deprecated fields warn and still pass. Other kinds
// are skipped, and the exit code is 1 on any error. A reserved environment
// name, bundle included, gets the API server's message and nothing about
// the Bundle validate builds internally (#1358).
// Covers CLI-VALIDATE-01, CLI-VALIDATE-02.
func TestCLI_Validate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ns := e.Namespace(t)

	pipeline := func(name, git, envs string) string {
		return "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata:\n  name: " + name + "\n  namespace: " + ns +
			"\nspec:\n  git:\n    url: https://git.example/o/r\n" + git + "  environments:\n" + envs
	}
	gate := func(name, spec string) string {
		return "apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: " + name + "\n  namespace: " + ns +
			"\nspec:\n" + spec
	}
	const envs = "  - name: test\n  - name: prod\n"
	const reservedMsg = "reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro reserved IDs " +
		"(spec, status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; rename the environment"
	cases := []validateCase{
		{name: "valid", doc: pipeline("ok", "", envs) + "---\n" + gate("ok", "  expression: \"!schedule.isWeekend\"\n") +
			"---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n  namespace: " + ns + "\n",
			valid: true, want: "- skipped ConfigMap/other: validate checks only kardinal.io Pipelines and PolicyGates\n"},
		{name: "empty git url", doc: strings.Replace(pipeline("nourl", "", envs), "https://git.example/o/r", `""`, 1),
			want: "  - spec.git.url is required\n", apiRejects: true},
		{name: "no environments", doc: strings.Replace(pipeline("noenv", "", ""), "environments:\n", "environments: []\n", 1),
			want: "  - spec.environments must contain at least one environment\n", apiRejects: true},
		{name: "duplicate environment", doc: pipeline("dup", "", "  - name: test\n  - name: test\n"),
			want: `  - build: environment "test" is declared twice` + "\n", apiRejects: true},
		{name: "spec.policyGates", doc: pipeline("pg", "", envs) + "  policyGates:\n  - name: no-weekend-deploys\n",
			want: "  - spec.policyGates is not implemented; remove it", apiRejects: true},
		{name: "argocd with pr-review", doc: pipeline("argo", "", "  - name: prod\n    approval: pr-review\n"+
			"    update:\n      strategy: argocd\n      argocd:\n        application: web-prod\n"),
			want:       "update.strategy argocd patches the Application directly and cannot honour approval: pr-review",
			apiRejects: true},
		{name: "gate selector", doc: gate("sel", "  expression: \"true\"\n  selector: {}\n"),
			want: "  - spec.selector is not implemented; use the kardinal.io/applies-to label\n", apiRejects: true},
		{name: "gate name of 64", doc: gate(strings.Repeat("g", 64), "  expression: \"true\"\n"),
			want: "has 64 characters: PolicyGate names are at most 63 characters, because the name is copied into the " +
				"kardinal.io/gate-template label of every gate instance; use a name of at most 63 characters\n", apiRejects: true,
			apiSays: "; use a name of at most 63 characters"},
		{name: "gate name of 63", doc: gate(strings.Repeat("g", 63), "  expression: \"true\"\n"), valid: true},
		{name: "deprecated when", doc: gate("when", "  expression: \"true\"\n  when: pre-deploy\n"), valid: true,
			want: "  ! warning: spec.when is deprecated and has no effect"},
		{name: "deprecated provider", doc: pipeline("prov", "    provider: gitlab\n", envs), valid: true,
			want: "  ! warning: spec.git.provider is deprecated and ignored"},
		{name: "autoRollback", doc: pipeline("rollback", "", envs) + "    autoRollback:\n      failureThreshold: 2\n",
			want: `  - environment "prod": environments[].autoRollback is not implemented; remove it`, apiRejects: true},
		{name: "reserved environment name", doc: pipeline("reserved", "", "  - name: spec\n"),
			want: `  - environment "spec": reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro ` +
				`reserved IDs (spec, status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; ` +
				"rename the environment\n",
			apiRejects: true, apiSays: reservedMsg},
		{name: "environment named bundle", doc: pipeline("bundle", "", "  - name: test\n  - name: bundle\n"),
			want: "✗ environment-named-bundle.yaml is invalid:\n" +
				`  - environment "bundle": reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro ` +
				`reserved IDs (spec, status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; ` +
				"rename the environment\n",
			apiRejects: true, apiSays: reservedMsg},
		// The help: "This is not full CRD schema validation".
		{name: "schema enum", doc: pipeline("enum", "", "  - name: test\n    approval: sometimes\n"), valid: true,
			apiRejects: true},
	}
	// The API server accepts these; the controller refuses them. reason and
	// message are the Pipeline's Ready=False condition; a PolicyGate case is
	// checked against the template gate's status.reason.
	controllerCases := []struct {
		validateCase
		reason, message string
	}{
		{validateCase{name: "unknown dependsOn", doc: pipeline("dep", "", "  - name: test\n  - name: prod\n    dependsOn: [staging]\n"),
			want: `  - build: environment "prod" dependsOn unknown environment "staging"` + "\n"},
			"ValidationFailed", `environment "prod" has dependsOn "staging" which does not exist in this pipeline`},
		{validateCase{name: "secretRef in another namespace",
			doc: pipeline("secret", "    secretRef:\n      name: git-token\n      namespace: kardinal-system\n", envs),
			want: `  - git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's ` +
				`namespace "` + ns + `"` + "\n"},
			"ValidationFailed", `git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's ` +
				`namespace "` + ns + `"`},
		{validateCase{name: "two regions", doc: pipeline("regions", "", envs) + "    regions: [us-east-1, eu-west-1]\n",
			want: `  - environment "prod": regions is not supported; declare one environment per region (prod-us, prod-eu) and use wave` + "\n"},
			"NotImplemented", `environment "prod": regions is not supported; declare one environment per region`},
		{validateCase{name: "bad CEL", doc: gate("cel", "  expression: schedule.hour >>> 9\n"),
			want: "  - spec.expression CEL error: ERROR: <input>:1:16: Syntax error: "}, "", ""},
	}
	for _, c := range controllerCases {
		cases = append(cases, c.validateCase)
	}

	dir := t.TempDir()
	validate := func(c validateCase) framework.CLIResult {
		t.Helper()
		file := strings.ReplaceAll(c.name, " ", "-") + ".yaml"
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(c.doc), 0o600))
		r := cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Dir: dir}, "validate", "-f", file)
		if c.valid {
			assert.Equal(t, 0, r.Code, "%s: validate passes", c.name)
			assert.Contains(t, r.Stdout, "✓ "+file+" is valid\n", c.name)
		} else {
			assert.Equal(t, 1, r.Code, "%s: validate fails", c.name)
			assert.Contains(t, r.Stdout, "✗ "+file+" is invalid:\n", c.name)
			assert.Equal(t, "validation failed\n", r.Stderr, c.name)
		}
		assert.Contains(t, r.Stdout, c.want, c.name)
		assert.NotContains(t, r.Stdout, "validate-dummy", "%s: validate never names its internal Bundle", c.name)

		out, err := cli.Kubectl(dir, "apply", "--dry-run=server", "-f", file)
		assert.Equal(t, c.apiRejects, err != nil, "%s: the API server rejects it: %v\n%s", c.name, c.apiRejects, out)
		if c.apiSays != "" {
			assert.Contains(t, out, c.apiSays, "%s: validate words it as the API server does", c.name)
		}
		return r
	}
	for _, c := range cases {
		validate(c)
	}

	// Apply the controller cases and compare with what the controller says.
	for _, c := range controllerCases {
		file := strings.ReplaceAll(c.name, " ", "-") + ".yaml"
		out, err := cli.Kubectl(dir, "apply", "-f", file)
		require.NoError(t, err, out)
		name := regexp.MustCompile(`(?m)^  name: (\S+)$`).FindStringSubmatch(c.doc)[1]
		if c.reason == "" {
			r := validate(c.validateCase)
			celErr := strings.TrimSuffix(r.Stdout[strings.Index(r.Stdout, "CEL error: ")+len("CEL error: "):], "\n")
			framework.Eventually(t, time.Minute, "template gate "+name+" checked", func(ctx context.Context) (bool, string) {
				var g v1alpha1.PolicyGate
				if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &g); err != nil {
					return false, err.Error()
				}
				return strings.TrimSpace(g.Status.Reason) == "CEL syntax error: "+celErr, g.Status.Reason
			})
			continue
		}
		framework.Eventually(t, time.Minute, "Pipeline "+name+" Ready=False/"+c.reason, func(ctx context.Context) (bool, string) {
			var p v1alpha1.Pipeline
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
				return false, err.Error()
			}
			cond := meta.FindStatusCondition(p.Status.Conditions, "Ready")
			if cond == nil {
				return false, "no Ready condition"
			}
			return cond.Status == metav1.ConditionFalse && cond.Reason == c.reason && strings.Contains(cond.Message, c.message),
				fmt.Sprintf("Ready=%s/%s: %s", cond.Status, cond.Reason, cond.Message)
		})
	}

	r := cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Dir: dir}, "validate", "-f", "nope.yaml")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "cannot read nope.yaml")
	r = cli.Exec(noCluster, "validate")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, `required flag(s) "file" not set`)
}

// TestCLI_PolicyTest runs kardinal policy test with no cluster. Each gate's
// expression is checked for syntax and dry-run at the current time: PASS or
// FAIL, or UNKNOWN when it needs cluster data. The summary line counts the
// gates, a file with only other kinds fails, and the exit code is 1 only for
// a syntax error. The syntax check is the controller's: a template gate with
// the same expression, applied to the cluster, gets the same error.
// Covers CLI-POLICY-TEST-01.
func TestCLI_PolicyTest(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ns := e.Namespace(t)
	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	gate := func(name, expr, appliesTo string) string {
		labels := ""
		if appliesTo != "" {
			labels = "  labels:\n    kardinal.io/applies-to: " + appliesTo + "\n"
		}
		return "---\napiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: " + name + "\n  namespace: " + ns +
			"\n" + labels + "spec:\n  expression: " + expr + "\n"
	}
	run := func(file string) framework.CLIResult {
		t.Helper()
		return cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Dir: dir}, "policy", "test", file)
	}

	write("mixed.yaml", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ignored\n"+
		gate("any-hour", `"schedule.hour >= 0"`, "")+
		gate("never", `"schedule.hour > 23"`, "prod")+
		gate("soak", `"upstream.uat.soakMinutes >= 30"`, "prod"))
	r := run("mixed.yaml")
	require.Equal(t, 0, r.Code, "a FAIL is not an error: %s", r.Stderr)
	assert.Equal(t, `PolicyGate "any-hour" (mixed.yaml):
  Expression: schedule.hour >= 0
  Syntax: valid
  Result: PASS (schedule.hour >= 0 = true)

PolicyGate "never" (mixed.yaml):
  Expression: schedule.hour > 23
  Syntax: valid
  Result: FAIL (schedule.hour > 23 = false)

PolicyGate "soak" (mixed.yaml):
  Expression: upstream.uat.soakMinutes >= 30
  Syntax: valid
  Result: UNKNOWN (CEL evaluation error: no such key: uat)

Some gates would BLOCK with current context (see FAIL results above) (3 gate(s))
`, r.Stdout)

	write("unknown.yaml", gate("ok", `"true"`, "")+gate("soak", `"upstream.uat.soakMinutes >= 30"`, ""))
	r = run("unknown.yaml")
	require.Equal(t, 0, r.Code)
	assert.Contains(t, r.Stdout, "  Result: PASS (true = true)\n")
	assert.True(t, strings.HasSuffix(r.Stdout, "All gates valid; some need cluster context to evaluate "+
		"(see UNKNOWN results above) (2 gate(s))\n"), r.Stdout)

	write("pass.yaml", gate("ok", `"true"`, ""))
	r = run("pass.yaml")
	require.Equal(t, 0, r.Code)
	assert.True(t, strings.HasSuffix(r.Stdout, "All gates valid and pass current context (1 gate(s))\n"), r.Stdout)

	const bad = "schedule.hour >>> 9"
	write("bad.yaml", gate("ok", `"true"`, "")+gate("bad", `"`+bad+`"`, ""))
	r = run("bad.yaml")
	assert.Equal(t, 1, r.Code)
	assert.Equal(t, "CEL syntax errors in 1 of 2 gate(s)\n", r.Stderr)
	m := regexp.MustCompile(`(?s)\n  Syntax: INVALID — (.+?)\n\n`).FindStringSubmatch(r.Stdout)
	require.NotNil(t, m, r.Stdout)
	assert.True(t, strings.HasSuffix(r.Stdout, "CEL syntax errors found (2 gate(s))\n"), r.Stdout)

	// The controller's check of the same template gate.
	out, err := cli.Kubectl(dir, "apply", "-f", "bad.yaml")
	require.NoError(t, err, out)
	framework.Eventually(t, time.Minute, "template gate bad checked", func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "bad"}, &g); err != nil {
			return false, err.Error()
		}
		return strings.TrimSpace(g.Status.Reason) == "CEL syntax error: "+m[1], g.Status.Reason
	})

	write("none.yaml", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ignored\n")
	r = run("none.yaml")
	assert.Equal(t, 1, r.Code)
	assert.Equal(t, "parse \"none.yaml\": no PolicyGate resources found in YAML\n", r.Stderr)
	r = run("missing.yaml")
	assert.Equal(t, 1, r.Code)
	assert.Equal(t, "read \"missing.yaml\": open missing.yaml: no such file or directory\n", r.Stderr)
}

// TestCLI_Doctor runs kardinal doctor against the suite's install: every
// check passes and the summary counts them, and the token check is named after
// the controller's SCM provider. --pipeline adds a check of that
// Pipeline in the -n namespace: it passes for a Pipeline the controller
// accepted, and fails with the controller's reason for one it refused or with
// a hint for a missing one. --controller-namespace looks for the controller
// elsewhere. Any failed check exits 1, and no kubeconfig fails before the
// checks.
// Covers CLI-DOCTOR-01.
func TestCLI_Doctor(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ns := e.Namespace(t)
	barePipeline(t, e, ns, pipelineName)
	refused := barePipeline(t, e, ns, "refused", func(p *v1alpha1.Pipeline) {
		p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "token", Namespace: framework.ControllerNamespace}
	})
	waitPipelineValid(t, e, ns, pipelineName)
	framework.Eventually(t, time.Minute, "Pipeline refused Ready=False", func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: refused.Name}, &p); err != nil {
			return false, err.Error()
		}
		return meta.IsStatusConditionFalse(p.Status.Conditions, "Ready"), fmt.Sprint(p.Status.Conditions)
	})

	// The token check is named after the scm.provider the suite installed the
	// controller with (hack/e2e/components/kardinal.sh).
	provider := os.Getenv("KARDINAL_E2E_SCM_PROVIDER")
	tokenLabel, ok := map[string]string{"forgejo": "Forgejo token", "gitea": "Gitea token",
		"gitlab": "GitLab token", "github": "GitHub token"}[provider]
	require.True(t, ok, "KARDINAL_E2E_SCM_PROVIDER %q", provider)

	header := "\nkardinal-promoter pre-flight check\n" + strings.Repeat("=", 50) + "\n"
	row := func(icon, label, detail string) string { return fmt.Sprintf("%s  %-32s  %s\n", icon, label, detail) }
	checks := row("✅", "Controller reachable", fmt.Sprintf("kardinal-promoter %s in kardinal-system", controllerVersion(t, e))) +
		row("✅", "CRDs installed", "all 13 kardinal.io/v1alpha1 resources served") +
		row("✅", "kro running", fmt.Sprintf("kro %s in kro-system", kroTag(t, e))) +
		row("✅", "kro Graph CRD installed", "kro.run/v1alpha1 graphs registered") +
		row("✅", tokenLabel, "secret "+framework.GitSecretName+" (key token) present in kardinal-system")

	r := cli.Run("", "doctor")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, header+checks+"\n5 check(s) passed\n", r.Stdout)

	r = cli.Run(ns, "doctor", "--pipeline", pipelineName)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, header+checks+row("✅", `Pipeline "podinfo"`, "status: Unknown (spec valid, no Bundle yet)")+
		"\n6 check(s) passed\n", r.Stdout)

	r = cli.Run(ns, "doctor", "--pipeline", refused.Name)
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stdout, row("❌", `Pipeline "refused"`, fmt.Sprintf(`Ready=False (ValidationFailed): `+
		`git.secretRef.namespace %q is not allowed: the Secret must be in the Pipeline's namespace %q`,
		framework.ControllerNamespace, ns))+strings.Repeat(" ", 38)+
		"Fix the Pipeline spec; 'kardinal validate -f <your-pipeline.yaml>' reports the same problems.\n")
	assert.True(t, strings.HasSuffix(r.Stdout, "\n5 check(s) passed, 1 failed\n"), r.Stdout)

	r = cli.Run(ns, "doctor", "--pipeline", "missing")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stdout, row("❌", `Pipeline "missing"`, fmt.Sprintf(`Pipeline "missing" not found in namespace %q`, ns))+
		strings.Repeat(" ", 38)+"Apply: kubectl apply -f <your-pipeline.yaml>\n")
	assert.True(t, strings.HasSuffix(r.Stdout, "\n5 check(s) passed, 1 failed\n"), r.Stdout)
	assert.Equal(t, "1 pre-flight check(s) failed\n", r.Stderr)

	// No controller in the test namespace.
	r = cli.Run("", "doctor", "--controller-namespace", ns)
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stdout, row("❌", "Controller reachable", "kardinal-version ConfigMap not found in "+ns))
	assert.Contains(t, r.Stdout, "Installed elsewhere? Use --controller-namespace. Install: helm upgrade --install "+
		"kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter")
	assert.Contains(t, r.Stdout, row("⚠️", "SCM token", "no kardinal-promoter Deployment in "+ns))
	assert.True(t, strings.HasSuffix(r.Stdout, "\n3 check(s) passed, 1 warning(s), 1 failed\n"), r.Stdout)

	r = cli.Exec(noCluster, "doctor")
	assert.Equal(t, 1, r.Code)
	assert.True(t, strings.HasPrefix(r.Stdout, "\n❌ Could not build kubeconfig: "), r.Stdout)
	assert.Contains(t, r.Stdout, "\nHint: ensure kubectl is configured and pointing at the correct cluster.\n")
}

// fakeOpener is an xdg-open that records the URL it is asked to open.
const fakeOpener = "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$(dirname \"$0\")/opened\"\n"

// TestCLI_Dashboard runs kardinal dashboard with a stand-in browser opener.
// It prints the UI URL (http://localhost:8082/ui/ by default) and opens it,
// --address changes the URL, and --no-open only prints it. The address in
// the help's example serves the UI once kubectl port-forwards to the
// controller's port 8082. Without an opener the command fails.
// Covers CLI-DASHBOARD-01.
func TestCLI_Dashboard(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "xdg-open"), []byte(fakeOpener), 0o755))
	withOpener := framework.CLIOptions{Kubeconfig: "/dev/null", Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH")}}
	opened := func() string {
		b, err := os.ReadFile(filepath.Join(bin, "opened"))
		if err != nil {
			return ""
		}
		return string(b)
	}

	r := cli.Exec(withOpener, "dashboard")
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "kardinal UI: http://localhost:8082/ui/\nOpening in browser...\n", r.Stdout)
	framework.Eventually(t, 30*time.Second, "the opener runs", func(context.Context) (bool, string) {
		return opened() == "http://localhost:8082/ui/\n", opened()
	})

	// The help's --address example, on the port kubectl forwards to.
	help := cli.Exec(noCluster, "dashboard", "--help").Stdout
	m := regexp.MustCompile(`(?m)^\s+kardinal dashboard --address (\S+)$`).FindStringSubmatch(help)
	require.NotNil(t, m, "the help has an --address example:\n%s", help)
	example, err := url.Parse(m[1])
	require.NoError(t, err)
	forwarded, err := url.Parse(cli.PortForward(framework.ControllerNamespace, "svc/kardinal-promoter", 8082))
	require.NoError(t, err)
	example.Host = forwarded.Host
	address := example.String()

	r = cli.Exec(withOpener, "dashboard", "--address", address)
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "kardinal UI: "+address+"\nOpening in browser...\n", r.Stdout)
	framework.Eventually(t, 30*time.Second, "the opener gets --address", func(context.Context) (bool, string) {
		return strings.HasSuffix(opened(), "\n"+address+"\n"), opened()
	})
	resp, err := http.Get(address)
	require.NoError(t, err)
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the opened address serves the UI: %s", body[:n])
	assert.Contains(t, string(body[:n]), "<title>Kardinal Promoter</title>")

	before := opened()
	r = cli.Exec(withOpener, "dashboard", "--no-open", "--address", address)
	require.Equal(t, 0, r.Code)
	assert.Equal(t, "kardinal UI: "+address+"\n", r.Stdout)
	framework.Consistently(t, 3*time.Second, "--no-open opens nothing", func(context.Context) (bool, string) {
		return opened() == before, opened()
	})

	r = cli.Exec(framework.CLIOptions{Kubeconfig: "/dev/null", Env: []string{"PATH=" + t.TempDir()}}, "dashboard")
	assert.Equal(t, 1, r.Code)
	assert.Equal(t, "kardinal UI: http://localhost:8082/ui/\nOpening in browser...\n", r.Stdout)
	assert.Contains(t, r.Stderr, `exec: "xdg-open": executable file not found in $PATH`)
}

// TestCLI_Refresh checks that kardinal refresh sets the kardinal.io/refresh
// annotation to the current time and that the controller then reconciles
// the Pipeline, which it does not do while nothing changes. A missing
// Pipeline is an error.
// Covers CLI-REFRESH-01.
func TestCLI_Refresh(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	cli := e.CLI(t)
	ns := e.Namespace(t)
	created := time.Now().Add(-time.Second)
	barePipeline(t, e, ns, pipelineName)
	waitPipelineValid(t, e, ns, pipelineName)

	reconciled := func(since time.Time) []string {
		var lines []string
		for _, line := range strings.Split(e.ControllerLogs(t, since), "\n") {
			if strings.Contains(line, `"pipeline":"`+pipelineName+`"`) && strings.Contains(line, `"namespace":"`+ns+`"`) &&
				strings.Contains(line, `"message":"pipeline status`) {
				lines = append(lines, line)
			}
		}
		return lines
	}
	// The status write that made the Pipeline Valid triggers one more
	// reconcile ("already correct"), which a loaded controller runs seconds
	// later (#1555). The idle window starts once that has happened:
	// the last line is that reconcile's "already correct" and no reconcile
	// has been logged for 3s.
	count, since := -1, time.Now()
	framework.Eventually(t, time.Minute, "the reconciles after the Pipeline turned Valid to settle", func(context.Context) (bool, string) {
		lines := reconciled(created)
		if len(lines) != count {
			count, since = len(lines), time.Now()
		}
		last := ""
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		return strings.Contains(last, "already correct") && time.Since(since) >= 3*time.Second,
			fmt.Sprintf("%d reconciles logged, the last %s ago: %s", len(lines), time.Since(since).Round(time.Second), last)
	})
	idle := time.Now()
	framework.Consistently(t, 15*time.Second, "no reconcile while nothing changes", func(context.Context) (bool, string) {
		lines := reconciled(idle)
		return len(lines) == 0, strings.Join(lines, "\n")
	})

	start := time.Now().UTC().Truncate(time.Second)
	r := cli.Run(ns, "refresh", pipelineName)
	require.Equal(t, 0, r.Code, r.Stderr)
	assert.Equal(t, "Pipeline podinfo marked for refresh. Controller will re-reconcile shortly.\n", r.Stdout)

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: pipelineName}, &p))
	at, err := time.Parse(time.RFC3339, p.Annotations["kardinal.io/refresh"])
	require.NoError(t, err, "kardinal.io/refresh is an RFC 3339 time: %q", p.Annotations["kardinal.io/refresh"])
	assert.False(t, at.Before(start) || at.After(time.Now().Add(time.Second)), "the annotation is the time of the refresh: %s", at)

	framework.Eventually(t, time.Minute, "the controller reconciles the Pipeline", func(context.Context) (bool, string) {
		return len(reconciled(start)) > 0, "no Pipeline reconcile logged since the refresh"
	})

	r = cli.Run(ns, "refresh", "missing")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, `get pipeline missing: `)
	assert.Contains(t, r.Stderr, `not found`)
}
