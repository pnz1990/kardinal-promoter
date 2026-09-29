// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// repoRoot is the repository root relative to this package directory.
const repoRoot = "../../.."

// docExpr is one CEL expression found in the user docs or the examples.
type docExpr struct {
	where string // file:line
	expr  string
	// boolResult is true for gate expressions, which must evaluate to a bool.
	// Function-table examples (json.marshal(...), lists.setAtIndex(...)) may
	// return any type; they only have to evaluate without an error.
	boolResult bool
	// mustBeTrue is set for the type checks generated from attribute tables.
	mustBeTrue bool
}

// attrTableFiles are the pages whose tables document CEL attributes. Other
// pages have tables with the same column shape (for example notification
// payload fields), so attribute rows are only read from these.
var attrTableFiles = map[string]bool{
	"docs/policy-gates.md":          true,
	"docs/reference/cel-context.md": true,
	"docs/concepts.md":              true,
}

// TestDocumentedCELContext holds the docs to the real PolicyGate CEL context
// (C04-gates-08, C14b-meta-08): every attribute listed in a CEL context table
// must exist with the documented type, and every example expression in the user
// docs and examples must compile and evaluate without an error against the
// context that buildContext produces. A missing attribute is an evaluation
// error, and in a real gate that blocks the promotion forever.
//
// SkipPermission gates are excluded: they are evaluated by the translator, not
// by this reconciler.
func TestDocumentedCELContext(t *testing.T) {
	r, gate := docsCELFixture(t)

	exprs := collectDocExprs(t)
	require.NotEmpty(t, exprs)

	// Guard the collector itself: if these stop being found, the test would
	// pass vacuously.
	var joined strings.Builder
	for _, e := range exprs {
		joined.WriteString(e.expr)
		joined.WriteString("\n")
	}
	for _, must := range []string{
		`type(bundle.labels) == map`,
		`type(bundle.pr["staging"].approvalCount) == int`,
		`type(metrics["error-rate"].value) == string`,
		`type(upstream.uat.recentSuccessCount) == int`,
		`upstream.staging.recentSuccessCount >= 3`,
		`!changewindow.isBlocked("q4-holiday-freeze")`,
		`random.seededInt(0, 100, bundle.version) < 10`,
		`metrics["success-rate"].result == "Pass"`,
	} {
		assert.Contains(t, joined.String(), must, "collector did not find a known docs expression")
	}

	for _, de := range exprs {
		t.Run(de.where, func(t *testing.T) {
			gate.Spec.Expression = de.expr
			celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
			require.NoError(t, err, "build context for %q", de.expr)
			if de.boolResult {
				ok, reason, err := r.eval.evaluate(de.expr, celCtx)
				require.NoError(t, err, "%s: %s", de.where, reason)
				if de.mustBeTrue {
					assert.True(t, ok, "%s: documented type is wrong: %s", de.where, reason)
				}
				return
			}
			ast, iss := r.eval.env.Compile(de.expr)
			require.NoError(t, iss.Err(), "%s: compile %q", de.where, de.expr)
			prg, err := r.eval.env.Program(ast)
			require.NoError(t, err)
			_, _, err = prg.Eval(celCtx)
			require.NoError(t, err, "%s: evaluate %q", de.where, de.expr)
		})
	}
}

