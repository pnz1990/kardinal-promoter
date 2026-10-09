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
// anything is written), {{template}} recurses, {{range 1000000000}} or
// {{range len (...)}} spins, printf with a large width or with argument
// indexes (%[1]s repeated) and replace with an empty old string allocate a
// gigabyte in one call. A cap on the output alone stops none of these, nor
// does a check of a function's result after it was built. So:
//
//   - Parse walks every node and refuses variable declarations and
//     assignments ($ and . stay usable), define, block and template, and
//     ranges over anything but a data path (.X, $.X.Y, .), nested deeper
//     than Limits.MaxRangeDepth or more than Limits.MaxRanges in all.
//   - Each range body counts its iterations; a render stops after
//     Limits.MaxIterations.
//   - Every function is a Func: before it runs, its Size computes the most
//     bytes it can build from its arguments, and that size is charged to the
//     render's budget (Limits.MaxBuild; one call at most
//     Limits.MaxFuncOutput). Nothing is checked after a call. printf and
//     call are not available; print, println, html, js and urlquery take
//     scalar arguments only.
//   - Execute stops writing at Limits.MaxOutput, makes at most
//     Limits.MaxFuncCalls calls and returns after Limits.MaxExecTime.
//
// The caller passes the data, the functions and the limits; the data must
// keep its lists short (the iteration budget is the backstop).
package tmplsafe

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	texttemplate "text/template"
	"text/template/parse"
	"time"
	"unicode/utf8"
)

// Limits bound one template and each of its renders.
type Limits struct {
	// MaxOutput is the most bytes a render writes.
	MaxOutput int
	// MaxFuncOutput is the most bytes one function call may build, and
	// MaxBuild the most all calls of one render may build together.
	MaxFuncOutput int
	MaxBuild      int
	// MaxRangeDepth is how deep ranges may nest, MaxRanges how many ranges
	// a template may have, and MaxIterations how many range iterations one
	// render may run.
	MaxRangeDepth int
	MaxRanges     int
	MaxIterations int
	// MaxFuncCalls is the most function calls one render makes.
	MaxFuncCalls int
	// MaxExecTime bounds one render.
	MaxExecTime time.Duration
}

// DefaultLimits suit a PR body or a notification body.
var DefaultLimits = Limits{
	MaxOutput: 64 << 10, MaxFuncOutput: 64 << 10, MaxBuild: 1 << 20,
	MaxRangeDepth: 2, MaxRanges: 8, MaxIterations: 1000,
	MaxFuncCalls: 2000, MaxExecTime: time.Second,
}

// Func is a template function and the bound on what it builds.
type Func struct {
	// Fn is the function, as in a text/template FuncMap.
	Fn interface{}
	// Size returns the most bytes a call with args can build. It runs
	// before the call and must not build the result. Nil means the result
	// is small (a bool, a number), counted as 8 bytes.
	Size func(args []interface{}) (int, error)
}

// FuncMap names template functions.
type FuncMap map[string]Func

// ErrOutputTooLarge is returned when a render writes more than
// Limits.MaxOutput bytes.
var ErrOutputTooLarge = errors.New("template output is too large")

// tickFunc is the function Parse puts at the start of every range body to
// count iterations. A template that calls it itself is refused.
const tickFunc = "_tmplsafeTick"

// Template is a checked template and its limits.
type Template struct {
	t     *texttemplate.Template
	lim   Limits
	funcs FuncMap
}

// Parse parses text as template name with funcs and checks it (see the
// package doc).
func Parse(name, text string, funcs FuncMap, lim Limits) (*Template, error) {
	all := builtins()
	for k, f := range funcs {
		all[k] = f
	}
	// Placeholders for parsing; Execute binds the functions to the render.
	parseFuncs := texttemplate.FuncMap{tickFunc: func() string { return "" }}
	for k, f := range all {
		if reflect.ValueOf(f.Fn).Kind() != reflect.Func {
			return nil, fmt.Errorf("function %s is not a function", k)
		}
		parseFuncs[k] = f.Fn
	}
	t, err := texttemplate.New(name).Option("missingkey=error").Funcs(parseFuncs).Parse(text)
	if err != nil {
		return nil, err
	}
	if n := len(t.Templates()); n > 1 {
		return nil, errors.New("define and block are not allowed")
	}
	if t.Tree != nil && t.Root != nil {
		c := &checker{lim: lim, tree: t.Tree}
		if err := c.walk(t.Root, 0); err != nil {
			return nil, err
		}
	}
	return &Template{t: t, lim: lim, funcs: all}, nil
}

