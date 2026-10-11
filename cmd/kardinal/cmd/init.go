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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// demoImageRef is the placeholder image used when --demo is passed.
// It is not a real image SHA — it signals to the user which image to substitute.
const demoImageRef = "ghcr.io/pnz1990/kardinal-test-app:sha-DEMO"

// InitConfig is the Pipeline kardinal init writes: from its flags, and the
// wizard's answers for the ones not given.
type InitConfig struct {
	// AppName is the application name (used as Pipeline name).
	AppName string

	// Namespace is the Pipeline's namespace; empty leaves it out (the
	// namespace it is applied in), and so does default unless
	// NamespaceGiven.
	Namespace string

	// NamespaceGiven is true when -n/--namespace was given: the namespace
	// is written even when it is default.
	NamespaceGiven bool

	// Environments is the ordered list of environment names.
	Environments []string

	// GitURL is the GitOps repository HTTPS URL.
	GitURL string

	// Branch is the base branch in the GitOps repository; main is left out
	// (the default).
	Branch string

	// UpdateStrategy is either "kustomize" (the default, left out) or
	// "helm".
	UpdateStrategy string

	// ProdApproval is the last environment's approval: pr-review (the
	// default) or auto (left out).
	ProdApproval string

	// Secret is the git Secret's name (default github-token).
	Secret string

	// Health is, per environment, the health type detected in the cluster
	// (argocd, flux); an environment without one gets the default
	// (resource: the Deployment named after the Pipeline).
	Health map[string]string
}

// initOptions are the flags of kardinal init.
type initOptions struct {
	name, repo, envs, branch, strategy, prodApproval, secret string
	stdout, scaffold, demo, createSecret, apply, force       bool
	noDetect                                                 bool
	file, gitopsDir                                          string
}

// initClient is the client init detects health targets, creates the Secret
// and applies the Pipeline with; a test replaces it.
var initClient = buildClient

