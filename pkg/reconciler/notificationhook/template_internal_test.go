// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tmplsafe"
)

// doublingBody is the QA reproduction: 24 assignments double a 16-byte
// string to 256 MiB inside the 16 KiB body limit.
func doublingBody() string {
	return `{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 24) + `{{len $a}}`
}

func TestParseBodyTemplate_RefusesVariables(t *testing.T) {
	tests := map[string]struct{ body, want string }{
		"doubling (QA repro)":   {doublingBody(), "variables are not allowed"},
		"assignment only":       {`{{$ = .Event}}`, "variables are not allowed"},
		"declaration in if":     {`{{if $x := .Event}}{{$x}}{{end}}`, "variables are not allowed"},
		"declaration in with":   {`{{with $x := .Event}}x{{end}}`, "variables are not allowed"},
		"declaration in else":   {`{{if .Event}}a{{else}}{{$x := 1}}{{end}}`, "variables are not allowed"},
		"declaration in nested": {`{{if .Event}}{{with .Bundle}}{{$y := .}}{{end}}{{end}}`, "variables are not allowed"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(tt.body), 16384, "fits the CRD limit")
			_, err := parseBodyTemplate(tt.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	// $ (the data) and dot are still usable.
	_, err := parseBodyTemplate(`{{ $.Event }} {{ with .Bundle }}{{ . }}{{ end }}`)
	assert.NoError(t, err)
}

// TestRenderTemplate_BoundsFunctionAllocation: no function call may build
// more than 64 KiB, and the calls of one render build at most maxFuncOutput
// bytes together, however they nest; each bound is charged before the call.
func TestRenderTemplate_BoundsFunctionAllocation(t *testing.T) {
	// Fields are cut to 4 KiB, so a 60 KiB string is built by joining one.
	data := &TemplateData{Event: "Bundle.Failed", Message: strings.Repeat("m", maxDataField)}
	big := "(print" + strings.Repeat(" .Message", 15) + ")"
	tests := map[string]struct{ body, want string }{
		"print over the call limit":    {`{{len (print ` + big + ` ` + big + `)}}`, "print: would build"},
		"println over the call limit":  {`{{len (println ` + big + ` ` + big + `)}}`, "println: would build"},
		"html over the call limit":     {`{{len (html ` + big + `)}}`, "html: would build"},
		"js over the call limit":       {`{{len (js ` + big + `)}}`, "js: would build"},
		"urlquery over the call limit": {`{{len (urlquery ` + big + `)}}`, "urlquery: would build"},
		"json charges 6x its input":    {`{{len (json (print ` + big + `))}}`, "json: would build"},

		"budget across calls": {strings.Repeat(`{{len (lower (print .Message .Message .Message .Message))}}`, 6),
			"the template would build more than 262144 bytes"},
		"budget across nested calls": {strings.Repeat(`{{len (print (print .Message .Message .Message .Message .Message .Message .Message .Message))}}`, 5),
			"the template would build more than 262144 bytes"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpl, err := parseBodyTemplate(tt.body)
			require.NoError(t, err)
			_, err = renderTemplate(tmpl, data, "text/plain")
			require.Error(t, err)
			assert.ErrorIs(t, err, errTemplate)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	// The budget is per render: the same template renders again.
	tmpl, err := parseBodyTemplate(`{{len (upper (print .Message .Message .Message))}}`)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		out, err := renderTemplate(tmpl, data, "text/plain")
		require.NoError(t, err)
		assert.Equal(t, "12288", string(out))
	}
}

// TestRenderTemplate_WorstCaseAllocation renders the most allocating body
// that parses within 16 KiB (nested calls on a 60 KiB field) and checks the
// render allocates a few MiB at most, not hundreds.
func TestRenderTemplate_WorstCaseAllocation(t *testing.T) {
	data := &TemplateData{Message: strings.Repeat("m", 60<<10)}
	big := "(print" + strings.Repeat(" .Message", 15) + ")"
	body := strings.Repeat(`{{len (print (upper (lower `+big+`)))}}`, 90)
	require.LessOrEqual(t, len(body), 16384)
	tmpl, err := parseBodyTemplate(body)
	require.NoError(t, err)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = renderTemplate(tmpl, data, "text/plain")
	runtime.ReadMemStats(&after)
	require.Error(t, err, "the budget stops it")
	assert.ErrorIs(t, err, errTemplate)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20), "allocated %d bytes", after.TotalAlloc-before.TotalAlloc)

	// The data as an operand, as often as fits: refused before formatting.
	printData, err := parseBodyTemplate("{{print" + strings.Repeat(" .", 16384/2-8) + "}}")
	require.NoError(t, err)
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = renderTemplate(printData, data, "text/plain")
	runtime.ReadMemStats(&after)
	require.Error(t, err)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20), "print of the data allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
}

// TestRenderTemplate_TruncatesData: every field the template sees is cut to
// maxDataField bytes, on a rune boundary.
func TestRenderTemplate_TruncatesData(t *testing.T) {
	tests := []struct {
		name, msg string
		want      int
	}{
		{"short", "hello", 5},
		{"long ASCII", strings.Repeat("m", 60<<10), maxDataField},
		{"long multibyte keeps whole runes", strings.Repeat("é", 3000), maxDataField}, // 2 bytes each
		{"cut inside a rune", "xx" + strings.Repeat("€", 2000), maxDataField - 2},     // 2 + 3n bytes
	}
	tmpl, err := parseBodyTemplate(`{{len .Message}}`)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := renderTemplate(tmpl, &TemplateData{Message: tt.msg}, "text/plain")
			require.NoError(t, err)
			assert.Equal(t, strconv.Itoa(tt.want), string(out))
		})
	}
}

// TestRenderTemplate_CountedBuiltins: the comparison, logic and indexing
// builtins still work, and every call counts against maxFuncCalls.
func TestRenderTemplate_CountedBuiltins(t *testing.T) {
	// The count, not the 500 ms deadline, stops the long bodies.
	data := &TemplateData{Event: "Bundle.Failed", Environment: "prod", Message: "abc"}
	tests := []struct{ body, want, err string }{
		{body: `{{if eq .Environment "prod"}}P{{end}}`, want: "P"},
		{body: `{{if eq .Environment "test" "prod"}}P{{end}}`, want: "P"},
		{body: `{{if ne .Environment "prod"}}N{{else}}S{{end}}`, want: "S"},
		{body: `{{if and (eq .Event "Bundle.Failed") (lt (len .Message) 5)}}Y{{end}}`, want: "Y"},
		{body: `{{or .Bundle "none"}}`, want: "none"},
		{body: `{{and .Message .Environment}}`, want: "prod"},
		{body: `{{not .Bundle}}`, want: "true"},
		{body: `{{gt 3 2}} {{ge 2 2}} {{le 1.5 2.5}} {{lt "a" "b"}}`, want: "true true true true"},
		{body: `{{eq true true}} {{ne true false}}`, want: "true true"},
		{body: `{{slice .Message 1 2}}{{index .Message 0}}`, want: "b97"},
		{body: `{{lt .Message 3}}`, err: "incompatible types for comparison"},
		{body: `{{slice .Message 2 9}}`, err: "out of range"},
		{body: strings.Repeat(`{{eq "a" "a"}}`, maxFuncCalls+1), err: "more than 2000 function calls"},
		{body: strings.Repeat(`{{not .Message}}`, maxFuncCalls+1), err: "more than 2000 function calls"},
	}
	for _, tt := range tests {
		name := tt.body
		if len(name) > 60 {
			name = name[:60]
		}
		t.Run(name, func(t *testing.T) {
			tmpl, err := parseBodyTemplate(tt.body)
			require.NoError(t, err)
			out, err := renderTemplate(tmpl, data, "text/plain")
			if tt.err != "" {
				require.Error(t, err)
				assert.ErrorIs(t, err, errTemplate)
				assert.Contains(t, err.Error(), tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(out))
		})
	}
}

// TestParseBodyTemplate_RefusesCall: call runs a function value and is
// refused like printf.
func TestParseBodyTemplate_RefusesCall(t *testing.T) {
	_, err := parseBodyTemplate(`{{call .Message}}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "call is not available")
}

