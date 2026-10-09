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
// text/template is not a sandbox, and a running template cannot be
// cancelled. Variables make a loop that doubles a string, {{template}}
// recurses, {{range}} runs as long as its data, printf with a large width or
// argument indexes and replace with an empty old string allocate a gigabyte
// in one call. So the language here is loop-free, and every step of a
// render is bounded before it runs:
//
//   - Parse walks every node and refuses variable declarations and
//     assignments ($ and . stay usable), define, block, template and range.
//     With no loop, a render runs each node of the template at most once:
//     its work is bounded by the template's size, not by the data. Lists a
//     template shows (images, gates, environments) come from functions that
//     build them in Go with a bound, such as the PR evidence sections.
//   - Every function, the text/template builtins included (and, or, not,
//     eq, ne, lt, le, gt, ge, len, index, slice, print, println, html, js,
//     urlquery), is a Func: before it runs, its Size computes the most bytes
//     it can build, or for a comparison the bytes it reads, and that is
//     charged to the render's budget (Limits.MaxBuild; one call at most
//     Limits.MaxFuncOutput). Every call counts against Limits.MaxFuncCalls.
//     printf and call are not available; print, println, html, js and
//     urlquery take scalar arguments only.
//   - Execute runs the template on the calling goroutine, so nothing
//     outlives it. Past Limits.MaxExecTime the render is stopped: every
//     function call and every write after that returns an error at once.
//     Writes stop at Limits.MaxOutput.
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
	// MaxFuncOutput is the most bytes one function call may build or read,
	// and MaxBuild the most all calls of one render may together.
	MaxFuncOutput int
	MaxBuild      int
	// MaxFuncCalls is the most function calls one render makes.
	MaxFuncCalls int
	// MaxExecTime bounds one render.
	MaxExecTime time.Duration
}

// DefaultLimits suit a PR body or a notification body.
var DefaultLimits = Limits{
	MaxOutput: 64 << 10, MaxFuncOutput: 64 << 10, MaxBuild: 1 << 20,
	MaxFuncCalls: 2000, MaxExecTime: time.Second,
}

// Func is a template function and the bound on what it builds.
type Func struct {
	// Fn is the function, as in a text/template FuncMap.
	Fn interface{}
	// Size returns the most bytes a call with args can build (or, for a
	// comparison, reads). It runs before the call and must not build the
	// result. Nil means the call is O(1) and its result small, counted as 8
	// bytes.
	Size func(args []interface{}) (int, error)
}

// FuncMap names template functions.
type FuncMap map[string]Func

// ErrOutputTooLarge is returned when a render writes more than
// Limits.MaxOutput bytes.
var ErrOutputTooLarge = errors.New("template output is too large")

// ErrStopped is returned by every call and write of a render past
// Limits.MaxExecTime.
var ErrStopped = errors.New("template stopped")

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
	// The functions as parsed; Execute binds them to the render.
	parseFuncs := texttemplate.FuncMap{}
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
		if err := walk(t.Root); err != nil {
			return nil, err
		}
	}
	return &Template{t: t, lim: lim, funcs: all}, nil
}

// render is the budget of one Execute.
type render struct {
	lim      Limits
	deadline time.Time
	stopped  bool
	built    int
	calls    int
}

