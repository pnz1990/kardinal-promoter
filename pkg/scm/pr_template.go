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

package scm

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// PRBodyUpstreamEnv holds per-environment upstream verification evidence for the
// PR body template. Elapsed is pre-computed by the caller (not derived at render time)
// to eliminate time.Since() calls inside template execution (SCM-4 logic leak).
type PRBodyUpstreamEnv struct {
	// Name is the environment name.
	Name string
	// Phase is the PromotionStep phase (e.g. "Verified").
	Phase string
	// HealthCheckedAt is the timestamp the environment was last health-checked.
	// Nil means not yet health-checked.
	HealthCheckedAt *metav1.Time
	// Elapsed is a pre-computed human-readable elapsed time since HealthCheckedAt.
	// Computed by the caller at the time the PR body is rendered, not by the template.
	// Empty string is rendered as "—" in the table.
	Elapsed string
}

// FormatElapsed formats a duration as a human-readable elapsed time string
// (e.g. "45m", "2h30m", "3d"). Used by callers to pre-compute PRBodyUpstreamEnv.Elapsed
// before calling RenderPRBody.
func FormatElapsed(since time.Time, now time.Time) string {
	d := now.Sub(since)
	if d < 0 {
		d = -d
	}
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh%dm", hours, minutes%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// PRBody holds the data used to render the PR body template.
type PRBody struct {
	// Pipeline is the Pipeline CRD spec.
	Pipeline v1alpha1.PipelineSpec

	// PipelineName is the pipeline resource name.
	PipelineName string

	// Environment is the target environment name.
	Environment string

	// Bundle holds the Bundle being promoted.
	Bundle v1alpha1.BundleSpec

	// BundleName is the Bundle resource name.
	BundleName string

	// RollbackOf is the name of the rollback's target (if this is a rollback
	// PR): the rollback restores its images, its config commit, or both, as
	// Bundle's type deploys. When non-empty, the PR body includes a rollback
	// notice section (#402).
	RollbackOf string

	// RestoredVersion is the version the rollback deploys, BundleVersion of
	// Bundle. Empty when the Bundle has no artifacts.
	RestoredVersion string

	// RollbackFrom names the Bundle the rollback replaces. Empty when it is
	// not recorded: the note then says "the bundle deployed in <env> now".
	RollbackFrom string

	// RollbackFromVersion is the version RollbackFrom deploys. Empty when it
	// is unknown.
	RollbackFromVersion string

	// RolledBackBy is who asked for the rollback. Empty when it is not
	// recorded.
	RolledBackBy string

	// GateResults holds PolicyGate evaluation results for this environment.
	GateResults []v1alpha1.GateResult

	// UpstreamEnvironments holds verification evidence for upstream environments.
	// Each entry includes a pre-computed Elapsed string to avoid time.Since in
	// template execution (SCM-4 logic leak fix).
	UpstreamEnvironments []PRBodyUpstreamEnv
}

// mdCellReplacer keeps free text inside one markdown table cell: a "|" (CEL
// uses "||") would start a new cell and a newline would end the row.
var mdCellReplacer = strings.NewReplacer("|", `\|`, "\r\n", " ", "\n", " ", "\r", " ")

// ciRunLink renders the CI Run cell of the provenance table: a link when raw
// passes graph.ValidateCIRunURL (an absolute http(s) URL), "—" otherwise. The
// bundle API checks new Bundles, but a Bundle created another way or before
// that check may hold anything: an empty ciRunURL must
// not render an empty "[CI run]()" link, another scheme (javascript:, a
// relative path) is not linked, and the characters of a valid URL that could
// end the link or the table cell (")", "|", "<", a backtick) are
// percent-encoded.
func ciRunLink(raw string) string {
	if raw == "" || graph.ValidateCIRunURL(raw) != nil {
		return "—"
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			strings.IndexByte("-._~:/?#[]@!$&'*+,;=%", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return "[CI run](" + b.String() + ")"
}

var prBodyTemplate = template.Must(template.New("pr-body").Funcs(template.FuncMap{
	"mdcell": mdCellReplacer.Replace,
	"cirun":  ciRunLink,
}).Parse(`<!-- kardinal-promoter auto-generated PR -->
{{- if .RollbackOf}}
## ROLLBACK: {{.BundleName}} -> {{.PipelineName}}/{{.Environment}}

> **This is a rollback PR.** {{if eq .Bundle.Type "mixed"}}It reverts environment {{.Environment}} to the state of bundle {{.RollbackOf}}.
{{- else if eq .Bundle.Type "config"}}It restores the config commit of bundle {{.RollbackOf}} in environment {{.Environment}}.
{{- else if eq .Bundle.Type "chart"}}It restores the chart version of bundle {{.RollbackOf}} in environment {{.Environment}}.
{{- else}}It restores the images of bundle {{.RollbackOf}} in environment {{.Environment}}.{{end}}
> Rolling back FROM: {{if .RollbackFrom}}{{.RollbackFrom}}{{with .RollbackFromVersion}} ({{mdcell .}}){{end}}{{else}}the bundle deployed in {{.Environment}} now{{end}}
> Rolling back TO: {{.RollbackOf}}{{with .RestoredVersion}} ({{mdcell .}}){{end}}
{{- with .RolledBackBy}}
> Rolled back by: {{mdcell .}}
{{- end}}

{{- else}}
## Promotion: {{.BundleName}} -> {{.PipelineName}}/{{.Environment}}
{{- end}}

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
{{- range .Bundle.Images}}
| {{.Repository}} | {{if .Tag}}{{.Tag}}{{else}}—{{end}} | {{if .Digest}}{{.Digest}}{{else}}—{{end}} | {{if $.Bundle.Provenance}}{{cirun $.Bundle.Provenance.CIRunURL}}{{else}}—{{end}} | {{if $.Bundle.Provenance}}{{or (mdcell $.Bundle.Provenance.CommitSHA) "—"}}{{else}}—{{end}} | {{if $.Bundle.Provenance}}{{or (mdcell $.Bundle.Provenance.Author) "—"}}{{else}}—{{end}} |
{{- else}}
| — | — | — | — | — | — |
{{- end}}
{{- with .Bundle.Chart}}

| Chart | Version | Repository | Digest |
|---|---|---|---|
| {{mdcell .Name}} | {{mdcell .Version}} | {{or (mdcell .RepoURL) "—"}} | {{or (mdcell .Digest) "—"}} |
{{- end}}

### Policy Gate Compliance

| Gate | Namespace | Result | Reason | Last Evaluated |
|---|---|---|---|---|
{{- range .GateResults}}
| {{.GateName}} | {{if .GateNamespace}}{{.GateNamespace}}{{else}}—{{end}} | {{.Result}} | {{mdcell .Reason}} | {{.EvaluatedAt.UTC.Format "2006-01-02T15:04Z"}} |
{{- else}}
| _(none)_ | — | — | — | — |
{{- end}}

### Upstream Verification

| Environment | Health Checked At | Elapsed |
|---|---|---|
{{- range .UpstreamEnvironments}}
| {{.Name}} | {{if .HealthCheckedAt}}{{.HealthCheckedAt.UTC.Format "2006-01-02T15:04Z"}}{{else}}—{{end}} | {{if .Elapsed}}{{.Elapsed}}{{else}}—{{end}} |
{{- else}}
| _(none)_ | — | — |
{{- end}}

---
*Generated by [kardinal-promoter](https://github.com/pnz1990/kardinal-promoter)*
`))

// RenderPRBody renders the PR body template with the given data.
func RenderPRBody(data PRBody) (string, error) {
	var buf bytes.Buffer
	if err := prBodyTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render PR body: %w", err)
	}
	return buf.String(), nil
}

// maxVersionImages is how many images BundleVersion lists before it says
// "and N more".
const maxVersionImages = 3

// BundleVersion names the version a Bundle deploys, for the rollback PR title
// and note: the tag of a one-image Bundle (its short digest, or the image name,
// when it has no tag), "<image>:<tag>" for each of several images, or
// "config <short commit>" for a config Bundle, "<chart> <version>" for a chart
// Bundle. A mixed Bundle deploys both, so
// it reads "<images> with config <short commit>". It returns "" when the
// Bundle has no artifacts.
func BundleVersion(spec v1alpha1.BundleSpec) string {
	if spec.Type == "chart" && spec.Chart != nil {
		return spec.Chart.Name + " " + spec.Chart.Version
	}
	config := ""
	if spec.ConfigRef != nil && spec.ConfigRef.CommitSHA != "" {
		config = "config " + truncate(spec.ConfigRef.CommitSHA, 7)
	}
	if spec.Type == "config" || len(spec.Images) == 0 {
		return config
	}
	images := imagesVersion(spec.Images)
	if spec.Type == "mixed" && config != "" {
		return images + " with " + config
	}
	return images
}

// imagesVersion names the images of a Bundle for BundleVersion.
func imagesVersion(images []v1alpha1.ImageRef) string {
	if len(images) == 1 {
		img := images[0]
		switch {
		case img.Tag != "":
			return img.Tag
		case img.Digest != "":
			return shortDigest(img.Digest)
		default:
			return path.Base(img.Repository)
		}
	}
	parts := make([]string, 0, maxVersionImages)
	for i, img := range images {
		if i == maxVersionImages {
			break
		}
		name := path.Base(img.Repository)
		switch {
		case img.Tag != "":
			name += ":" + img.Tag
		case img.Digest != "":
			name += "@" + shortDigest(img.Digest)
		}
		parts = append(parts, name)
	}
	v := strings.Join(parts, ", ")
	if more := len(images) - maxVersionImages; more > 0 {
		v += fmt.Sprintf(" and %d more", more)
	}
	return v
}

// shortDigest cuts "sha256:<64 hex>" to "sha256:<12 hex>".
func shortDigest(d string) string {
	if algo, hex, ok := strings.Cut(d, ":"); ok {
		return algo + ":" + truncate(hex, 12)
	}
	return truncate(d, 12)
}

// truncate returns the first n bytes of s.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
