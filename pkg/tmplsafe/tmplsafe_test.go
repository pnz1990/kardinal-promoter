// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tmplsafe_test

import (
	"strings"
	"testing"
	texttemplate "text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tmplsafe"
)

var lim = tmplsafe.Limits{MaxOutput: 1 << 10, MaxFuncInput: 256, MaxFuncOutput: 512, MaxRangeDepth: 2}

func funcs() texttemplate.FuncMap {
	return texttemplate.FuncMap{
		"upper":   strings.ToUpper,
		"replace": func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
		"join":    func(sep string, items []string) string { return strings.Join(items, sep) },
		"fails":   func() (string, error) { return "", assert.AnError },
	}
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
		{"nested in a pipeline", `{{ print ($x := .Name) }}`, ""},
		{"define", `{{ define "x" }}a{{ end }}b`, "define and block are not allowed"},
		{"block", `{{ block "x" . }}a{{ end }}`, "define and block are not allowed"},
		{"template", `{{ template "x" }}`, "template is not allowed"},
		{"range over a number", `{{ range 1000000000 }}x{{ end }}`, "range over a number is not allowed"},
		{"deep ranges", `{{ range .Items }}{{ range $.Items }}{{ range $.Items }}{{ end }}{{ end }}{{ end }}`, "ranges nested more than 2 deep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tmplsafe.Parse("t", tt.text, funcs(), lim)
			require.Error(t, err)
			if tt.want != "" {
				assert.Contains(t, err.Error(), tt.want)
			}
		})
	}
}

// TestExecute_Bounds: the string builtins and the caller's functions refuse
// arguments or results past the limits, and the output stops at MaxOutput.
func TestExecute_Bounds(t *testing.T) {
	data := map[string]interface{}{"Name": "web", "Items": []string{"a", "b"}, "Big": strings.Repeat("y", 300)}
	tests := []struct {
		name, text, want, wantErr string
	}{
		{name: "plain", text: `{{ .Name | upper }}-{{ range .Items }}{{ . }}{{ end }}-{{ join "," .Items }}`, want: "WEB-ab-a,b"},
		{name: "two ranges", text: `{{ range .Items }}{{ range $.Items }}x{{ end }}{{ end }}`, want: "xxxx"},
		{name: "print input", text: `{{ print .Big }}`, wantErr: "print: arguments of 300 bytes, more than 256"},
		{name: "printf input", text: `{{ printf "%s" .Big }}`, wantErr: "printf: arguments of 302 bytes"},
		{name: "println input", text: `{{ println .Big }}`, wantErr: "println: arguments"},
		{name: "html input", text: `{{ html .Big }}`, wantErr: "html: arguments"},
		{name: "js input", text: `{{ js .Big }}`, wantErr: "js: arguments"},
		{name: "urlquery input", text: `{{ urlquery .Big }}`, wantErr: "urlquery: arguments"},
		{name: "upper input", text: `{{ upper .Big }}`, wantErr: "upper: arguments"},
		{name: "replace blowup", text: `{{ replace "" "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz" "abcdefghijklmnopqrstuvwxyz" }}`,
			wantErr: "replace: result of"},
		{name: "output cap", text: strings.Repeat(`{{ .Big }}`, 4), wantErr: "template output is too large (more than 1024 bytes)"},
		{name: "function error", text: `{{ fails }}`, wantErr: assert.AnError.Error()},
		{name: "missing field", text: `{{ .Nope }}`, wantErr: "map has no entry for key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := tmplsafe.Parse("t", tt.text, funcs(), lim)
			require.NoError(t, err)
			got, err := tmpl.Execute(data)
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

// TestExecute_Concurrent: one parsed template executes on several
// goroutines at once (go test -race).
func TestExecute_Concurrent(t *testing.T) {
	tmpl, err := tmplsafe.Parse("t", `{{ .Name | upper }}`, funcs(), lim)
	require.NoError(t, err)
	done := make(chan string)
	for i := 0; i < 8; i++ {
		go func() {
			out, _ := tmpl.Execute(map[string]string{"Name": "x"})
			done <- out
		}()
	}
	for i := 0; i < 8; i++ {
		assert.Equal(t, "X", <-done)
	}
}
