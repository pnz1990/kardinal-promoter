// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tmplsafe_test

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tmplsafe"
)

var lim = tmplsafe.Limits{MaxOutput: 1 << 10, MaxFuncOutput: 512, MaxBuild: 4 << 10, MaxRangeDepth: 2, MaxRanges: 3,
	MaxIterations: 20, MaxFuncCalls: 50, MaxExecTime: 200 * time.Millisecond}

func funcs() tmplsafe.FuncMap {
	f := tmplsafe.StringFuncs()
	f["fails"] = tmplsafe.Func{Fn: func() (string, error) { return "", assert.AnError }}
	f["slow"] = tmplsafe.Func{Fn: func() string { time.Sleep(time.Second); return "" }}
	f["section"] = tmplsafe.Const("## Section")
	return f
}

func testData() map[string]interface{} {
	return map[string]interface{}{"Name": "web", "Items": []string{"a", "b"}, "Big": strings.Repeat("y", 300),
		"Many": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}, "Map": map[string]string{"k": "v"}}
}

// doubling is the template that took 256 MiB through variables: a 16-byte
// string doubled 24 times.
func doubling() string {
	return `{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 24) + `{{$a}}`
}

// TestParse_Refuses: the constructs that make a template grow or spin
// without output are refused before anything runs.
func TestParse_Refuses(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{"doubling with variables", doubling(), "variables are not allowed"},
		{"declaration", `{{ $x := .Name }}{{ $x }}`, "variables are not allowed"},
		{"assignment in if", `{{ if $x := .Name }}{{ end }}`, "variables are not allowed"},
		{"range variables", `{{ range $i, $v := .Items }}{{ end }}`, "variables are not allowed"},
		{"with variable", `{{ with $x := .Name }}{{ $x }}{{ end }}`, "variables are not allowed"},
		{"define", `{{ define "x" }}a{{ end }}b`, "define and block are not allowed"},
		{"block", `{{ block "x" . }}a{{ end }}`, "define and block are not allowed"},
		{"template", `{{ template "x" }}`, "template is not allowed"},
		{"printf", `{{ printf "%999999999d" 1 }}`, "printf is not available"},
		{"printf argument indexes", `{{ printf "%[1]s%[1]s%[1]s%[1]s" .Big }}`, "printf is not available"},
		{"printf in a pipeline", `{{ .Name | printf "%s" }}`, "printf is not available"},
		{"call", `{{ call .Fn }}`, "call is not available"},
		{"the tick", `{{ _tmplsafeTick }}`, "unknown function"},
		{"range over a number", `{{ range 1000000000 }}x{{ end }}`, "range takes a data path only"},
		{"range over a parenthesised number", `{{ range (1000000000) }}x{{ end }}`, "range takes a data path only"},
		{"range over a function", `{{ range len .Big }}x{{ end }}`, "range takes a data path only"},
		{"range over a pipeline", `{{ range .Items | len }}x{{ end }}`, "range takes a data path only"},
		{"range over a chain", `{{ range (.Items).X }}x{{ end }}`, "range takes a data path only"},
		{"too many ranges", strings.Repeat(`{{ range .Items }}{{ end }}`, 4), "more than 3 ranges are not allowed"},
		{"deep ranges", `{{ range .Items }}{{ range $.Items }}{{ range $.Items }}{{ end }}{{ end }}{{ end }}`, "ranges nested more than 2 deep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tmplsafe.Parse("t", tt.text, funcs(), lim)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestExecute_Bounds: every function's size is computed before it runs and
// charged to the render's budget; ranges count their iterations; the output,
// the calls and the time are bounded.
func TestExecute_Bounds(t *testing.T) {
	tests := []struct {
		name, text, want, wantErr string
	}{
		{name: "plain", text: `{{ .Name | upper }}-{{ range .Items }}{{ . }}{{ end }}-{{ join "," .Items }}-{{ section }}`, want: "WEB-ab-a,b-## Section"},
		{name: "two ranges", text: `{{ range .Items }}{{ range $.Items }}x{{ end }}{{ end }}`, want: "xxxx"},
		{name: "print scalars", text: `{{ print .Name 1 true }}`, want: "web1 true"},
		{name: "print a map", text: `{{ print .Map }}`, wantErr: "print: takes strings, numbers and bools only"},
		{name: "html growth", text: `{{ html .Big }}`, wantErr: "html: would build 1806 bytes, more than 512"},
		{name: "js growth", text: `{{ js .Big }}`, wantErr: "js: would build"},
		{name: "replace with an empty old", text: `{{ replace "" "zz" "abc" }}`, wantErr: "replace: the string to replace is empty"},
		{name: "replace blowup", text: `{{ replace "y" "zzzz" .Big }}`, wantErr: "replace: would build 1200 bytes, more than 512"},
		{name: "join blowup", text: `{{ join "` + strings.Repeat("-", 100) + `" .Many }}`, wantErr: "join: would build 910 bytes"},
		{name: "budget", text: strings.Repeat(`{{ if trimSpace .Big }}{{ end }}`, 14), wantErr: "the template would build more than 4096 bytes"},
		{name: "iterations", text: `{{ range .Many }}{{ range $.Many }}{{ end }}{{ end }}`, wantErr: "more than 20 range iterations"},
		{name: "output cap", text: strings.Repeat(`{{ .Big }}`, 4), wantErr: "template output is too large (more than 1024 bytes)"},
		{name: "function error", text: `{{ fails }}`, wantErr: assert.AnError.Error()},
		{name: "too many calls", text: strings.Repeat(`{{ upper "a" }}`, 51), wantErr: "more than 50 function calls"},
		{name: "too slow", text: `{{ slow }}`, wantErr: "template took longer than 200ms"},
		{name: "missing field", text: `{{ .Nope }}`, wantErr: "map has no entry for key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := tmplsafe.Parse("t", tt.text, funcs(), lim)
			require.NoError(t, err)
			got, err := tmpl.Execute(testData())
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestExecute_Concurrent: one parsed template renders on several goroutines
// at once, each with its own budget (go test -race).
func TestExecute_Concurrent(t *testing.T) {
	tmpl, err := tmplsafe.Parse("t", `{{ range .Items }}{{ . | upper }}{{ end }}`, funcs(), lim)
	require.NoError(t, err)
	done := make(chan string)
	for i := 0; i < 8; i++ {
		go func() {
			out, _ := tmpl.Execute(testData())
			done <- out
		}()
	}
	for i := 0; i < 8; i++ {
		assert.Equal(t, "AB", <-done)
	}
}

// grammar generates random templates from what Parse may accept, with the
// functions that build the most.
type grammar struct {
	r      *rand.Rand
	ranges int
}

var (
	genPaths = []string{".Name", ".Big", "$.Big", "$.Name", "."}
	genFuncs = []string{"upper", "lower", "trimSpace", "html", "js", "urlquery", "print", "println"}
)

func (g *grammar) str() string {
	if g.r.Intn(3) == 0 {
		return `"` + strings.Repeat(string(rune('a'+g.r.Intn(26))), g.r.Intn(200)) + `"`
	}
	return genPaths[g.r.Intn(len(genPaths))]
}

func (g *grammar) expr(depth int) string {
	switch n := g.r.Intn(9); {
	case n < 2 || depth > 3:
		return g.str()
	case n == 2:
		return `(replace "` + string(rune('a'+g.r.Intn(26))) + `" "` + strings.Repeat("z", g.r.Intn(100)) + `" ` + g.expr(depth+1) + `)`
	case n == 3:
		return `(join "` + strings.Repeat("-", g.r.Intn(100)) + `" $.Many)`
	case n == 4:
		return `(print ` + g.expr(depth+1) + ` ` + g.expr(depth+1) + `)`
	case n == 5:
		return "(section)"
	default:
		return "(" + genFuncs[g.r.Intn(len(genFuncs))] + " " + g.expr(depth+1) + ")"
	}
}

func (g *grammar) node(depth int) string {
	switch n := g.r.Intn(10); {
	case n < 5 || depth > 1:
		return "{{ " + g.expr(0) + " }}"
	case n < 7 && g.ranges < tmplsafe.DefaultLimits.MaxRanges:
		g.ranges++
		return "{{ range $.Many }}" + g.nodes(depth+1) + "{{ end }}"
	case n < 8:
		return "{{ if " + g.expr(0) + " }}" + g.nodes(depth+1) + "{{ else }}" + g.nodes(depth+1) + "{{ end }}"
	case n < 9:
		return "{{ with .Name }}" + g.nodes(depth+1) + "{{ end }}"
	default:
		return strings.Repeat("x", g.r.Intn(100))
	}
}

func (g *grammar) nodes(depth int) string {
	var b strings.Builder
	for i := g.r.Intn(6); i >= 0; i-- {
		b.WriteString(g.node(depth))
	}
	return b.String()
}

// template returns a random template of at most max bytes.
func (g *grammar) template(max int) string {
	var b strings.Builder
	for b.Len() < max {
		s := g.node(0)
		if b.Len()+len(s) > max {
			break
		}
		b.WriteString(s)
	}
	return b.String()
}

// checkBounded parses and renders text with the default limits and fails
// when that allocates more than allocBound or takes longer than timeBound.
// It reports whether the template parsed.
func checkBounded(t *testing.T, text string) bool {
	const allocBound = 64 << 20
	const timeBound = 2 * time.Second
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	tmpl, err := tmplsafe.Parse("t", text, funcs(), tmplsafe.DefaultLimits)
	if err == nil {
		_, _ = tmpl.Execute(testData())
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > allocBound {
		t.Fatalf("render allocated %d bytes (bound %d) for a template of %d bytes:\n%.500s", alloc, allocBound, len(text), text)
	}
	if elapsed > timeBound {
		t.Fatalf("render took %s (bound %s):\n%.500s", elapsed, timeBound, text)
	}
	return err == nil
}

// TestRandomTemplatesAreBounded renders random templates of up to the
// CRD's largest template (16 KiB) from the allowed grammar and checks that
// each render stays under a fixed allocation and time bound.
func TestRandomTemplatesAreBounded(t *testing.T) {
	n := 200
	if testing.Short() {
		n = 20
	}
	parsed := 0
	for seed := int64(0); seed < int64(n); seed++ {
		if checkBounded(t, (&grammar{r: rand.New(rand.NewSource(seed))}).template(16<<10)) {
			parsed++
		}
	}
	assert.Greater(t, parsed, n/2, "most generated templates are allowed, so their renders are measured")
}

// FuzzTemplates is TestRandomTemplatesAreBounded driven by the fuzzer: the
// first input seeds the grammar, and the second is tried as a template too.
func FuzzTemplates(f *testing.F) {
	f.Add(int64(1), `{{ range .Many }}{{ html $.Big }}{{ end }}`)
	f.Add(int64(2), doubling())
	f.Add(int64(3), `{{ printf "%[1]s%[1]s" .Big }}`)
	f.Fuzz(func(t *testing.T, seed int64, raw string) {
		if len(raw) > 16<<10 {
			raw = raw[:16<<10]
		}
		checkBounded(t, raw)
		checkBounded(t, (&grammar{r: rand.New(rand.NewSource(seed))}).template(16<<10))
	})
}