// docsCELFixture returns a Reconciler over a fake cluster that has every object
// the docs refer to: the Bundle, its Pipeline, MetricChecks, a PRStatus and
// ChangeWindows, and the prod gate instance.
func docsCELFixture(t *testing.T) (*Reconciler, *kardinalv1alpha1.PolicyGate) {
	t.Helper()
	const ns = "default"
	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC) // a Tuesday
	checked := metav1.NewTime(now.Add(-2 * time.Hour))

	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "staging"}, {Name: "staging-us"}, {Name: "staging-eu"},
			{Name: "uat"}, {Name: "prod"},
		}},
	}
	var envStatus []kardinalv1alpha1.EnvironmentStatus
	for _, e := range []string{"test", "staging", "staging-us", "staging-eu", "uat"} {
		envStatus = append(envStatus, kardinalv1alpha1.EnvironmentStatus{
			Name: e, Phase: "Verified", SoakMinutes: 90, HealthCheckedAt: &checked,
		})
	}
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-v1", Namespace: ns,
			Labels: map[string]string{
				"hotfix":             "true",
				"release-type":       "hotfix",
				"kardinal.io/hotfix": "true",
			},
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "app",
			Images:   []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.29.0"}},
			Provenance: &kardinalv1alpha1.BundleProvenance{
				Author: "engineer@co.com", CommitSHA: "abc123def", CIRunURL: "https://github.com/org/app/actions/runs/1",
			},
			Intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod"},
		},
		Status: kardinalv1alpha1.BundleStatus{Environments: envStatus},
	}
	objs := []runtime.Object{pipeline, bundle}
	for name, value := range map[string]string{
		"error-rate": "0.001", "p99-latency": "120", "success-rate": "0.999", "staging-error-rate": "0.001",
	} {
		objs = append(objs, &kardinalv1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Status:     kardinalv1alpha1.MetricCheckStatus{LastValue: value, Result: "Pass"},
		})
	}
	objs = append(objs, &kardinalv1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-v1-staging", Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "staging"},
		},
		Status: kardinalv1alpha1.PRStatusStatus{Open: true, Approved: true, ApprovalCount: 2},
	})
	for _, name := range []string{"q4-holiday-freeze", "holiday-freeze"} {
		objs = append(objs, &kardinalv1alpha1.ChangeWindow{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: kardinalv1alpha1.ChangeWindowSpec{
				Type:  "blackout",
				Start: metav1.NewTime(time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)),
				End:   metav1.NewTime(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)),
			},
		})
	}
	objs = append(objs, &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours"},
		Spec: kardinalv1alpha1.ChangeWindowSpec{
			Type:     "recurring",
			Schedule: &kardinalv1alpha1.ChangeWindowSchedule{AllowedHours: "09:00-17:00"},
		},
	})

	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()

	r, err := NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }

	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prod-gate", Namespace: ns,
			Labels: map[string]string{
				labelPipeline:        "app",
				labelEnvironment:     "prod",
				"kardinal.io/bundle": "app-v1",
			},
		},
	}
	return r, gate
}

// collectDocExprs gathers the expressions to check from the user docs
// (docs/*.md, docs/guides, docs/reference) and the example manifests.
func collectDocExprs(t *testing.T) []docExpr {
	t.Helper()
	var files []string
	for _, pattern := range []string{"docs/*.md", "docs/guides/*.md", "docs/reference/*.md"} {
		m, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		require.NoError(t, err)
		files = append(files, m...)
	}
	require.NotEmpty(t, files)

	var out []docExpr
	for _, f := range files {
		out = append(out, markdownExprs(t, f)...)
	}
	for _, dir := range []string{"examples", "demo"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
				out = append(out, manifestExprs(t, path)...)
			}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

var (
	fenceRe = regexp.MustCompile("^\\s*```\\s*([A-Za-z]*)")
	// A CEL context table row: | `attribute` | type | ...
	attrRowRe = regexp.MustCompile("^\\|\\s*`((?:bundle|schedule|environment|upstream|metrics|changewindow)[^`]*)`\\s*\\|\\s*([a-z ]+?)\\s*\\|")
	// Any row of a function table; the example is the last backticked cell.
	cellRe = regexp.MustCompile("`([^`]+)`")
)

// markdownExprs returns the expressions in one markdown file: `expression:`
// values in yaml blocks, every expression in cel blocks, the attributes of CEL
// context tables (as type checks) and the examples in the function tables of
// the CEL context reference.
func markdownExprs(t *testing.T, path string) []docExpr {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	rel := strings.TrimPrefix(path, repoRoot+"/")
	lines := strings.Split(string(data), "\n")

	var out []docExpr
	section := "" // current "## " heading
	inFence, lang, start := false, "", 0
	var block []string
	for i, line := range lines {
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			if !inFence {
				inFence, lang, start, block = true, strings.ToLower(m[1]), i+1, nil
				continue
			}
			inFence = false
			if strings.Contains(strings.Join(block, "\n"), "skip-permission") {
				continue // SkipPermission gates are evaluated by the translator
			}
			switch lang {
			case "yaml", "yml", "bash", "sh":
				// bash blocks carry manifests in heredocs (cat <<EOF | kubectl apply -f -).
				out = append(out, yamlBlockExprs(rel, start, block)...)
			case "cel":
				out = append(out, celBlockExprs(rel, start, block)...)
			}
			continue
		}
		if inFence {
			block = append(block, line)
			continue
		}
		if strings.HasPrefix(line, "## ") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		where := fmt.Sprintf("%s:%d", rel, i+1)
		if m := attrRowRe.FindStringSubmatch(line); m != nil && attrTableFiles[rel] {
			if typ := celTypeName(m[2]); typ != "" {
				out = append(out, docExpr{
					where:      where,
					expr:       fmt.Sprintf("type(%s) == %s", substitutePlaceholders(m[1]), typ),
					boolResult: true,
					mustBeTrue: true,
				})
			}
			continue
		}
		if strings.HasSuffix(rel, "cel-context.md") && strings.HasPrefix(section, "Extended CEL Functions") &&
			strings.HasPrefix(line, "|") && !strings.HasPrefix(line, "|--") && !strings.HasPrefix(line, "| Function") &&
			!strings.HasPrefix(line, "| Method") {
			cells := cellRe.FindAllStringSubmatch(line, -1)
			if len(cells) > 0 {
				out = append(out, docExpr{where: where, expr: cells[len(cells)-1][1]})
			}
		}
	}
	return out
}

