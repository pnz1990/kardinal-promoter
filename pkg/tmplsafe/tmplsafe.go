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

// Package tmplsafe runs Go text/templates that users write in CRDs (PR
// titles and bodies, notification bodies) inside the controller, without
// letting a template exhaust the controller's memory or CPU.
//
// text/template is not a sandbox. Variables make a loop that doubles a
// string ({{$a := "xx"}}{{$a = print $a $a}} 24 times is 256 MiB before
// anything is written), {{template}} recurses, and {{range 1000000000}}
// spins. A cap on the output alone stops none of these. Parse therefore
// refuses, before anything runs:
//
//   - variable declarations and assignments ({{$x := ...}}, {{$x = ...}},
//     range $i, $v := ...); $ (the data) stays usable;
//   - {{define}}, {{block}} and {{template}};
//   - range over a number, and ranges nested deeper than Limits.MaxRangeDepth.
//
// Every function, the builtins that build strings (print, printf, println,
// html, js, urlquery) included, has its string arguments checked against
// Limits.MaxFuncInput before it runs and its string result against
// Limits.MaxFuncOutput after; and Execute stops writing at Limits.MaxOutput.
// What is left grows at most linearly with the template's length, which the
// CRD bounds.
package tmplsafe

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	texttemplate "text/template"
	"text/template/parse"
)

// Limits bound one template.
type Limits struct {
	// MaxOutput is the most bytes Execute writes.
	MaxOutput int
	// MaxFuncInput is the most bytes of string (and []string) arguments one
	// function call takes.
	MaxFuncInput int
	// MaxFuncOutput is the longest string one function call returns.
	MaxFuncOutput int
	// MaxRangeDepth is how deep ranges may nest.
	MaxRangeDepth int
}

// DefaultLimits suit a PR body or a notification body.
var DefaultLimits = Limits{MaxOutput: 64 << 10, MaxFuncInput: 64 << 10, MaxFuncOutput: 64 << 10, MaxRangeDepth: 2}

// ErrOutputTooLarge is returned when a template writes more than
// Limits.MaxOutput bytes.
var ErrOutputTooLarge = errors.New("template output is too large")

// Template is a checked template and its limits.
type Template struct {
	t   *texttemplate.Template
	lim Limits
}

// Parse parses text as template name with funcs and checks it (see the
// package doc). Every function of funcs, and the string builtins, are
// wrapped with the limits.
func Parse(name, text string, funcs texttemplate.FuncMap, lim Limits) (*Template, error) {
	all := texttemplate.FuncMap{}
	for k, f := range builtinStringFuncs() {
		all[k] = f
	}
	for k, f := range funcs {
		all[k] = f
	}
	wrapped := texttemplate.FuncMap{}
	for k, f := range all {
		w, err := wrapFunc(k, f, lim)
		if err != nil {
			return nil, err
		}
		wrapped[k] = w
	}
	t, err := texttemplate.New(name).Option("missingkey=error").Funcs(wrapped).Parse(text)
	if err != nil {
		return nil, err
	}
	if n := len(t.Templates()); n > 1 {
		return nil, errors.New("define and block are not allowed")
	}
	if t.Tree != nil && t.Root != nil {
		if err := check(t.Root, 0, lim); err != nil {
			return nil, err
		}
	}
	return &Template{t: t, lim: lim}, nil
}

// Execute runs the template on data and returns what it wrote.
func (t *Template) Execute(data interface{}) (string, error) {
	w := &limitedBuffer{max: t.lim.MaxOutput}
	if err := t.t.Execute(w, data); err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return "", fmt.Errorf("%w (more than %d bytes)", ErrOutputTooLarge, t.lim.MaxOutput)
		}
		return "", err
	}
	return w.String(), nil
}

// limitedBuffer is a bytes.Buffer that refuses to grow past max.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, ErrOutputTooLarge
	}
	return b.Buffer.Write(p)
}

// check walks the parse tree and refuses what the package doc lists.
func check(n parse.Node, rangeDepth int, lim Limits) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := check(c, rangeDepth, lim); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return checkPipe(n.Pipe, rangeDepth, lim)
	case *parse.IfNode:
		return checkBranch(&n.BranchNode, rangeDepth, lim)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode, rangeDepth, lim)
	case *parse.RangeNode:
		if rangeDepth+1 > lim.MaxRangeDepth {
			return fmt.Errorf("ranges nested more than %d deep are not allowed", lim.MaxRangeDepth)
		}
		if p := n.Pipe; p != nil && len(p.Cmds) == 1 && len(p.Cmds[0].Args) == 1 {
			if _, ok := p.Cmds[0].Args[0].(*parse.NumberNode); ok {
				return errors.New("range over a number is not allowed")
			}
		}
		if err := checkPipe(n.Pipe, rangeDepth, lim); err != nil {
			return err
		}
		if err := check(n.List, rangeDepth+1, lim); err != nil {
			return err
		}
		return check(n.ElseList, rangeDepth, lim)
	case *parse.TemplateNode:
		return errors.New("template is not allowed")
	}
	return nil
}

