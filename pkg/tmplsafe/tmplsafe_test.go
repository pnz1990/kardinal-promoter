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

var lim = tmplsafe.Limits{MaxOutput: 1 << 10, MaxFuncOutput: 512, MaxBuild: 4 << 10, MaxFuncCalls: 50, MaxExecTime: 200 * time.Millisecond}

func funcs() tmplsafe.FuncMap {
	f := tmplsafe.StringFuncs()
	f["fails"] = tmplsafe.Func{Fn: func() (string, error) { return "", assert.AnError }}
	f["slow"] = tmplsafe.Func{Fn: func() string { time.Sleep(300 * time.Millisecond); return "" }}
	f["section"] = tmplsafe.Const("## Section")
	return f
}

func testData() map[string]interface{} {
	return map[string]interface{}{"Name": "web", "Items": []string{"a", "b"}, "Big": strings.Repeat("y", 300),
		"Many": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}, "Map": map[string]string{"k": "v"},
		"N": 3, "Yes": true}
}

// doubling is the template that took 256 MiB through variables: a 16-byte
// string doubled 24 times.
func doubling() string {
	return `{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 24) + `{{$a}}`
}

// TestParse_Refuses: the constructs that loop, recurse or grow without
// output are refused before anything runs.
func TestParse_Refuses(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{"doubling with variables", doubling(), "variables are not allowed"},
		{"declaration", `{{ $x := .Name }}{{ $x }}`, "variables are not allowed"},
		{"assignment in if", `{{ if $x := .Name }}{{ end }}`, "variables are not allowed"},
		{"with variable", `{{ with $x := .Name }}{{ $x }}{{ end }}`, "variables are not allowed"},
		{"range over data", `{{ range .Items }}{{ . }}{{ end }}`, "range is not allowed"},
		{"range over a number", `{{ range 1000000000 }}x{{ end }}`, "range is not allowed"},
		{"range nested in if", `{{ if .Yes }}{{ range .Many }}{{ end }}{{ end }}`, "range is not allowed"},
		{"range with variables", `{{ range $i, $v := .Items }}{{ end }}`, "range is not allowed"},
		{"define", `{{ define "x" }}a{{ end }}b`, "define and block are not allowed"},
		{"block", `{{ block "x" . }}a{{ end }}`, "define and block are not allowed"},
		{"template", `{{ template "x" }}`, "template is not allowed"},
		{"printf", `{{ printf "%999999999d" 1 }}`, "printf is not available"},
		{"printf argument indexes", `{{ printf "%[1]s%[1]s%[1]s%[1]s" .Big }}`, "printf is not available"},
		{"printf in a pipeline", `{{ .Name | printf "%s" }}`, "printf is not available"},
		{"call", `{{ call .Fn }}`, "call is not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tmplsafe.Parse("t", tt.text, funcs(), lim)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestExecute_Bounds: every function, builtins included, is counted and its
// size charged to the render's budget before it runs; the output, the calls
// and the time are bounded.
func TestExecute_Bounds(t *testing.T) {
	tests := []struct {
		name, text, want, wantErr string
	}{
		{name: "plain", text: `{{ .Name | upper }}-{{ join "," .Items }}-{{ section }}`, want: "WEB-a,b-## Section"},
		{name: "builtins", text: `{{ if and (eq .Name "web" "x") (lt 1 .N) (not false) (or "" "z") }}{{ len .Many }}{{ index .Map "k" }}{{ index .Items 1 }}{{ slice .Name 1 2 }}{{ end }}`,
			want: "10vbe"},
		{name: "comparisons", text: `{{ ne .N 3 }} {{ le .N 3 }} {{ gt .N 2 }} {{ ge 2 .N }} {{ eq .Yes true }}`, want: "false true true false true"},
		{name: "with and else", text: `{{ with .Name }}{{ . }}{{ else }}none{{ end }}{{ with "" }}x{{ else }}-empty{{ end }}`, want: "web-empty"},
		{name: "print scalars", text: `{{ print .Name 1 true }}`, want: "web1 true"},
		{name: "print a map", text: `{{ print .Map }}`, wantErr: "print: takes strings, numbers and bools only"},
		{name: "print the data", text: `{{ print . }}`, wantErr: "print: takes strings, numbers and bools only"},
		{name: "html of a slice", text: `{{ html .Items }}`, wantErr: "html: takes strings, numbers and bools only"},
		{name: "print many strings stops at the cap", text: `{{ print` + strings.Repeat(" .Big", 50) + ` }}`, wantErr: "print: would build"},
		{name: "incomparable", text: `{{ eq .Name 1 }}`, wantErr: "incompatible types for comparison"},
		{name: "index out of range", text: `{{ index .Items 5 }}`, wantErr: "index out of range"},
		{name: "html growth", text: `{{ html .Big }}`, wantErr: "html: would build 1806 bytes, more than 512"},
		{name: "js growth", text: `{{ js .Big }}`, wantErr: "js: would build"},
		{name: "upper growth", text: `{{ upper .Big }}`, wantErr: "upper: would build 900 bytes"},
		{name: "replace with an empty old", text: `{{ replace "" "zz" "abc" }}`, wantErr: "replace: the string to replace is empty"},
		{name: "replace blowup", text: `{{ replace "y" "zzzz" .Big }}`, wantErr: "replace: would build 1200 bytes, more than 512"},
		{name: "join blowup", text: `{{ join "` + strings.Repeat("-", 100) + `" .Many }}`, wantErr: "join: would build 910 bytes"},
		{name: "budget", text: strings.Repeat(`{{ if trimSpace .Big }}{{ end }}`, 14), wantErr: "the template would build more than 4096 bytes"},
		{name: "a comparison reads at most MaxFuncOutput", text: `{{ if eq .Big .Big }}{{ end }}`, wantErr: "eq: would build 600 bytes, more than 512"},
		{name: "comparisons are charged", text: strings.Repeat(`{{ if eq "`+strings.Repeat("a", 200)+`" "`+strings.Repeat("a", 200)+`" }}{{ end }}`, 11),
			wantErr: "eq: the template would build more than 4096 bytes"},
		{name: "output cap", text: strings.Repeat(`{{ .Big }}`, 4), wantErr: "template output is too large (more than 1024 bytes)"},
		{name: "function error", text: `{{ fails }}`, wantErr: assert.AnError.Error()},
		{name: "too many calls", text: strings.Repeat(`{{ not true }}`, 51), wantErr: "more than 50 function calls"},
		{name: "too slow", text: `{{ slow }}{{ not true }}`, wantErr: "template took longer than 200ms"},
		{name: "too slow, then a write", text: `{{ slow }}x`, wantErr: "template took longer than 200ms"},
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
	tmpl, err := tmplsafe.Parse("t", `{{ index .Items 0 | upper }}{{ index .Items 1 | upper }}`, funcs(), lim)
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

// grammar generates random templates from every construct Parse accepts.
type grammar struct {
	r *rand.Rand
}

var (
	genPaths = []string{".Name", ".Big", "$.Big", "$.Name", ".Many", ".Items", ".Map", ".N", ".Yes", "."}
	genStrFn = []string{"upper", "lower", "trimSpace", "html", "js", "urlquery", "print", "println", "mdcell"}
)

func (g *grammar) str() string {
	if g.r.Intn(3) == 0 {
		return `"` + strings.Repeat(string(rune('a'+g.r.Intn(26))), g.r.Intn(300)) + `"`
	}
	return []string{".Name", ".Big", "$.Big", "$.Name"}[g.r.Intn(4)]
}

func (g *grammar) expr(depth int) string {
	if depth > 3 {
		return g.str()
	}
	switch g.r.Intn(16) {
	case 0, 1:
		return g.str()
	case 2:
		return `(replace "` + string(rune('a'+g.r.Intn(26))) + `" "` + strings.Repeat("z", g.r.Intn(100)) + `" ` + g.expr(depth+1) + `)`
	case 3:
		return `(join "` + strings.Repeat("-", g.r.Intn(100)) + `" $.Many)`
	case 4:
		return `(print ` + g.expr(depth+1) + ` ` + g.expr(depth+1) + `)`
	case 5:
		return "(section)"
	case 6:
		return `(truncate ` + []string{"0", "7", "100000", "-3"}[g.r.Intn(4)] + ` ` + g.expr(depth+1) + `)`
	case 7:
		return `(default ` + g.str() + ` ` + g.expr(depth+1) + `)`
	case 8:
		return `(trimPrefix "a" ` + g.expr(depth+1) + `)`
	case 9:
		return `(index $.Items ` + []string{"0", "1"}[g.r.Intn(2)] + `)`
	case 10:
		return `(index $.Map "k")`
	case 11:
		return `(slice ` + g.str() + ` 0)`
	default:
		return "(" + genStrFn[g.r.Intn(len(genStrFn))] + " " + g.expr(depth+1) + ")"
	}
}

func (g *grammar) cond() string {
	switch g.r.Intn(9) {
	case 0:
		return "(eq " + g.expr(1) + " " + g.expr(1) + ")"
	case 1:
		return "(ne " + g.expr(1) + " " + g.expr(1) + ")"
	case 2:
		return "(lt (len " + genPaths[g.r.Intn(len(genPaths)-3)] + ") 5)"
	case 3:
		return "(and $.Yes " + g.expr(1) + ")"
	case 4:
		return "(or " + g.expr(1) + " $.N)"
	case 5:
		return "(not " + g.expr(1) + ")"
	case 6:
		return "(contains \"y\" " + g.expr(1) + ")"
	case 7:
		return "(hasPrefix \"w\" " + g.expr(1) + ")"
	}
	return "(ge $.N 2)"
}

func (g *grammar) node(depth int) string {
	switch n := g.r.Intn(10); {
	case n < 5 || depth > 2:
		return "{{ " + g.expr(0) + " }}"
	case n < 7:
		return "{{ if " + g.cond() + " }}" + g.nodes(depth+1) + "{{ else if " + g.cond() + " }}" + g.nodes(depth+1) + "{{ else }}" + g.nodes(depth+1) + "{{ end }}"
	case n < 8:
		return "{{ with " + genPaths[g.r.Intn(len(genPaths))] + " }}" + g.nodes(depth+1) + "{{ else }}x{{ end }}"
	case n < 9:
		return "{{- " + g.expr(0) + " -}}"
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

// fuzzFuncs are funcs() without slow, plus mdcell (a 2x growth like the PR
// templates').
func fuzzFuncs() tmplsafe.FuncMap {
	f := funcs()
	delete(f, "slow")
	f["mdcell"] = tmplsafe.Func{Fn: func(s string) string { return strings.ReplaceAll(s, "|", `\|`) },
		Size: func(a []interface{}) (int, error) { s, _ := a[0].(string); return 2 * len(s), nil }}
	return f
}

// largeData is testData with every value at the PR data's caps: 1 KiB
// strings and a list of 20.
func largeData() map[string]interface{} {
	d := testData()
	d["Big"] = strings.Repeat("y|", 512)
	d["Name"] = strings.Repeat("w", 1024)
	many := make([]string, 20)
	for i := range many {
		many[i] = strings.Repeat("m", 1024)
	}
	d["Many"] = many
	return d
}

// checkBounded parses and renders text with the default limits on large
// data and fails when that allocates more than allocBound, takes longer
// than timeBound, or leaves a goroutine behind. It reports whether the
// template parsed.
func checkBounded(t *testing.T, text string, timeBound time.Duration) bool {
	const allocBound = 64 << 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	goroutines := runtime.NumGoroutine()
	start := time.Now()
	tmpl, err := tmplsafe.Parse("t", text, fuzzFuncs(), tmplsafe.DefaultLimits)
	if err == nil {
		_, _ = tmpl.Execute(largeData())
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > allocBound {
		t.Fatalf("render allocated %d bytes (bound %d) for a template of %d bytes:\n%.500s", alloc, allocBound, len(text), text)
	}
	if elapsed > timeBound {
		t.Fatalf("render took %s (bound %s):\n%.500s", elapsed, timeBound, text)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Fatalf("%d goroutines after Execute, %d before", n, goroutines)
	}
	return err == nil
}

// TestRandomTemplatesAreBounded renders random templates of up to the
// CRD's largest template (16 KiB), from every construct Parse allows, on
// data at its caps, and checks that each render stays under a fixed
// allocation and time bound and starts no goroutine that outlives it.
func TestRandomTemplatesAreBounded(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 30
	}
	parsed := 0
	for seed := int64(0); seed < int64(n); seed++ {
		if checkBounded(t, (&grammar{r: rand.New(rand.NewSource(seed))}).template(16<<10), 2*time.Second) {
			parsed++
		}
	}
	assert.Greater(t, parsed, n*9/10, "almost every generated template is allowed, so its render is measured")
}

// TestWorstCaseTemplate is the slowest template found at the CRD's largest
// size (16 KiB): every action an expensive call on the largest data. Parse
// and Execute together must take under 50 ms (the fastest of 5 runs, so a
// busy machine does not fail it; times 10 under the race detector), on the
// calling goroutine, and stay under checkBounded's allocation bound.
func TestWorstCaseTemplate(t *testing.T) {
	cases := map[string]string{
		"escapes":                    `{{ html $.Big }}`,
		"comparisons":                `{{ if eq $.Name $.Name }}{{ end }}`,
		"case":                       `{{ upper (index $.Many 19) }}`,
		"join":                       `{{ join "," $.Many }}`,
		"cheap calls":                `{{ not $.Yes }}`,
		"print the data":             `{{ print` + strings.Repeat(" .", 8000) + ` }}`,
		"print data in every action": `{{ print . . . . . . . . }}`,
		"writes":                     `{{ $.Name }}`,
	}
	for name, action := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Repeat(action, (16<<10)/len(action))
			require.LessOrEqual(t, len(text), 16<<10)
			checkBounded(t, text, 2*time.Second)
			fastest := time.Hour
			for range 5 {
				start := time.Now()
				tmpl, err := tmplsafe.Parse("t", text, fuzzFuncs(), tmplsafe.DefaultLimits)
				require.NoError(t, err)
				_, _ = tmpl.Execute(largeData())
				fastest = min(fastest, time.Since(start))
			}
			assert.Less(t, fastest, 50*time.Millisecond*raceSlowdown)
		})
	}
}

// FuzzTemplates is TestRandomTemplatesAreBounded driven by the fuzzer: the
// first input seeds the grammar, and the second is tried as a template too.
func FuzzTemplates(f *testing.F) {
	f.Add(int64(1), `{{ html $.Big }}{{ if eq .Name .Name }}{{ index .Many 3 }}{{ end }}`)
	f.Add(int64(2), doubling())
	f.Add(int64(3), `{{ printf "%[1]s%[1]s" .Big }}`)
	f.Add(int64(4), `{{ range .Many }}{{ . }}{{ end }}`)
	f.Fuzz(func(t *testing.T, seed int64, raw string) {
		if len(raw) > 16<<10 {
			raw = raw[:16<<10]
		}
		checkBounded(t, raw, 2*time.Second)
		checkBounded(t, (&grammar{r: rand.New(rand.NewSource(seed))}).template(16<<10), 2*time.Second)
	})
}
