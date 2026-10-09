// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doublingBody is the QA reproduction: 24 assignments double a 16-byte
// string to 256 MiB inside the 16 KiB body limit.
func doublingBody() string {
	return `{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 24) + `{{len $a}}`
}

func TestParseBodyTemplate_RefusesVariables(t *testing.T) {
	tests := map[string]struct{ body, want string }{
		"doubling (QA repro)":   {doublingBody(), "variable declaration is not allowed"},
		"assignment only":       {`{{$ = .Event}}`, "variable assignment is not allowed"},
		"declaration in if":     {`{{if $x := .Event}}{{$x}}{{end}}`, "variable declaration is not allowed"},
		"declaration in with":   {`{{with $x := .Event}}x{{end}}`, "variable declaration is not allowed"},
		"declaration in else":   {`{{if .Event}}a{{else}}{{$x := 1}}{{end}}`, "variable declaration is not allowed"},
		"declaration in nested": {`{{if .Event}}{{with .Bundle}}{{$y := .}}{{end}}{{end}}`, "variable declaration is not allowed"},
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

// TestRenderTemplate_BoundsFunctionAllocation: every string-producing
// function refuses an input over 64 KiB, and the functions of one render
// produce at most maxFuncOutput bytes together, however the calls nest.
func TestRenderTemplate_BoundsFunctionAllocation(t *testing.T) {
	// Fields are cut to 4 KiB, so a 60 KiB string is built by joining one.
	data := &TemplateData{Event: "Bundle.Failed", Message: strings.Repeat("m", maxDataField)}
	big := "(print" + strings.Repeat(" .Message", 15) + ")"
	tests := map[string]struct{ body, want string }{
		"print over input limit":             {`{{len (print ` + big + ` ` + big + `)}}`, "print: input is over 65536 bytes"},
		"println over input limit":           {`{{len (println ` + big + ` ` + big + `)}}`, "println: input is over"},
		"html over input limit":              {`{{len (html ` + big + ` ` + big + `)}}`, "html: input is"},
		"js over input limit":                {`{{len (js ` + big + ` ` + big + `)}}`, "js: input is"},
		"urlquery over input limit":          {`{{len (urlquery ` + big + ` ` + big + `)}}`, "urlquery: input is"},
		"json charges 6x its input up front": {`{{len (json (print ` + big + ` "x"))}}`, "json: function output is over 262144 bytes in total"},

		"budget across calls": {strings.Repeat(`{{len (upper `+big+`)}}`, 5),
			"function output is over 262144 bytes in total"},
		"budget across nested calls": {strings.Repeat(`{{len (lower (print `+big+`))}}`, 3),
			"function output is over 262144 bytes in total"},
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
	tmpl, err := parseBodyTemplate(`{{len (upper ` + big + `)}}`)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		out, err := renderTemplate(tmpl, data, "text/plain")
		require.NoError(t, err)
		assert.Equal(t, "61440", string(out))
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
	assert.ErrorIs(t, err, errFuncBudget)
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
	// The count, not the 20 ms deadline, must stop the long bodies, also
	// under -race; not parallel, so nothing else sees the raised deadline.
	renderDeadline = 10 * time.Second
	t.Cleanup(func() { renderDeadline = maxRenderTime })
	data := &TemplateData{Event: "Bundle.Failed", Environment: "prod", Message: "abc"}
	tests := []struct{ body, want, err string }{
		{body: `{{if eq .Environment "prod"}}P{{end}}`, want: "P"},
		{body: `{{if eq .Environment "test" "prod"}}P{{end}}`, want: "P"},
		{body: `{{if ne .Environment "prod"}}N{{else}}S{{end}}`, want: "S"},
		{body: `{{if and (eq .Event "Bundle.Failed") (lt (len .Message) 5)}}Y{{end}}`, want: "Y"},
		{body: `{{or .Bundle "none"}}`, want: "none"},
		{body: `{{and .Message .Environment}}`, want: "prod"},
		{body: `{{not .Bundle}}`, want: "true"},
		{body: `{{gt 3 2}} {{ge 2 2}} {{le 1.5 2}} {{lt "a" "b"}}`, want: "true true true true"},
		{body: `{{eq true true}} {{ne true false}}`, want: "true true"},
		{body: `{{slice .Message 1 2}}{{index .Message 0}}`, want: "b97"},
		{body: `{{lt .Message 3}}`, err: "lt: incompatible types for comparison"},
		{body: `{{slice .Message 2 9}}`, err: "out of range"},
		{body: strings.Repeat(`{{eq "a" "a"}}`, maxFuncCalls+1), err: errTooManyCalls.Error()},
		{body: strings.Repeat(`{{not .Message}}`, maxFuncCalls+1), err: errTooManyCalls.Error()},
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
	assert.Contains(t, err.Error(), "call is not allowed")
}

// TestFuncBudget_Stopped: once the render's time is up, every function call
// and every write fails, so Execute returns at the next action.
func TestFuncBudget_Stopped(t *testing.T) {
	b := &funcBudget{}
	require.NoError(t, b.tick("eq"))
	b.stopped.Store(true)
	assert.ErrorIs(t, b.tick("eq"), errRenderTime)
	_, err := b.guard("print", []interface{}{"x"}, times(1), func() (string, error) { return "x", nil })
	assert.ErrorIs(t, err, errRenderTime)
	buf := &limitedBuffer{max: 10, stopped: &b.stopped}
	_, err = buf.Write([]byte("x"))
	assert.ErrorIs(t, err, errRenderTime)
}

// TestRenderTemplate_WorstCaseTime renders the slowest bodies that fit in
// 16 KiB: the most calls, and the most bytes built. Each finishes in under
// 50 ms and leaves no goroutine behind: Execute runs on the caller's
// goroutine and the timer is stopped.
func TestRenderTemplate_WorstCaseTime(t *testing.T) {
	data := &TemplateData{Message: strings.Repeat("<m>", 60<<10)}
	big := "(print" + strings.Repeat(" .Message", 15) + ")"
	bodies := map[string]string{
		"most calls":        strings.Repeat(`{{eq (len (slice .Message 1)) 2}}`, 16384/34),
		"most building":     strings.Repeat(`{{len (js (html `+big+`))}}`, 16384/165),
		"nested escapes":    strings.Repeat(`{{len (json (js (html (urlquery .Message))))}}`, 16384/50),
		"print of the data": "{{print" + strings.Repeat(" .", 16384/2-8) + "}}",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(body), 16384)
			tmpl, err := parseBodyTemplate(body)
			require.NoError(t, err)
			before := runtime.NumGoroutine()
			start := time.Now()
			_, _ = renderTemplate(tmpl, data, "text/plain")
			elapsed := time.Since(start)
			assert.Less(t, elapsed, 50*time.Millisecond)
			time.Sleep(maxRenderTime + 5*time.Millisecond) // a timer that fired would have run by now
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
// real timer sets the stopped flag, so the next call or write ends the
// render with errRenderTime. That error is retryable: not errTemplate.
func TestRenderTemplate_DeadlineStopsARender(t *testing.T) {
	for name, body := range map[string]string{
		"next call":  `{{slow}}{{print "x"}}`,
		"next write": `{{slow}}after`,
	} {
		t.Run(name, func(t *testing.T) {
			tmpl, err := template.New("body").Option("missingkey=error").Funcs(templateFuncs(nil)).
				Funcs(template.FuncMap{"slow": func() string { time.Sleep(maxRenderTime + 15*time.Millisecond); return "" }}).
				Parse(body)
			require.NoError(t, err)
			start := time.Now()
			_, err = renderTemplate(tmpl, &TemplateData{}, "text/plain")
			require.Error(t, err)
			assert.ErrorIs(t, err, errRenderTime)
			assert.NotErrorIs(t, err, errTemplate, "out of time is retried, not given up on")
			assert.Less(t, time.Since(start), 100*time.Millisecond)
		})
	}
}