func checkBranch(b *parse.BranchNode, rangeDepth int, lim Limits) error {
	if err := checkPipe(b.Pipe, rangeDepth, lim); err != nil {
		return err
	}
	if err := check(b.List, rangeDepth, lim); err != nil {
		return err
	}
	return check(b.ElseList, rangeDepth, lim)
}

// checkPipe refuses variable declarations and assignments, and checks the
// pipelines nested in the commands' arguments.
func checkPipe(p *parse.PipeNode, rangeDepth int, lim Limits) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 {
		return errors.New("variables are not allowed: use the data ($ or .) directly")
	}
	for _, c := range p.Cmds {
		for _, a := range c.Args {
			if err := checkArg(a, rangeDepth, lim); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkArg checks the pipelines in a command argument: (pipeline) and
// (pipeline).Field.
func checkArg(a parse.Node, rangeDepth int, lim Limits) error {
	switch a := a.(type) {
	case *parse.PipeNode:
		return checkPipe(a, rangeDepth, lim)
	case *parse.ChainNode:
		return checkArg(a.Node, rangeDepth, lim)
	}
	return nil
}

// builtinStringFuncs are the text/template builtins that build strings,
// reimplemented so wrapFunc can bound them: a FuncMap entry takes precedence
// over a builtin of the same name.
func builtinStringFuncs() texttemplate.FuncMap {
	return texttemplate.FuncMap{
		"print":    fmt.Sprint,
		"printf":   fmt.Sprintf,
		"println":  fmt.Sprintln,
		"html":     func(args ...interface{}) string { return texttemplate.HTMLEscapeString(fmt.Sprint(args...)) },
		"js":       func(args ...interface{}) string { return texttemplate.JSEscapeString(fmt.Sprint(args...)) },
		"urlquery": func(args ...interface{}) string { return url.QueryEscape(fmt.Sprint(args...)) },
	}
}

var (
	stringSliceType = reflect.TypeOf([]string(nil))
	errorType       = reflect.TypeOf((*error)(nil)).Elem()
)

// wrapFunc returns f with its string arguments and string result bounded.
func wrapFunc(name string, f interface{}, lim Limits) (interface{}, error) {
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		return nil, fmt.Errorf("function %s is not a function", name)
	}
	t := v.Type()
	hasErr := t.NumOut() == 2 && t.Out(1) == errorType
	fail := func(err error) []reflect.Value {
		out := make([]reflect.Value, t.NumOut())
		for i := range out {
			out[i] = reflect.Zero(t.Out(i))
		}
		if hasErr {
			out[1] = reflect.ValueOf(&err).Elem()
			return out
		}
		// A function without an error result cannot report one; text/template
		// turns a panic in a function into an execution error.
		panic(err)
	}
	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		size := 0
		for _, a := range args {
			size += argSize(a)
		}
		if size > lim.MaxFuncInput {
			return fail(fmt.Errorf("%s: arguments of %d bytes, more than %d", name, size, lim.MaxFuncInput))
		}
		var out []reflect.Value
		if t.IsVariadic() {
			out = v.CallSlice(args)
		} else {
			out = v.Call(args)
		}
		if len(out) > 0 && out[0].Kind() == reflect.String && out[0].Len() > lim.MaxFuncOutput {
			return fail(fmt.Errorf("%s: result of %d bytes, more than %d", name, out[0].Len(), lim.MaxFuncOutput))
		}
		return out
	}).Interface(), nil
}

// argSize is the bytes of a string, []string or variadic []interface{}
// argument; other values count 0 (they come from the data, which the CRDs
// bound).
func argSize(a reflect.Value) int {
	switch {
	case a.Kind() == reflect.String:
		return a.Len()
	case a.Type() == stringSliceType:
		n := 0
		for i := 0; i < a.Len(); i++ {
			n += a.Index(i).Len()
		}
		return n
	case a.Kind() == reflect.Slice && a.Type().Elem().Kind() == reflect.Interface:
		n := 0
		for i := 0; i < a.Len(); i++ {
			e := a.Index(i)
			if !e.IsNil() {
				n += argSize(e.Elem())
			}
		}
		return n
	case a.Kind() == reflect.Interface && !a.IsNil():
		return argSize(a.Elem())
	}
	return 0
}