// TestRenderTemplate_WorstCaseTime renders the costliest bodies that fit in
// 16 KiB: the most calls, the most bytes built, nested escapes, and the data
// as every operand. The budgets, not the clock, decide each: a render either
// completes or stops on a call, byte or argument budget, never on the
// deadline (500 ms, a backstop). Execute runs on the caller's goroutine and
// the timer is stopped, so no goroutine is left behind.
func TestRenderTemplate_WorstCaseTime(t *testing.T) {
	data := &TemplateData{Message: strings.Repeat("<m>", 60<<10)}
	big := "(print" + strings.Repeat(" .Message", 15) + ")"
	bodies := map[string]string{
		"most calls":        strings.Repeat(`{{eq (len (slice .Message 1)) 2}}`, 16384/34),
		"most building":     strings.Repeat(`{{len (js (html `+big+`))}}`, 16384/165),
		"nested escapes":    strings.Repeat(`{{len (json (js (html (urlquery .Message))))}}`, 16384/50),
		"print of the data": "{{print" + strings.Repeat(" .", 16384/2-8) + "}}",
	}
	budget := func(err error) bool {
		if err == nil || errors.Is(err, tmplsafe.ErrOutputTooLarge) {
			return true
		}
		msg := err.Error()
		for _, b := range []string{"function calls", "would build", "takes strings, numbers and bools only"} {
			if strings.Contains(msg, b) {
				return true
			}
		}
		return false
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(body), 16384)
			tmpl, err := parseBodyTemplate(body)
			require.NoError(t, err)
			before := runtime.NumGoroutine()
			_, err = renderTemplate(tmpl, data, "text/plain")
			assert.NotErrorIs(t, err, tmplsafe.ErrStopped, "a budget stops it, not the clock")
			assert.True(t, budget(err), "completes or stops on a budget: %v", err)
			assert.LessOrEqual(t, runtime.NumGoroutine(), before, "no goroutine left after Execute")
		})
	}
}