// celTypeName maps a docs type column to a CEL type name, or "" when the row
// is not an attribute with a checkable type.
func celTypeName(docType string) string {
	switch {
	case docType == "string", docType == "int", docType == "bool":
		return docType
	case strings.HasPrefix(docType, "map"):
		return "map"
	}
	return ""
}

// substitutePlaceholders turns a documented attribute pattern into a concrete
// path that exists in the fixture.
func substitutePlaceholders(attr string) string {
	r := strings.NewReplacer(
		`["<envName>"]`, `["staging"]`,
		`["<stageName>"]`, `["staging"]`,
		".<envName>.", ".uat.",
		".<env>.", ".uat.",
		".<name>.", `["error-rate"].`,
		`"window-name"`, `"q4-holiday-freeze"`,
		`.*`, ``,
	)
	return r.Replace(attr)
}

// yamlBlockExprs returns the `expression:` values in a yaml code block. The
// value may be quoted or a block scalar (| or >).
func yamlBlockExprs(rel string, start int, block []string) []docExpr {
	var out []docExpr
	for i := 0; i < len(block); i++ {
		line := block[i]
		idx := strings.Index(line, "expression:")
		if idx < 0 || strings.TrimSpace(line[:idx]) != "" {
			continue
		}
		snippet := []string{line[idx:]}
		for j := i + 1; j < len(block); j++ {
			next := block[j]
			if strings.TrimSpace(next) != "" && len(next)-len(strings.TrimLeft(next, " ")) <= idx {
				break
			}
			snippet = append(snippet, next)
		}
		var v struct {
			Expression string `json:"expression"`
		}
		if err := yaml.Unmarshal([]byte(dedent(snippet, idx)), &v); err != nil {
			out = append(out, docExpr{where: fmt.Sprintf("%s:%d", rel, start+i+1), expr: "<unparseable yaml: " + err.Error() + ">", boolResult: true})
			continue
		}
		expr := strings.TrimSpace(v.Expression)
		if expr == "" || strings.HasPrefix(expr, "<") { // the CRD skeleton: expression: <string>
			continue
		}
		out = append(out, docExpr{where: fmt.Sprintf("%s:%d", rel, start+i+1), expr: expr, boolResult: true})
	}
	return out
}

// dedent removes the first n columns from the continuation lines of snippet.
func dedent(snippet []string, n int) string {
	var b strings.Builder
	for i, l := range snippet {
		if i > 0 {
			if len(l) >= n {
				l = l[n:]
			} else {
				l = strings.TrimLeft(l, " ")
			}
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// celBlockExprs splits a cel code block into expressions: comment lines and
// blank lines separate expressions; consecutive lines form one expression.
func celBlockExprs(rel string, start int, block []string) []docExpr {
	var out []docExpr
	var cur []string
	curStart := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, docExpr{
				where:      fmt.Sprintf("%s:%d", rel, start+curStart+1),
				expr:       strings.Join(cur, "\n"),
				boolResult: true,
			})
		}
		cur = nil
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(block, "\n")))
	for i := 0; sc.Scan(); i++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			flush()
			continue
		}
		if len(cur) == 0 {
			curStart = i
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

// manifestExprs returns spec.expression of every PolicyGate in a manifest
// file, except SkipPermission gates.
func manifestExprs(t *testing.T, path string) []docExpr {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	rel := strings.TrimPrefix(path, repoRoot+"/")
	var out []docExpr
	for i, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(data), -1) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Expression string `json:"expression"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue // not every yaml file under examples is a plain manifest
		}
		if obj.Kind != "PolicyGate" || obj.Spec.Expression == "" ||
			obj.Metadata.Labels["kardinal.io/type"] == "skip-permission" {
			continue
		}
		out = append(out, docExpr{
			where:      fmt.Sprintf("%s#%d/%s", rel, i, obj.Metadata.Name),
			expr:       strings.TrimSpace(obj.Spec.Expression),
			boolResult: true,
		})
	}
	return out
}