// render is the budget of one Execute.
type render struct {
	lim        Limits
	built      int
	calls      int
	iterations int
}

func (r *render) charge(name string, size int) error {
	if size > r.lim.MaxFuncOutput {
		return fmt.Errorf("%s: would build %d bytes, more than %d", name, size, r.lim.MaxFuncOutput)
	}
	r.built += size
	if r.built > r.lim.MaxBuild {
		return fmt.Errorf("%s: the template would build more than %d bytes", name, r.lim.MaxBuild)
	}
	return nil
}

// Execute renders the template on data and returns what it wrote. Each
// render has its own budget, so a Template may be rendered concurrently.
func (t *Template) Execute(data interface{}) (string, error) {
	r := &render{lim: t.lim}
	clone, err := t.t.Clone()
	if err != nil {
		return "", fmt.Errorf("clone template: %w", err)
	}
	bound := texttemplate.FuncMap{tickFunc: func() (string, error) {
		r.iterations++
		if r.iterations > t.lim.MaxIterations {
			return "", fmt.Errorf("more than %d range iterations", t.lim.MaxIterations)
		}
		return "", nil
	}}
	for k, f := range t.funcs {
		bound[k] = bind(k, f, r)
	}
	clone.Funcs(bound)

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		w := &limitedBuffer{max: t.lim.MaxOutput}
		err := clone.Execute(w, data)
		done <- result{w.String(), err}
	}()
	timeout := t.lim.MaxExecTime
	if timeout <= 0 {
		timeout = DefaultLimits.MaxExecTime
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			if errors.Is(res.err, ErrOutputTooLarge) {
				return "", fmt.Errorf("%w (more than %d bytes)", ErrOutputTooLarge, t.lim.MaxOutput)
			}
			return "", res.err
		}
		return res.out, nil
	case <-timer.C:
		return "", fmt.Errorf("template took longer than %s", timeout)
	}
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// bind wraps f so that each call counts against r and charges its Size
// before it runs.
func bind(name string, f Func, r *render) interface{} {
	v := reflect.ValueOf(f.Fn)
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
		r.calls++
		if r.calls > r.lim.MaxFuncCalls {
			return fail(fmt.Errorf("%s: more than %d function calls", name, r.lim.MaxFuncCalls))
		}
		size := 8
		if f.Size != nil {
			vals := make([]interface{}, 0, len(args))
			for i, a := range args {
				if t.IsVariadic() && i == len(args)-1 {
					for j := 0; j < a.Len(); j++ {
						vals = append(vals, a.Index(j).Interface())
					}
					continue
				}
				vals = append(vals, a.Interface())
			}
			var err error
			if size, err = f.Size(vals); err != nil {
				return fail(fmt.Errorf("%s: %w", name, err))
			}
		}
		if err := r.charge(name, size); err != nil {
			return fail(err)
		}
		if t.IsVariadic() {
			return v.CallSlice(args)
		}
		return v.Call(args)
	}).Interface()
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

// checker walks every node of the parse tree, refuses what the package doc
// lists, and puts the iteration counter at the start of every range body.
type checker struct {
	lim    Limits
	tree   *parse.Tree
	ranges int
}

var errVariables = errors.New("variables are not allowed: use the data ($ or .) directly")

