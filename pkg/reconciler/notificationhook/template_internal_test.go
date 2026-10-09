// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"runtime"
	"strings"
	"testing"

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
	big := strings.Repeat("m", 60<<10)
	data := &TemplateData{Event: "Bundle.Failed", Message: big}
	tests := map[string]struct{ body, want string }{
		"print over input limit":             {`{{len (print .Message .Message)}}`, "print: input is 122880 bytes, over 65536"},
		"println over input limit":           {`{{len (println .Message .Message)}}`, "println: input is"},
		"html over input limit":              {`{{len (html .Message .Message)}}`, "html: input is"},
		"js over input limit":                {`{{len (js .Message .Message)}}`, "js: input is"},
		"urlquery over input limit":          {`{{len (urlquery .Message .Message)}}`, "urlquery: input is"},
		"json charges 6x its input up front": {`{{len (json (print .Message "x"))}}`, "json: function output is over 262144 bytes in total"},

		"budget across calls": {strings.Repeat(`{{len (upper .Message)}}`, 5),
			"upper: function output is over 262144 bytes in total"},
		"budget across nested calls": {strings.Repeat(`{{len (lower (print .Message))}}`, 3),
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
	tmpl, err := parseBodyTemplate(`{{len (upper .Message)}}`)
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
	body := strings.Repeat(`{{len (print (upper (lower .Message)))}}`, 400)
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
}