// TestRenderTemplate_RefusesNonScalarArguments: function arguments may only
// be strings, numbers or bools. The data itself (a struct), and anything a
// pipeline turns into a pointer, map or slice, is refused before it is
// formatted: {{print . . .}} used to format the whole data per operand.
func TestRenderTemplate_RefusesNonScalarArguments(t *testing.T) {
	data := &TemplateData{Message: strings.Repeat("m", 60<<10)}
	bodies := []string{
		`{{print .}}`, `{{json .}}`, `{{html . .}}`, `{{upper (print .)}}`,
		"{{print" + strings.Repeat(" .", 8000) + "}}",
	}
	for _, body := range bodies {
		name := body
		if len(name) > 30 {
			name = name[:30]
		}
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(body), 16384)
			tmpl, err := parseBodyTemplate(body)
			require.NoError(t, err)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, err = renderTemplate(tmpl, data, "text/plain")
			runtime.ReadMemStats(&after)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "takes strings, numbers and bools only")
			assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20), "allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
		})
	}
}

// TestRenderTemplate_DeadlineStopsARender: a function call that blocks past
// the render deadline is not interrupted (text/template cannot be), but the
// next call or write ends the render with tmplsafe.ErrStopped. That error is
// retryable: not errTemplate.
func TestRenderTemplate_DeadlineStopsARender(t *testing.T) {
	useShortDeadline(t)
	for name, body := range map[string]string{
		"next call":  `{{slow}}{{print "x"}}`,
		"next write": `{{slow}}after`,
	} {
		t.Run(name, func(t *testing.T) {
			tmpl, err := parseTemplate(body, tmplsafe.FuncMap{"slow": {Fn: func() string {
				time.Sleep(renderDeadline + 15*time.Millisecond)
				return ""
			}}})
			require.NoError(t, err)
			start := time.Now()
			_, err = renderTemplate(tmpl, &TemplateData{}, "text/plain")
			require.Error(t, err)
			assert.ErrorIs(t, err, tmplsafe.ErrStopped)
			assert.NotErrorIs(t, err, errTemplate, "out of time is retried, not given up on")
			assert.Less(t, time.Since(start), 100*time.Millisecond)
		})
	}
}

// useShortDeadline makes the render deadline 20 ms for one test about the
// deadline itself, so it does not wait the production 500 ms. No test in the
// package runs in parallel with it.
func useShortDeadline(t *testing.T) {
	t.Helper()
	renderDeadline = 20 * time.Millisecond
	t.Cleanup(func() { renderDeadline = maxRenderTime })
}

// TestRenderTemplate_Functions: the functions docs/notifications.md lists,
// tmplsafe's string functions included, in pipelines.
func TestRenderTemplate_Functions(t *testing.T) {
	data := &TemplateData{Event: "Bundle.Failed", Pipeline: "  app  ", Environment: "prod", Message: "héllo wörld"}
	tests := map[string]string{
		`{{ json .Message }}`:                             `"héllo wörld"`,
		`{{ .Message | truncate 5 }}`:                     "héll…",
		`{{ .Message | truncate 50 }}`:                    "héllo wörld",
		`{{ .Pipeline | trimSpace | upper }}`:             "APP",
		`{{ .Bundle | default "none" }}`:                  "none",
		`{{ .Event | replace "." "-" | lower }}`:          "bundle-failed",
		`{{ .Event | trimPrefix "Bundle." }}`:             "Failed",
		`{{ if .Event | hasPrefix "Bundle." }}B{{ end }}`: "B",
		`{{ if contains "rod" .Environment }}P{{ end }}`:  "P",
		`{{ print "kardinal/" .Environment }}`:            "kardinal/prod",
	}
	for body, want := range tests {
		t.Run(body, func(t *testing.T) {
			tmpl, err := parseBodyTemplate(body)
			require.NoError(t, err)
			out, err := renderTemplate(tmpl, data, "text/plain")
			require.NoError(t, err)
			assert.Equal(t, want, string(out))
		})
	}
	_, err := parseBodyTemplate("  ")
	assert.ErrorContains(t, err, "empty template")
}

// TestParseBodyTemplate_HookSpecificErrors: the range refusal names what a
// hook template has, not the PR template's list functions, and join (for
// lists, which hook data has none of) is not a function here.
func TestParseBodyTemplate_HookSpecificErrors(t *testing.T) {
	_, err := parseBodyTemplate(`{{ range .Message }}{{ end }}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "range is not allowed: the template language has no loops; an event is one notification")
	assert.NotContains(t, err.Error(), "provenanceTable")

	_, err = parseBodyTemplate(`{{ join "," .Message }}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `function "join" not defined`)
}