func (c *checker) walk(n parse.Node, rangeDepth int) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, x := range n.Nodes {
			if err := c.walk(x, rangeDepth); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return c.walk(n.Pipe, rangeDepth)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		if len(n.Decl) > 0 || n.IsAssign {
			return errVariables
		}
		for _, cmd := range n.Cmds {
			if err := c.walk(cmd, rangeDepth); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			if err := c.walk(a, rangeDepth); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		return c.walk(n.Node, rangeDepth)
	case *parse.VariableNode:
		if len(n.Ident) == 0 || n.Ident[0] != "$" {
			return errVariables
		}
	case *parse.IfNode:
		return c.walkBranch(&n.BranchNode, rangeDepth)
	case *parse.WithNode:
		return c.walkBranch(&n.BranchNode, rangeDepth)
	case *parse.RangeNode:
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			return errVariables
		}
		c.ranges++
		if c.ranges > c.lim.MaxRanges {
			return fmt.Errorf("more than %d ranges are not allowed", c.lim.MaxRanges)
		}
		if rangeDepth+1 > c.lim.MaxRangeDepth {
			return fmt.Errorf("ranges nested more than %d deep are not allowed", c.lim.MaxRangeDepth)
		}
		if err := checkRangeTarget(n.Pipe); err != nil {
			return err
		}
		if err := c.walk(n.Pipe, rangeDepth); err != nil {
			return err
		}
		if err := c.walk(n.List, rangeDepth+1); err != nil {
			return err
		}
		c.tick(n)
		return c.walk(n.ElseList, rangeDepth)
	case *parse.TemplateNode:
		return errors.New("template is not allowed")
	case *parse.IdentifierNode:
		switch n.Ident {
		case tickFunc:
			return errors.New("unknown function")
		case "printf", "call":
			// printf's argument indexes repeat an argument without bound,
			// and call runs a function value from the data.
			return fmt.Errorf("%s is not available in this template", n.Ident)
		}
	}
	return nil
}

func (c *checker) walkBranch(b *parse.BranchNode, rangeDepth int) error {
	if err := c.walk(b.Pipe, rangeDepth); err != nil {
		return err
	}
	if err := c.walk(b.List, rangeDepth); err != nil {
		return err
	}
	return c.walk(b.ElseList, rangeDepth)
}

// tick puts {{tickFunc}} at the start of the range's body.
func (c *checker) tick(n *parse.RangeNode) {
	ident := parse.NewIdentifier(tickFunc).SetTree(c.tree).SetPos(n.Pos)
	action := &parse.ActionNode{NodeType: parse.NodeAction, Pos: n.Pos, Line: n.Line, Pipe: &parse.PipeNode{
		NodeType: parse.NodePipe, Pos: n.Pos, Line: n.Line,
		Cmds: []*parse.CommandNode{{NodeType: parse.NodeCommand, Pos: n.Pos, Args: []parse.Node{ident}}},
	}}
	if n.List == nil {
		n.List = &parse.ListNode{NodeType: parse.NodeList, Pos: n.Pos}
	}
	n.List.Nodes = append([]parse.Node{action}, n.List.Nodes...)
}

// checkRangeTarget allows a range over a data path only: ., .Field.Chain,
// $ or $.Field. A number, a function call or a parenthesised pipeline is
// refused, so the data bounds every range.
func checkRangeTarget(p *parse.PipeNode) error {
	if p != nil && len(p.Cmds) == 1 && len(p.Cmds[0].Args) == 1 && len(p.Decl) == 0 {
		switch a := p.Cmds[0].Args[0].(type) {
		case *parse.FieldNode, *parse.DotNode:
			return nil
		case *parse.VariableNode:
			if len(a.Ident) > 0 && a.Ident[0] == "$" {
				return nil
			}
		}
	}
	return errors.New("range takes a data path only (.Field, $.Field or .), not a number, function or pipeline")
}

// scalarSize is the most bytes fmt.Sprint makes of a scalar: a string, a
// bool, a number or nil. Other values (maps, slices, structs) are refused,
// since printing them builds an unbounded string.
func scalarSize(v interface{}) (int, error) {
	switch x := v.(type) {
	case nil:
		return 5, nil
	case string:
		return len(x), nil
	case bool:
		return 5, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr:
		return 20, nil
	case float32, float64, complex64, complex128:
		return 64, nil
	case fmt.Stringer, error:
		return 0, fmt.Errorf("takes strings, numbers and bools only, not %T", v)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.Len(), nil
	case reflect.Bool:
		return 5, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 20, nil
	}
	return 0, fmt.Errorf("takes strings, numbers and bools only, not %T", v)
}

// scalarsSize is the size of fmt.Sprint(args...) with a separator between
// every two arguments.
func scalarsSize(args []interface{}, extra int) (int, error) {
	n := extra
	for _, a := range args {
		s, err := scalarSize(a)
		if err != nil {
			return 0, err
		}
		n += s + 1
	}
	return n, nil
}