// ghToken is the token of "gh auth token"; a test replaces it.
var ghToken = func() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func newInitCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a Pipeline for your GitOps repository, from flags or a few questions",
		Long: `Write the smallest Pipeline that promotes your environments, defaults left
out: from flags (--repo makes it non-interactive), else the wizard asks for
what the flags do not say. With a cluster it finds the Argo CD Applications
or Flux Kustomizations to check health with; --create-secret makes the git
Secret, --apply applies the Pipeline.`,
		Example: `  kardinal init --name my-app --repo https://github.com/myorg/gitops
  kardinal init --name my-app --repo https://github.com/myorg/gitops --envs dev,prod --create-secret --apply
  kardinal init
  kardinal init --demo --scaffold-gitops && kubectl apply -f pipeline.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "Application and Pipeline name (default my-app)")
	f.StringVar(&o.repo, "repo", "", "GitOps repository https:// URL; with it, init asks nothing")
	f.StringVar(&o.envs, "envs", "", "Environments in promotion order, comma-separated (default test,uat,prod)")
	f.StringVar(&o.branch, "branch", "", "Base branch of the repository (default main)")
	f.StringVar(&o.strategy, "strategy", "", "Update strategy: kustomize (default) or helm")
	f.StringVar(&o.prodApproval, "prod-approval", "", "Approval of the last environment: pr-review (default) or auto")
	f.StringVar(&o.secret, "secret", "github-token", "Name of the Secret holding the git token")
	f.BoolVar(&o.createSecret, "create-secret", false, "Create the git Secret from $GITHUB_TOKEN, else gh auth token, labelled for the Pipeline to use")
	f.BoolVar(&o.apply, "apply", false, "Apply the Pipeline to the cluster")
	f.BoolVar(&o.force, "force", false, "Overwrite the file, and replace a Pipeline of the same name with --apply")
	f.BoolVar(&o.noDetect, "no-detect", false, "Do not look in the cluster for Argo CD Applications or Flux Kustomizations")
	f.BoolVar(&o.stdout, "stdout", false,
		"Print the Pipeline YAML to stdout instead of writing a file (a scaffold still writes its files)")
	f.StringVarP(&o.file, "file", "f", "pipeline.yaml", "File to write the Pipeline YAML to")
	f.BoolVar(&o.scaffold, "scaffold-gitops", false, "Create GitOps repo structure (environments/<env>/kustomization.yaml)")
	f.StringVar(&o.gitopsDir, "gitops-dir", ".gitops", "Directory for the GitOps scaffold")
	f.BoolVar(&o.demo, "demo", false, "Scaffold with kardinal-test-app placeholder image (implies --scaffold-gitops)")

	withReference(cmd, initReference)
	return cmd
}

// runInit is kardinal init: the config from the flags and the wizard, the
// detected health targets, then the YAML (validated before anything is
// written), the Secret, the apply and the scaffold.
func runInit(cmd *cobra.Command, o initOptions) error {
	cfg, err := initConfig(cmd, o)
	if err != nil {
		return err
	}
	progress := cmd.OutOrStdout()
	if o.stdout {
		// stdout carries only the YAML (it is usually redirected).
		progress = cmd.ErrOrStderr()
	}

	// A cluster, when one can be reached: for detection, and needed for
	// --create-secret and --apply.
	var c sigs_client.Client
	ns := cfg.Namespace
	if o.apply || o.createSecret || !o.noDetect {
		cl, cns, cerr := initClient()
		switch {
		case cerr == nil:
			c = cl
			if ns == "" {
				ns = cns
			}
		case o.apply || o.createSecret:
			return fmt.Errorf("init: --apply and --create-secret need a cluster: %w", cerr)
		}
	}
	if c != nil && !o.noDetect {
		cfg.Health = detectHealth(cmd.Context(), c, ns, cfg, cmd.ErrOrStderr())
	}

	var buf bytes.Buffer
	if err := initFn(&buf, cfg); err != nil {
		return err
	}
	// The Pipeline is checked as kardinal validate checks it before it is
	// written anywhere.
	var check bytes.Buffer
	allowed, err := scm.ParseRepositoryAllowlist(nil)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := validatePipeline(&check, "the Pipeline", buf.Bytes(), allowed); err != nil {
		detail := strings.TrimSpace(check.String())
		if detail == "" {
			detail = err.Error()
		} else {
			detail += "\n" + err.Error()
		}
		return fmt.Errorf("init: the Pipeline it would write is not valid:\n%s", detail)
	}

	if o.stdout {
		if _, err := fmt.Fprint(cmd.OutOrStdout(), buf.String()); err != nil {
			return err
		}
	} else {
		existing, rerr := os.ReadFile(o.file)
		switch {
		case rerr == nil && bytes.Equal(existing, buf.Bytes()):
			_, _ = fmt.Fprintf(progress, "Pipeline YAML in %s unchanged\n", o.file)
		case rerr == nil && !o.force:
			return fmt.Errorf("init: %s exists with another Pipeline; overwrite it with --force, or write another file with -f", o.file)
		}
		if rerr != nil || !bytes.Equal(existing, buf.Bytes()) {
			// --file may name a directory that does not exist yet.
			if err := os.MkdirAll(filepath.Dir(o.file), 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", o.file, err)
			}
			if err := os.WriteFile(o.file, buf.Bytes(), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", o.file, err)
			}
			_, _ = fmt.Fprintf(progress, "Pipeline YAML written to %s\n", o.file)
		}
		if !o.apply {
			_, _ = fmt.Fprintf(progress, "Apply with: kubectl apply -f %s\n", o.file)
		}
	}

	if o.createSecret {
		if err := createGitSecret(cmd.Context(), c, ns, cfg.Secret, progress); err != nil {
			return err
		}
	}
	if o.apply {
		if err := applyPipeline(cmd.Context(), c, ns, buf.Bytes(), o.force, progress); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Next: ship a version: kardinal ship %s --image <ref>\n", cfg.AppName)
	}

	if (o.scaffold || o.demo) && cfg.UpdateStrategy != "kustomize" && cfg.UpdateStrategy != "" {
		_, _ = fmt.Fprintf(progress,
			"GitOps scaffold skipped: it writes Kustomize overlays, and the update strategy is %s\n",
			cfg.UpdateStrategy)
	} else if o.scaffold || o.demo {
		imageRef := "REPLACE_ME:latest"
		if o.demo {
			imageRef = demoImageRef
		}
		if err := scaffoldGitOpsFn(progress, cfg.Environments, o.gitopsDir, imageRef); err != nil {
			return fmt.Errorf("scaffold gitops: %w", err)
		}
	}
	return nil
}

// initConfig is the Pipeline the flags describe; without --repo, the wizard
// asks for each value whose flag is not set, reading cmd's input (one
// answer per line when it is not a terminal).
func initConfig(cmd *cobra.Command, o initOptions) (*InitConfig, error) {
	cfg := &InitConfig{AppName: o.name, Environments: splitTrimmed(o.envs, ","), GitURL: o.repo, Branch: o.branch,
		UpdateStrategy: o.strategy, ProdApproval: o.prodApproval, Secret: o.secret}
	if globalNamespace != "" {
		cfg.Namespace = globalNamespace
		cfg.NamespaceGiven = true
	}
	if o.repo == "" {
		var eof *initEOFError
		err := runInitWizard(cmd.InOrStdin(), cmd.ErrOrStderr(), cfg, cmd.Flags().Changed)
		if errors.As(err, &eof) {
			return nil, fmt.Errorf("kardinal init asks its questions on a terminal, and its input ended at %q (%v): "+
				"run it in a terminal, pipe one answer per line, or give the answers as flags: "+
				"kardinal init --name my-app --repo https://github.com/myorg/gitops", eof.label, eof.err)
		}
		if err != nil {
			return nil, fmt.Errorf("init wizard: %w", err)
		}
	}
	if cfg.AppName == "" {
		cfg.AppName = "my-app"
	}
	if len(cfg.Environments) == 0 {
		cfg.Environments = []string{"test", "uat", "prod"}
	}
	if cfg.Secret == "" {
		cfg.Secret = "github-token"
	}
	if err := checkInitNames(cfg); err != nil {
		return nil, err
	}
	switch {
	case !validRepoURL(cfg.GitURL):
		return nil, fmt.Errorf("init: --repo needs a git repository URL, https://host/owner/repo or ssh (ssh://git@host/owner/repo, git@host:owner/repo.git), "+
			"with no spaces, query or fragment; got %q", cfg.GitURL)
	case cfg.Branch != "" && !validBranch(cfg.Branch):
		return nil, fmt.Errorf("init: --branch %q is not a git branch name", cfg.Branch)
	case o.createSecret && scm.IsSSHRemote(cfg.GitURL):
		ns := cfg.Namespace
		if ns == "" {
			ns = "<namespace>"
		}
		return nil, fmt.Errorf("init: --create-secret creates a token Secret, and an ssh --repo needs an ssh key instead; "+
			"create the Secret yourself and run init without --create-secret: "+
			"kubectl create secret generic %[1]s -n %[2]s --from-file=sshPrivateKey=$HOME/.ssh/id_ed25519 "+
			"--from-literal=knownHosts=\"$(ssh-keyscan %[3]s)\" && kubectl label secret %[1]s -n %[2]s %[4]s=true",
			cfg.Secret, ns, sshHost(cfg.GitURL), v1alpha1.LabelSecretReferenceable)
	case cfg.UpdateStrategy != "" && cfg.UpdateStrategy != "kustomize" && cfg.UpdateStrategy != "helm":
		return nil, fmt.Errorf("init: --strategy must be kustomize or helm, got %q", cfg.UpdateStrategy)
	case cfg.ProdApproval != "" && cfg.ProdApproval != "pr-review" && cfg.ProdApproval != "auto":
		return nil, fmt.Errorf("init: --prod-approval must be pr-review or auto, got %q", cfg.ProdApproval)
	}
	return cfg, nil
}

// checkInitNames checks the names init writes into the Pipeline: the
// Pipeline, its namespace and its environments are DNS-1123 labels, and the
// Secret a DNS-1123 subdomain, as Kubernetes needs them.
func checkInitNames(cfg *InitConfig) error {
	label := func(what, v string) error {
		if errs := validation.IsDNS1123Label(v); len(errs) > 0 {
			return fmt.Errorf("init: %s %q is not a valid name: %s", what, v, strings.Join(errs, "; "))
		}
		return nil
	}
	if err := label("the Pipeline name (--name)", cfg.AppName); err != nil {
		return err
	}
	if cfg.Namespace != "" {
		if err := label("the namespace (-n)", cfg.Namespace); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, env := range cfg.Environments {
		if err := label("the environment (--envs)", env); err != nil {
			return err
		}
		if seen[env] {
			return fmt.Errorf("init: the environment %q is given twice (--envs)", env)
		}
		seen[env] = true
	}
	if errs := validation.IsDNS1123Subdomain(cfg.Secret); len(errs) > 0 {
		return fmt.Errorf("init: the Secret name (--secret) %q is not a valid name: %s", cfg.Secret, strings.Join(errs, "; "))
	}
	return nil
}

// sshHost is the host of ssh remote v (ssh://user@host/path or
// user@host:path), for ssh-keyscan.
func sshHost(v string) string {
	if u, err := url.Parse(v); err == nil && u.Scheme == "ssh" {
		return u.Hostname()
	}
	if _, rest, ok := strings.Cut(v, "@"); ok {
		host, _, _ := strings.Cut(rest, ":")
		return host
	}
	return "<host>"
}

// scpLikeRepo is git's scp-like ssh remote, user@host:path.
var scpLikeRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)

// validRepoURL reports whether v is a git remote init may write: an https or
// ssh URL with a host and a path, no query, fragment or password, or an
// scp-like ssh remote; never with whitespace or control characters.
func validRepoURL(v string) bool {
	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	if scpLikeRepo.MatchString(v) {
		return true
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || u.Host == "" || u.Opaque != "" {
		return false
	}
	if _, hasPassword := u.User.Password(); hasPassword || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(v, "#?") {
		return false
	}
	return strings.Trim(u.Path, "/") != ""
}

// validBranch reports whether v is a branch name git accepts (the rules of
// git check-ref-format that matter for a name typed on a command line).
func validBranch(v string) bool {
	if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") ||
		strings.HasSuffix(v, ".") || strings.HasSuffix(v, ".lock") || strings.Contains(v, "..") ||
		strings.Contains(v, "//") || strings.Contains(v, "@{") || v == "@" {
		return false
	}
	return strings.IndexFunc(v, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) || r > unicode.MaxASCII
	}) < 0
}

// The health types init can find in the cluster: an Argo CD Application or
// a Flux Kustomization with the name and namespace the controller looks at
// by default (docs/pipeline-reference.md, Health Check Defaults).
var healthTargets = []struct {
	kind      string
	gvk       schema.GroupVersionKind
	namespace string
	what      string
}{
	{"argocd", schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}, "argocd", "Argo CD Application"},
	{"flux", schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, "flux-system", "Flux Kustomization"},
}

// detectHealth is, per environment, the health type whose object exists in
// the cluster under its default name (<pipeline>-<env>); an environment
// with neither keeps the default (a Deployment). Each find is reported on
// out. A kind the cluster does not serve, or cannot be read, finds nothing.
func detectHealth(ctx context.Context, c sigs_client.Reader, ns string, cfg *InitConfig, out io.Writer) map[string]string {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	found := map[string]string{}
	for _, env := range cfg.Environments {
		name := cfg.AppName + "-" + env
		for _, h := range healthTargets {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(h.gvk)
			if err := c.Get(ctx, types.NamespacedName{Namespace: h.namespace, Name: name}, u); err != nil {
				continue
			}
			found[env] = h.kind
			_, _ = fmt.Fprintf(out, "Found %s %s/%s: %s checks health with it\n", h.what, h.namespace, name, env)
			break
		}
	}
	return found
}

// createGitSecret creates the git Secret name in ns with the token from
// $GITHUB_TOKEN, else gh auth token, labelled kardinal.io/referenceable so
// the Pipeline may use it. An existing Secret is kept, and labelled when it
// is not. The token is never printed.
func createGitSecret(ctx context.Context, c sigs_client.Client, ns, name string, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var existing corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing)
	switch {
	case err == nil:
		if existing.Labels[v1alpha1.LabelSecretReferenceable] == "true" {
			_, _ = fmt.Fprintf(out, "Secret %s/%s exists; kept\n", ns, name)
			return nil
		}
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels[v1alpha1.LabelSecretReferenceable] = "true"
		if err := c.Update(ctx, &existing); err != nil {
			return fmt.Errorf("init: label Secret %s/%s: %w", ns, name, HumanError(err))
		}
		_, _ = fmt.Fprintf(out, "Secret %s/%s exists; labelled %s=true\n", ns, name, v1alpha1.LabelSecretReferenceable)
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("init: get Secret %s/%s: %w", ns, name, HumanError(err))
	}
	token, from, err := gitToken()
	if err != nil {
		return fmt.Errorf("init: --create-secret needs a git token: set GITHUB_TOKEN, or log in with gh auth login")
	}
	s := &corev1.Secret{}
	s.Name, s.Namespace = name, ns
	s.Labels = map[string]string{v1alpha1.LabelSecretReferenceable: "true"}
	s.StringData = map[string]string{"token": token}
	if err := c.Create(ctx, s); err != nil {
		return fmt.Errorf("init: create Secret %s/%s: %w", ns, name, HumanError(err))
	}
	_, _ = fmt.Fprintf(out, "Secret %s/%s created from %s\n", ns, name, from)
	return nil
}

// errNoGitToken is gitToken's error when there is no token.
var errNoGitToken = errors.New("no git token: GITHUB_TOKEN is not set and gh auth token gave none")

// gitToken is the git token for a Secret: $GITHUB_TOKEN, else gh auth
// token. from says which; the token itself is never printed.
func gitToken() (token, from string, err error) {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t, "$GITHUB_TOKEN", nil
	}
	t, err := ghToken()
	if err != nil || t == "" {
		return "", "", errNoGitToken
	}
	return t, "gh auth token", nil
}

// applyPipeline creates the Pipeline of data in ns; one of the same name is
// replaced only with force.
func applyPipeline(ctx context.Context, c sigs_client.Client, ns string, data []byte, force bool, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var p v1alpha1.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if p.Namespace == "" {
		p.Namespace = ns
	}
	var existing v1alpha1.Pipeline
	err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := c.Create(ctx, &p); err != nil {
			return fmt.Errorf("init: apply Pipeline %s/%s: %w", p.Namespace, p.Name, HumanError(err))
		}
		_, _ = fmt.Fprintf(out, "Pipeline %s/%s created\n", p.Namespace, p.Name)
		return nil
	case err != nil:
		return fmt.Errorf("init: get Pipeline %s/%s: %w", p.Namespace, p.Name, HumanError(err))
	}
	// What the API server would store (its defaults applied) tells an
	// identical Pipeline from a change.
	want := existing.DeepCopy()
	want.Spec = p.Spec
	if err := c.Update(ctx, want, sigs_client.DryRunAll); err != nil {
		return fmt.Errorf("init: check Pipeline %s/%s: %w", p.Namespace, p.Name, HumanError(err))
	}
	if equality.Semantic.DeepEqual(want.Spec, existing.Spec) {
		_, _ = fmt.Fprintf(out, "Pipeline %s/%s unchanged\n", p.Namespace, p.Name)
		return nil
	}
	if !force {
		return fmt.Errorf("init: Pipeline %s/%s exists with another spec; replace it with --force", p.Namespace, p.Name)
	}
	existing.Spec = p.Spec
	if err := c.Update(ctx, &existing); err != nil {
		return fmt.Errorf("init: replace Pipeline %s/%s: %w", p.Namespace, p.Name, HumanError(err))
	}
	_, _ = fmt.Fprintf(out, "Pipeline %s/%s replaced\n", p.Namespace, p.Name)
	return nil
}

// scaffoldGitOpsFn creates the GitOps directory structure for the given environments.
// It is idempotent: existing files are not overwritten.
// imageRef is placed in each environment's kustomization.yaml images block.
func scaffoldGitOpsFn(out io.Writer, environments []string, gitopsDir string, imageRef string) error {
	for _, env := range environments {
		envDir := filepath.Join(gitopsDir, "environments", env)
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", envDir, err)
		}
		kustomizationPath := filepath.Join(envDir, "kustomization.yaml")

		if _, err := os.Stat(kustomizationPath); err == nil {
			// File already exists — skip to preserve user edits.
			_, _ = fmt.Fprintf(out, "  skipped (already exists): %s\n", kustomizationPath)
			continue
		}

		content := buildKustomization(imageRef)
		if err := os.WriteFile(kustomizationPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", kustomizationPath, err)
		}
		_, _ = fmt.Fprintf(out, "  created: %s\n", kustomizationPath)
	}
	_, _ = fmt.Fprintf(out, "GitOps scaffold written to %s\n", gitopsDir)
	return nil
}

// buildKustomization returns a minimal Kustomize overlay for an environment.
// The images block uses imageRef as a placeholder for the application image.
func buildKustomization(imageRef string) string {
	// The tag is after the last ":" that follows the last "/", so a registry
	// port (localhost:5000/app:v1) stays in the name. A digest is written as
	// Kustomize's digest field.
	name, digest, _ := strings.Cut(imageRef, "@")
	tag := ""
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name, tag = name[:colon], name[colon+1:]
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	sb.WriteString("kind: Kustomization\n")
	sb.WriteString("\n")
	sb.WriteString("# Add resources here, e.g.:\n")
	sb.WriteString("# resources:\n")
	sb.WriteString("#   - ../../base\n")
	sb.WriteString("\n")
	sb.WriteString("images:\n")
	sb.WriteString("  - name: " + name + "\n")
	if tag != "" {
		sb.WriteString("    newTag: " + tag + "\n")
	}
	if digest != "" {
		sb.WriteString("    digest: " + digest + "\n")
	}
	return sb.String()
}

// runInitWizard asks for the values of cfg whose flag was not set
// (changed reports a set flag) and fills them in. An invalid answer is asked
// again; at end of input it is an error.
func runInitWizard(in io.Reader, out io.Writer, cfg *InitConfig, changed func(string) bool) error {
	r := bufio.NewReader(in)
	prompt := func(label, defaultVal string, valid func(string) error) (string, error) {
		for {
			text := label + ": "
			if defaultVal != "" {
				text = fmt.Sprintf("%s [%s]: ", label, defaultVal)
			}
			if _, err := fmt.Fprint(out, text); err != nil {
				return "", fmt.Errorf("prompt write: %w", err)
			}
			line, err := r.ReadString('\n')
			if err != nil && err != io.EOF {
				return "", fmt.Errorf("read input: %w", err)
			}
			answer := strings.TrimSpace(line)
			if answer == "" {
				answer = defaultVal
			}
			if valid == nil {
				return answer, nil
			}
			verr := valid(answer)
			if verr == nil {
				return answer, nil
			}
			if err == io.EOF {
				return "", &initEOFError{label: label, err: verr}
			}
			if _, err := fmt.Fprintf(out, "  %v\n", verr); err != nil {
				return "", fmt.Errorf("prompt write: %w", err)
			}
		}
	}
	var err error
	if !changed("name") {
		if cfg.AppName, err = prompt("Application name", "my-app", nil); err != nil {
			return err
		}
	}
	if globalNamespace == "" {
		if cfg.Namespace, err = prompt("Namespace", "default", nil); err != nil {
			return err
		}
	}
	if !changed("envs") {
		envs, err := prompt("Environments (comma-separated)", "test,uat,prod", nil)
		if err != nil {
			return err
		}
		cfg.Environments = splitTrimmed(envs, ",")
	}
	if cfg.GitURL, err = prompt("Git repository URL", "", func(v string) error {
		if !validRepoURL(v) {
			return fmt.Errorf("a git repository URL is required (https://host/owner/repo or git@host:owner/repo.git)")
		}
		return nil
	}); err != nil {
		return err
	}
	if !changed("branch") {
		if cfg.Branch, err = prompt("Base branch", "main", nil); err != nil {
			return err
		}
	}
	if !changed("strategy") {
		if cfg.UpdateStrategy, err = prompt("Update strategy (kustomize/helm)", "kustomize", func(v string) error {
			if v != "kustomize" && v != "helm" {
				return fmt.Errorf("update strategy must be kustomize or helm, got %q", v)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// initPipeline is the Pipeline cfg describes, with only what differs from
// the defaults set (the smallest Pipeline that says it).
func initPipeline(cfg *InitConfig) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{}
	p.APIVersion, p.Kind = v1alpha1.GroupVersion.String(), "Pipeline"
	p.Name = cfg.AppName
	if cfg.Namespace != "default" || cfg.NamespaceGiven {
		p.Namespace = cfg.Namespace
	}
	secret := cfg.Secret
	if secret == "" {
		secret = "github-token"
	}
	p.Spec.Git.URL = cfg.GitURL
	if cfg.Branch != "main" {
		p.Spec.Git.Branch = cfg.Branch
	}
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: secret}
	for i, name := range cfg.Environments {
		env := v1alpha1.EnvironmentSpec{Name: name}
		if i == len(cfg.Environments)-1 && cfg.ProdApproval != "auto" {
			env.Approval = "pr-review"
		}
		if cfg.UpdateStrategy == "helm" {
			env.Update.Strategy = "helm"
		}
		env.Health.Type = cfg.Health[name]
		p.Spec.Environments = append(p.Spec.Environments, env)
	}
	return p
}

// initFn writes the Pipeline cfg describes (initPipeline) as YAML in the
// layout of examples/minimal. Every value is a YAML scalar encoded by the
// YAML library, and the YAML is decoded back and compared with the Pipeline
// before it is written: no input can add or change a field.
func initFn(w io.Writer, cfg *InitConfig) error {
	p := initPipeline(cfg)
	var b strings.Builder
	b.WriteString("apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\n")
	if p.Namespace != "" {
		fmt.Fprintf(&b, "metadata: { name: %s, namespace: %s }\n", yamlFlowScalar(p.Name), yamlFlowScalar(p.Namespace))
	} else {
		fmt.Fprintf(&b, "metadata: { name: %s }\n", yamlFlowScalar(p.Name))
	}
	b.WriteString("spec:\n  git:\n")
	fmt.Fprintf(&b, "    url: %s\n", yamlFlowScalar(p.Spec.Git.URL))
	if p.Spec.Git.Branch != "" {
		fmt.Fprintf(&b, "    branch: %s\n", yamlFlowScalar(p.Spec.Git.Branch))
	}
	fmt.Fprintf(&b, "    secretRef: { name: %s }\n", yamlFlowScalar(p.Spec.Git.SecretRef.Name))
	b.WriteString("  environments:\n")
	for _, env := range p.Spec.Environments {
		fmt.Fprintf(&b, "    - name: %s\n", yamlFlowScalar(env.Name))
		if env.Approval != "" {
			fmt.Fprintf(&b, "      approval: %s\n", yamlFlowScalar(env.Approval))
		}
		if env.Update.Strategy != "" {
			fmt.Fprintf(&b, "      update: { strategy: %s }\n", yamlFlowScalar(env.Update.Strategy))
		}
		if env.Health.Type != "" {
			fmt.Fprintf(&b, "      health: { type: %s }\n", yamlFlowScalar(env.Health.Type))
		}
	}
	var back v1alpha1.Pipeline
	if err := yaml.UnmarshalStrict([]byte(b.String()), &back); err != nil {
		return fmt.Errorf("render pipeline YAML: it does not parse: %w", err)
	}
	if !equality.Semantic.DeepEqual(back.ObjectMeta, p.ObjectMeta) || !equality.Semantic.DeepEqual(back.Spec, p.Spec) {
		return fmt.Errorf("render pipeline YAML: it does not say what was asked; a value is not valid YAML here")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render pipeline YAML: %w", err)
	}
	return nil
}

// yamlFlowScalar is v as a YAML scalar that is safe anywhere, also inside a
// flow mapping: plain when the YAML library writes it plain (it quotes what
// would read as another type or starts with an indicator) and it has no flow
// indicator, space or quote, else double-quoted.
func yamlFlowScalar(v string) string {
	plain, err := yaml.Marshal(v)
	out := strings.TrimSuffix(string(plain), "\n")
	if err == nil && out == v && !strings.ContainsAny(v, ",[]{}#'\"` \t") {
		return v
	}
	return strconv.Quote(v)
}