// check stops the render past its deadline; a stopped render refuses
// everything.
func (r *render) check() error {
	if !r.stopped && time.Now().After(r.deadline) {
		r.stopped = true
	}
	if r.stopped {
		return fmt.Errorf("%w: it took longer than %s", ErrStopped, r.lim.MaxExecTime)
	}
	return nil
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

// Execute renders the template on data and returns what it wrote. It runs
// on the calling goroutine and starts none. Each render has its own budget,
// so a Template may be rendered concurrently.
func (t *Template) Execute(data interface{}) (string, error) {
	lim := t.lim
	if lim.MaxExecTime <= 0 {
		lim.MaxExecTime = DefaultLimits.MaxExecTime
	}
	r := &render{lim: lim, deadline: time.Now().Add(lim.MaxExecTime)}
	clone, err := t.t.Clone()
	if err != nil {
		return "", fmt.Errorf("clone template: %w", err)
	}
	bound := texttemplate.FuncMap{}
	for k, f := range t.funcs {
		bound[k] = bind(k, f, r)
	}
	clone.Funcs(bound)
	w := &limitedBuffer{max: lim.MaxOutput, r: r}
	if err := clone.Execute(w, data); err != nil {
		switch {
		case errors.Is(err, ErrOutputTooLarge):
			return "", fmt.Errorf("%w (more than %d bytes)", ErrOutputTooLarge, lim.MaxOutput)
		case r.stopped:
			return "", fmt.Errorf("template took longer than %s", lim.MaxExecTime)
		}
		return "", err
	}
	if r.stopped {
		return "", fmt.Errorf("template took longer than %s", lim.MaxExecTime)
	}
	return w.String(), nil
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// bind wraps f so that each call checks the deadline, counts against r and
// charges its Size before it runs.
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
		if err := r.check(); err != nil {
			return fail(err)
		}
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

// limitedBuffer is a bytes.Buffer that refuses to grow past max, and
// refuses every write once the render is stopped.
type limitedBuffer struct {
	bytes.Buffer
	max int
	r   *render
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if err := b.r.check(); err != nil {
		return 0, err
	}
	if b.Len()+len(p) > b.max {
		return 0, ErrOutputTooLarge
	}
	return b.Buffer.Write(p)
}

var errVariables = errors.New("variables are not allowed: use the data ($ or .) directly")

// walk visits every node of the parse tree and refuses what the package doc
// lists.
func walk(n parse.Node) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, x := range n.Nodes {
			if err := walk(x); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return walk(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		if len(n.Decl) > 0 || n.IsAssign {
			return errVariables
		}
		for _, cmd := range n.Cmds {
			if err := walk(cmd); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			if err := walk(a); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		return walk(n.Node)
	case *parse.VariableNode:
		if len(n.Ident) == 0 || n.Ident[0] != "$" {
			return errVariables
		}
	case *parse.IfNode:
		return walkBranch(&n.BranchNode)
	case *parse.WithNode:
		return walkBranch(&n.BranchNode)
	case *parse.RangeNode:
		return errors.New("range is not allowed: the template language has no loops; " +
			"use the functions that list the data (provenanceTable, gatesTable, upstreamTable, imageList)")
	case *parse.BreakNode, *parse.ContinueNode:
		return errors.New("break and continue are not allowed")
	case *parse.TemplateNode:
		return errors.New("template is not allowed")
	case *parse.IdentifierNode:
		switch n.Ident {
		case "printf", "call":
			// printf's argument indexes repeat an argument without bound,
			// and call runs a function value from the data.
			return fmt.Errorf("%s is not available in this template", n.Ident)
		}
	}
	return nil
}

func walkBranch(b *parse.BranchNode) error {
	if err := walk(b.Pipe); err != nil {
		return err
	}
	if err := walk(b.List); err != nil {
		return err
	}
	return walk(b.ElseList)
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

// readSize is what a comparison reads: the length of each string argument,
// 8 bytes for anything else.
func readSize(args []interface{}) (int, error) {
	n := 0
	for _, a := range args {
		if rv := reflect.ValueOf(a); rv.IsValid() && rv.Kind() == reflect.String {
			n += rv.Len()
			continue
		}
		n += 8
	}
	return n, nil
}

// escapeGrowth is how much html, js and urlquery can grow their input at
// most (a byte becomes \uXXXX).
const escapeGrowth = 6

// builtins replace every text/template builtin with a counted Func (a
// FuncMap entry takes precedence over a builtin of the same name), and
// disable printf (argument indexes repeat an argument without bound) and
// call.
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
	compare := func(op func(c int) bool) Func {
		return Func{Fn: func(a, b interface{}) (bool, error) {
			c, err := compareBasic(a, b)
			if err != nil {
				return false, err
			}
			return op(c), nil
		}, Size: readSize}
	}
	return FuncMap{
		"print":    {Fn: fmt.Sprint, Size: func(args []interface{}) (int, error) { return scalarsSize(args, 0) }},
		"println":  {Fn: fmt.Sprintln, Size: func(args []interface{}) (int, error) { return scalarsSize(args, 1) }},
		"html":     escaper(texttemplate.HTMLEscapeString),
		"js":       escaper(texttemplate.JSEscapeString),
		"urlquery": escaper(url.QueryEscape),
		"printf":   disabled("printf"),
		"call":     disabled("call"),
		"and": {Fn: func(first interface{}, rest ...interface{}) interface{} {
			if !truth(first) {
				return first
			}
			for _, a := range rest {
				if !truth(a) {
					return a
				}
			}
			if len(rest) == 0 {
				return first
			}
			return rest[len(rest)-1]
		}},
		"or": {Fn: func(first interface{}, rest ...interface{}) interface{} {
			if truth(first) {
				return first
			}
			for _, a := range rest {
				if truth(a) {
					return a
				}
			}
			if len(rest) == 0 {
				return first
			}
			return rest[len(rest)-1]
		}},
		"not": {Fn: func(a interface{}) bool { return !truth(a) }},
		"eq": {Fn: func(a interface{}, bs ...interface{}) (bool, error) {
			if len(bs) == 0 {
				return false, errors.New("missing argument for comparison")
			}
			for _, b := range bs {
				c, err := compareBasic(a, b)
				if err != nil {
					return false, err
				}
				if c == 0 {
					return true, nil
				}
			}
			return false, nil
		}, Size: readSize},
		"ne":    compare(func(c int) bool { return c != 0 }),
		"lt":    compare(func(c int) bool { return c < 0 }),
		"le":    compare(func(c int) bool { return c <= 0 }),
		"gt":    compare(func(c int) bool { return c > 0 }),
		"ge":    compare(func(c int) bool { return c >= 0 }),
		"len":   {Fn: lengthOf},
		"index": {Fn: indexOf},
		"slice": {Fn: sliceOf},
	}
}

// truth is text/template's truth: false, 0, nil, an empty string, slice or
// map are false.
func truth(a interface{}) bool {
	v := reflect.ValueOf(a)
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() > 0
	case reflect.Bool:
		return v.Bool()
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() != 0
	case reflect.Chan, reflect.Func, reflect.Pointer, reflect.Interface:
		return !v.IsNil()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() != 0
	case reflect.Float32, reflect.Float64:
		return v.Float() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() != 0
	}
	return true
}

var errIncomparable = errors.New("incompatible types for comparison")

// compareBasic compares two strings, bools, integers or floats (integers of
// any size and sign with each other); -1, 0 or 1.
func compareBasic(a, b interface{}) (int, error) {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if !va.IsValid() || !vb.IsValid() {
		return 0, errors.New("invalid type for comparison")
	}
	cmp := func(less, equal bool) int {
		switch {
		case equal:
			return 0
		case less:
			return -1
		}
		return 1
	}
	switch {
	case va.Kind() == reflect.String && vb.Kind() == reflect.String:
		return strings.Compare(va.String(), vb.String()), nil
	case va.Kind() == reflect.Bool && vb.Kind() == reflect.Bool:
		x, y := va.Bool(), vb.Bool()
		return cmp(!x && y, x == y), nil
	case isSigned(va) && isSigned(vb):
		i, j := va.Int(), vb.Int()
		return cmp(i < j, i == j), nil
	case isNumber(va) && isNumber(vb):
		x, y := toFloat(va), toFloat(vb)
		return cmp(x < y, x == y), nil
	}
	return 0, fmt.Errorf("%w: %T and %T", errIncomparable, a, b)
}

func isSigned(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func isInt(v reflect.Value) bool {
	if isSigned(v) {
		return true
	}
	switch v.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

func isNumber(v reflect.Value) bool {
	return isInt(v) || v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64
}

func toFloat(v reflect.Value) float64 {
	switch {
	case isSigned(v):
		return float64(v.Int())
	case isInt(v):
		return float64(v.Uint())
	}
	return v.Float()
}

// lengthOf is the builtin len.
func lengthOf(a interface{}) (int, error) {
	v := reflect.ValueOf(a)
	switch {
	case !v.IsValid():
		return 0, errors.New("len of nil")
	case v.Kind() == reflect.Pointer && v.IsNil():
		return 0, errors.New("len of nil pointer")
	}
	v = reflect.Indirect(v)
	switch v.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return v.Len(), nil
	}
	return 0, fmt.Errorf("len of type %s", v.Type())
}

func intArg(a interface{}) (int, error) {
	v := reflect.ValueOf(a)
	if !v.IsValid() || !isInt(v) {
		return 0, fmt.Errorf("cannot index with %T", a)
	}
	if isSigned(v) {
		return int(v.Int()), nil
	}
	return int(v.Uint()), nil
}

// indexOf is the builtin index: item[i][j]... over arrays, slices, strings
// (a byte) and maps (a missing key is the zero value).
func indexOf(item interface{}, indexes ...interface{}) (interface{}, error) {
	v := reflect.ValueOf(item)
	if !v.IsValid() {
		return nil, errors.New("index of untyped nil")
	}
	for _, ix := range indexes {
		v = reflect.Indirect(v)
		if v.Kind() == reflect.Interface {
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Array, reflect.Slice, reflect.String:
			i, err := intArg(ix)
			if err != nil {
				return nil, err
			}
			if i < 0 || i >= v.Len() {
				return nil, fmt.Errorf("index out of range: %d", i)
			}
			v = v.Index(i)
		case reflect.Map:
			k := reflect.ValueOf(ix)
			if !k.IsValid() || !k.Type().AssignableTo(v.Type().Key()) {
				return nil, fmt.Errorf("cannot index map with %T", ix)
			}
			if x := v.MapIndex(k); x.IsValid() {
				v = x
			} else {
				v = reflect.Zero(v.Type().Elem())
			}
		default:
			return nil, fmt.Errorf("cannot index item of type %s", v.Type())
		}
	}
	if !v.IsValid() {
		return nil, nil
	}
	return v.Interface(), nil
}

// sliceOf is the builtin slice over strings, slices and arrays: item[i:j],
// with at most two indexes. The result shares the item's memory.
func sliceOf(item interface{}, indexes ...interface{}) (interface{}, error) {
	v := reflect.Indirect(reflect.ValueOf(item))
	if !v.IsValid() {
		return nil, errors.New("slice of untyped nil")
	}
	if len(indexes) > 2 {
		return nil, errors.New("slice takes at most two indexes here")
	}
	switch v.Kind() {
	case reflect.String, reflect.Slice:
	default:
		return nil, fmt.Errorf("cannot slice item of type %s", v.Type())
	}
	lo, hi := 0, v.Len()
	var err error
	if len(indexes) > 0 {
		if lo, err = intArg(indexes[0]); err != nil {
			return nil, err
		}
	}
	if len(indexes) > 1 {
		if hi, err = intArg(indexes[1]); err != nil {
			return nil, err
		}
	}
	if lo < 0 || hi < lo || hi > v.Len() {
		return nil, fmt.Errorf("slice indexes out of range: [%d:%d] of %d", lo, hi, v.Len())
	}
	return v.Slice(lo, hi).Interface(), nil
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
	// Changing the case of a UTF-8 string grows it at most 3 times (a
	// 1-byte rune never maps to more than 3 bytes).
	caseSize := func(a []interface{}) (int, error) { return 3 * len(strArg(a, 0)), nil }
	return FuncMap{
		"lower":      {Fn: strings.ToLower, Size: caseSize},
		"upper":      {Fn: strings.ToUpper, Size: caseSize},
		"trimSpace":  {Fn: strings.TrimSpace, Size: sameSize(0)},
		"trimPrefix": {Fn: func(prefix, s string) string { return strings.TrimPrefix(s, prefix) }, Size: sameSize(1)},
		"contains":   {Fn: func(substr, s string) bool { return strings.Contains(s, substr) }, Size: readSize},
		"hasPrefix":  {Fn: func(prefix, s string) bool { return strings.HasPrefix(s, prefix) }, Size: readSize},
		// truncate reads s (counting its runes) and builds at most n runes
		// of 4 bytes, and never more than s.
		"truncate": {Fn: func(n int, s string) string { return TruncateRunes(s, n) }, Size: func(a []interface{}) (int, error) {
			n, _ := a[0].(int)
			s := strArg(a, 1)
			return len(s) + min(len(s), 4*max(n, 0)), nil
		}},
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