// escapeGrowth is how much html, js and urlquery can grow their input at
// most (a byte becomes \uXXXX).
const escapeGrowth = 6

// builtins replace the text/template builtins that build strings, and
// disable printf (argument indexes repeat an argument without bound) and
// call: a FuncMap entry takes precedence over a builtin of the same name.
func builtins() FuncMap {
	escaper := func(esc func(string) string) Func {
		return Func{
			Fn: func(args ...interface{}) string { return esc(fmt.Sprint(args...)) },
			Size: func(args []interface{}) (int, error) {
				n, err := scalarsSize(args, 0)
				return n * escapeGrowth, err
			},
		}
	}
	disabled := func(name string) Func {
		return Func{Fn: func(...interface{}) (string, error) {
			return "", fmt.Errorf("%s is not available in this template", name)
		}}
	}
	return FuncMap{
		"print":    {Fn: fmt.Sprint, Size: func(args []interface{}) (int, error) { return scalarsSize(args, 0) }},
		"println":  {Fn: fmt.Sprintln, Size: func(args []interface{}) (int, error) { return scalarsSize(args, 1) }},
		"html":     escaper(texttemplate.HTMLEscapeString),
		"js":       escaper(texttemplate.JSEscapeString),
		"urlquery": escaper(url.QueryEscape),
		"printf":   disabled("printf"),
		"call":     disabled("call"),
	}
}

// strArg returns args[i] as a string.
func strArg(args []interface{}, i int) string {
	if i < len(args) {
		if s, ok := args[i].(string); ok {
			return s
		}
	}
	return ""
}

// StringFuncs are string helpers for templates whose Size bounds the result
// before it is built. In each the string comes last, so they work in a
// pipeline: {{ .Name | truncate 7 }}.
func StringFuncs() FuncMap {
	sameSize := func(i int) func([]interface{}) (int, error) {
		return func(args []interface{}) (int, error) { return len(strArg(args, i)), nil }
	}
	return FuncMap{
		// Lower or upper casing grows a UTF-8 string at most 1.5 times.
		"lower":      {Fn: strings.ToLower, Size: func(a []interface{}) (int, error) { return len(strArg(a, 0)) * 3 / 2, nil }},
		"upper":      {Fn: strings.ToUpper, Size: func(a []interface{}) (int, error) { return len(strArg(a, 0)) * 3 / 2, nil }},
		"trimSpace":  {Fn: strings.TrimSpace, Size: sameSize(0)},
		"trimPrefix": {Fn: func(prefix, s string) string { return strings.TrimPrefix(s, prefix) }, Size: sameSize(1)},
		"contains":   {Fn: func(substr, s string) bool { return strings.Contains(s, substr) }},
		"hasPrefix":  {Fn: func(prefix, s string) bool { return strings.HasPrefix(s, prefix) }},
		"truncate":   {Fn: func(n int, s string) string { return TruncateRunes(s, n) }, Size: sameSize(1)},
		"default": {
			Fn: func(def, s string) string {
				if s == "" {
					return def
				}
				return s
			},
			Size: func(a []interface{}) (int, error) { return max(len(strArg(a, 0)), len(strArg(a, 1))), nil },
		},
		"replace": {
			Fn: func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
			Size: func(a []interface{}) (int, error) {
				old, repl, s := strArg(a, 0), strArg(a, 1), strArg(a, 2)
				if old == "" {
					return 0, errors.New("the string to replace is empty")
				}
				return len(s) + strings.Count(s, old)*max(len(repl)-len(old), 0), nil
			},
		},
		"join": {
			Fn: func(sep string, items []string) string { return strings.Join(items, sep) },
			Size: func(a []interface{}) (int, error) {
				items, _ := a[1].([]string)
				n := len(strArg(a, 0)) * max(len(items)-1, 0)
				for _, it := range items {
					n += len(it)
				}
				return n, nil
			},
		},
	}
}

// Const returns a Func with no arguments that returns s, sized len(s): for
// text computed before the render, such as a section of a default body.
func Const(s string) Func {
	return Func{Fn: func() string { return s }, Size: func([]interface{}) (int, error) { return len(s), nil }}
}

// TruncateRunes returns the first n characters of s.
func TruncateRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