// splitTrimmed splits s by sep and trims spaces from each element.
func splitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// initReference is the detail of the command for its reference page.
const initReference = `With --repo, init asks nothing: --name (default my-app), --envs (default
test,uat,prod, in promotion order), --branch (default main), --strategy
(kustomize, the default, or helm), --prod-approval (pr-review, the default,
or auto: the last environment's approval) and --secret (default
github-token) say the rest, and -n the namespace. Without --repo it asks for
each of them that no flag gives, on a terminal or one answer per line from a
pipe.

It writes the smallest Pipeline that says it, as examples/minimal does: a
default (branch main, kustomize, approval auto, the default health check) is
left out (also namespace default, unless -n gives it). The name, namespace and environments must
be DNS-1123 labels, --repo an https or ssh git URL (https://host/owner/repo,
ssh://git@host/owner/repo or git@host:owner/repo.git) with no spaces, query
or fragment, and --branch a git branch name; every value is written as a
YAML scalar and the YAML is decoded back and compared, so no value can add
a field. When a cluster can be reached it looks, for each environment, for
an Argo CD Application <name>-<env> in argocd or a Flux Kustomization
<name>-<env> in flux-system, and sets health type argocd or flux where it
finds one (--no-detect skips this). The Pipeline is checked as kardinal
validate checks it before anything is written. Running init again with the
same answers changes nothing ("unchanged") and succeeds; a file with
another Pipeline is not overwritten without --force.

--create-secret creates the git Secret (key token) from $GITHUB_TOKEN, else
from gh auth token, labelled kardinal.io/referenceable=true so the Pipeline
may use it; an existing Secret is kept (and labelled). --apply creates the
Pipeline; one of the same name and spec is unchanged, one with another spec
is replaced only with --force.

Use --scaffold-gitops to also create the GitOps repository structure:
  environments/<env>/kustomization.yaml for each environment.
The scaffold is Kustomize-only; it is skipped for the helm strategy.

Use --demo to scaffold with the kardinal-test-app placeholder image.`

// initEOFError is the wizard's input ending before a required answer.
type initEOFError struct {
	label string
	err   error
}

func (e *initEOFError) Error() string { return fmt.Sprintf("%s: %v", e.label, e.err) }
func (e *initEOFError) Unwrap() error { return e.err }
