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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// demoImageRef is the placeholder image used when --demo is passed.
// It is not a real image SHA — it signals to the user which image to substitute.
const demoImageRef = "ghcr.io/pnz1990/kardinal-test-app:sha-DEMO"

// InitConfig holds the parameters gathered by the kardinal init wizard.
type InitConfig struct {
	// AppName is the application name (used as Pipeline name).
	AppName string

	// Namespace is the Kubernetes namespace for the Pipeline.
	Namespace string

	// Environments is the ordered list of environment names.
	Environments []string

	// GitURL is the GitOps repository HTTPS URL.
	GitURL string

	// Branch is the base branch in the GitOps repository.
	Branch string

	// UpdateStrategy is either "kustomize" or "helm".
	UpdateStrategy string
}

// approvalModeFunc returns "pr-review" for the last environment, "auto" for others.
func approvalModeFunc(idx, total int) string {
	if idx == total-1 {
		return "pr-review"
	}
	return "auto"
}

func newInitCmd() *cobra.Command {
	var (
		stdoutFlag     bool
		outputFlag     string
		scaffoldGitOps bool
		gitopsDirFlag  string
		demoFlag       bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo",
		Long: `kardinal init guides you through creating a Pipeline CRD YAML.

It prompts for application name, namespace, environments, Git repo, and
update strategy, then writes a ready-to-apply pipeline.yaml.

Use --scaffold-gitops to also create the GitOps repository branch structure:
  environments/<env>/kustomization.yaml for each environment.

Use --demo to scaffold with the kardinal-test-app placeholder image.

Example:
  kardinal init
  kardinal init --scaffold-gitops --gitops-dir ./my-gitops
  kardinal init --demo --scaffold-gitops
  kubectl apply -f pipeline.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := runInitWizard(cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return fmt.Errorf("init wizard: %w", err)
			}

			var buf bytes.Buffer
			if err := initFn(&buf, cfg); err != nil {
				return err
			}

			if stdoutFlag {
				_, err = fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}

			outFile := outputFlag
			if outFile == "" {
				outFile = "pipeline.yaml"
			}
			if err := os.WriteFile(outFile, buf.Bytes(), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", outFile, err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Pipeline YAML written to %s\nApply with: kubectl apply -f %s\n",
				outFile, outFile,
			)

			if scaffoldGitOps || demoFlag {
				dir := gitopsDirFlag
				if dir == "" {
					dir = ".gitops"
				}
				imageRef := "REPLACE_ME:latest"
				if demoFlag {
					imageRef = demoImageRef
				}
				if err := scaffoldGitOpsFn(cmd.OutOrStdout(), cfg.Environments, dir, imageRef); err != nil {
					return fmt.Errorf("scaffold gitops: %w", err)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&stdoutFlag, "stdout", false, "Print to stdout instead of writing a file")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "Output file (default: pipeline.yaml)")
	cmd.Flags().BoolVar(&scaffoldGitOps, "scaffold-gitops", false, "Create GitOps repo structure (environments/<env>/kustomization.yaml)")
	cmd.Flags().StringVar(&gitopsDirFlag, "gitops-dir", ".gitops", "Directory for the GitOps scaffold (default: .gitops)")
	cmd.Flags().BoolVar(&demoFlag, "demo", false, "Scaffold with kardinal-test-app placeholder image (implies --scaffold-gitops)")

	return cmd
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
	// Parse imageRef into name and newTag for the Kustomize images block.
	// Cases:
	//   "repo:tag"           → name="repo",        newTag="tag"
	//   "repo@sha256:digest" → name="repo",        newTag="sha256:digest"
	//   "REPLACE_ME:latest"  → name="REPLACE_ME",  newTag="latest"
	name := imageRef
	tag := ""
	if atIdx := strings.Index(imageRef, "@"); atIdx >= 0 {
		// Digest ref: everything after "@" is the tag (including "sha256:...")
		name = imageRef[:atIdx]
		tag = imageRef[atIdx+1:]
	} else if n, t, ok := strings.Cut(imageRef, ":"); ok {
		name = n
		tag = t
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
	return sb.String()
}

// runInitWizard prompts the user interactively and returns an InitConfig.
func runInitWizard(in io.Reader, out io.Writer) (*InitConfig, error) {
	r := bufio.NewReader(in)
	prompt := func(label, defaultVal string) (string, error) {
		if defaultVal != "" {
			if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultVal); err != nil {
				return "", fmt.Errorf("prompt write: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(out, "%s: ", label); err != nil {
				return "", fmt.Errorf("prompt write: %w", err)
			}
		}
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read input: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal, nil
		}
		return line, nil
	}

	appName, err := prompt("Application name", "my-app")
	if err != nil {
		return nil, err
	}
	namespace, err := prompt("Namespace", "default")
	if err != nil {
		return nil, err
	}
	envsStr, err := prompt("Environments (comma-separated)", "test,uat,prod")
	if err != nil {
		return nil, err
	}
	gitURL, err := prompt("Git repository URL", "")
	if err != nil {
		return nil, err
	}
	branch, err := prompt("Base branch", "main")
	if err != nil {
		return nil, err
	}
	strategy, err := prompt("Update strategy (kustomize/helm)", "kustomize")
	if err != nil {
		return nil, err
	}

	envs := splitTrimmed(envsStr, ",")
	if len(envs) == 0 {
		envs = []string{"test", "uat", "prod"}
	}

	return &InitConfig{
		AppName:        appName,
		Namespace:      namespace,
		Environments:   envs,
		GitURL:         gitURL,
		Branch:         branch,
		UpdateStrategy: strategy,
	}, nil
}

// initFn is the testable core of kardinal init.
// It renders a Pipeline YAML from the given config and writes it to w.
func initFn(w io.Writer, cfg *InitConfig) error {
	tmpl, err := template.New("pipeline").Funcs(template.FuncMap{
		"approvalMode": approvalModeFunc,
		"len":          func(s []string) int { return len(s) },
	}).Parse(`# Generated by kardinal init
# Apply with: kubectl apply -f pipeline.yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: {{.AppName}}
  namespace: {{.Namespace}}
spec:
  git:
    url: {{.GitURL}}
    branch: {{.Branch}}
    secretRef:
      name: github-token
  environments:
{{- range $i, $env := .Environments}}
  - name: {{$env}}
    approval: {{approvalMode $i (len $.Environments)}}
    path: environments/{{$env}}
    update:
      strategy: {{$.UpdateStrategy}}
{{- end}}
`)
	if err != nil {
		return fmt.Errorf("parse pipeline template: %w", err)
	}

	if err := tmpl.Execute(w, cfg); err != nil {
		return fmt.Errorf("render pipeline YAML: %w", err)
	}
	return nil
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
