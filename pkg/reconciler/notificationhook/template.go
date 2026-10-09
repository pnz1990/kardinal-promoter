// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"reflect"
	"strings"
	"sync/atomic"
	"text/template"
	"text/template/parse"
	"time"
)

// maxRenderedBody bounds a rendered template body.
const maxRenderedBody = 64 << 10

// TemplateData is what a format: template body is rendered over
// (docs/notifications.md#templated-body). Every field is a string; empty
// when the event does not carry it.
type TemplateData struct {
	// Event is the event type, e.g. "Bundle.Verified".
	Event string
	// Key identifies the event; the same event always has the same key, so a
	// receiver can drop duplicates on it. Also sent as X-Kardinal-Event-Key.
	Key string
	// Pipeline, Bundle and Environment name where the event happened.
	Pipeline    string
	Bundle      string
	Environment string
	// Message is the human-readable description of the json format.
	Message string
	// Timestamp is the RFC3339 UTC delivery time.
	Timestamp string
	// PRURL is the pull request of PromotionStep.PROpened and
	// PromotionStep.WaitingForApproval.
	PRURL string
	// Hook and Namespace name the NotificationHook.
	Hook      string
	Namespace string
}

// errTemplate marks a body that cannot be rendered for an event. Retrying
// the same event cannot help, so the reconciler gives up on it at once.
var errTemplate = errors.New("template")

// maxFuncOutput bounds the bytes all function calls of one render may
// produce together. Without loops or variables a template cannot grow a
// string geometrically, but nested calls can still each build a string; this
// budget keeps the total allocation of a render to a few hundred KiB.
const maxFuncOutput = 4 * maxRenderedBody

// errFuncBudget is returned when a function call would exceed the budget.
var errFuncBudget = fmt.Errorf("function output is over %d bytes in total", maxFuncOutput)

// maxFuncCalls bounds the function calls of one render, builtins included.
// A template is at most 16 KiB and has no loops, so a real one makes a few
// dozen.
const maxFuncCalls = 2000

// maxRenderTime is a backstop on one render's wall-clock time. The call
// budget (maxFuncCalls), the byte budgets and the scalar-only arguments are
// what stop a hostile template; a tight wall-clock limit would instead drop
// real notifications on a CPU-throttled controller. text/template cannot be
// cancelled, so a timer sets the render's stopped flag, which every function
// call and every write checks; Execute runs on the caller's goroutine and
// returns at the next one.
const maxRenderTime = 500 * time.Millisecond

// renderDeadline is maxRenderTime; the deadline tests shorten it.
var renderDeadline = maxRenderTime

// maxDataField is the most bytes of each TemplateData field a template sees;
// longer values (a long Message) are cut.
const maxDataField = 4 << 10

var (
	errTooManyCalls = fmt.Errorf("more than %d function calls", maxFuncCalls)
	errRenderTime   = fmt.Errorf("render took longer than %s", maxRenderTime)
)

// funcBudget counts the bytes the functions of one render have produced and
// the calls they made. Nil (parse time only) means unlimited: the functions
// are not called then.
type funcBudget struct {
	used    int
	calls   int
	stopped atomic.Bool
}

// tick counts one function call and refuses it past maxFuncCalls or after
// the render's time ran out.
func (b *funcBudget) tick(name string) error {
	if b == nil {
		return nil
	}
	if b.stopped.Load() {
		return fmt.Errorf("%s: %w", name, errRenderTime)
	}
	b.calls++
	if b.calls > maxFuncCalls {
		return fmt.Errorf("%s: %w", name, errTooManyCalls)
	}
	return nil
}

// argLen is the size of a function's input, checked before it builds its
// result. Arguments may only be strings, numbers or bools: anything else (a
// struct such as the template data, a pointer, map or slice) is refused
// before it is formatted, since formatting it is what costs. It stops as soon
// as the running total passes maxRenderedBody.
func argLen(name string, args ...interface{}) (int, error) {
	n := 0
	for _, a := range args {
		size, err := scalarLen(a)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		n += size
		if n > maxRenderedBody {
			return n, fmt.Errorf("%s: input is over %d bytes", name, maxRenderedBody)
		}
	}
	return n, nil
}

// scalarLen is the most bytes fmt.Sprint makes of a string, number or bool.
func scalarLen(a interface{}) (int, error) {
	switch x := a.(type) {
	case nil:
		return 5, nil
	case string:
		return len(x), nil
	case bool:
		return 5, nil
	}
	v := reflect.ValueOf(a)
	switch v.Kind() {
	case reflect.String:
		return v.Len(), nil
	case reflect.Bool:
		return 5, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return 20, nil
	case reflect.Float32, reflect.Float64:
		return 32, nil
	}
	return 0, fmt.Errorf("takes strings, numbers and bools only, not %T", a)
}

// guard runs a string-producing function. Its input must be at most
// maxRenderedBody bytes, and bound(input size), the most the function can
// produce from that input, is charged to the render's budget before the
// function runs, so no call allocates past the budget.
func (b *funcBudget) guard(name string, args []interface{}, bound func(in int) int, build func() (string, error)) (string, error) {
	if err := b.tick(name); err != nil {
		return "", err
	}
	in, err := argLen(name, args...)
	if err != nil {
		return "", err
	}
	if b != nil {
		b.used += bound(in)
		if b.used > maxFuncOutput {
			return "", fmt.Errorf("%s: %w", name, errFuncBudget)
		}
	}
	return build()
}

// Output bounds from the input size: escaping turns a byte into at most 6
// (\u003c, &#34;), case mapping a byte into at most 3 (an invalid byte
// becomes U+FFFD), and print adds at most one space per operand.
func times(k int) func(int) int { return func(in int) int { return k*in + 2 } }

// templateFuncs are the functions a body template may call: json, lower,
// upper, truncate, and the string-producing text/template builtins print,
// println, html, js and urlquery, each replaced by a version that bounds its
// input and charges its largest possible output to b before it runs.
// printf is refused when the template is parsed: argument indexes
// (%[1]s) repeat an operand without bound.
func templateFuncs(b *funcBudget) template.FuncMap {
	m := template.FuncMap{
		"json": func(v interface{}) (string, error) {
			return b.guard("json", []interface{}{v}, times(6), func() (string, error) {
				out, err := json.Marshal(v)
				return string(out), err
			})
		},
		"lower": func(s string) (string, error) {
			return b.guard("lower", []interface{}{s}, times(3), func() (string, error) { return strings.ToLower(s), nil })
		},
		"upper": func(s string) (string, error) {
			return b.guard("upper", []interface{}{s}, times(3), func() (string, error) { return strings.ToUpper(s), nil })
		},
		"truncate": func(n int, s string) (string, error) {
			return b.guard("truncate", []interface{}{s}, times(1), func() (string, error) { return truncateRunes(n, s), nil })
		},
		"print": func(args ...interface{}) (string, error) {
			return b.guard("print", args, func(in int) int { return in + len(args) }, func() (string, error) { return fmt.Sprint(args...), nil })
		},
		"println": func(args ...interface{}) (string, error) {
			return b.guard("println", args, func(in int) int { return in + len(args) + 1 }, func() (string, error) { return fmt.Sprintln(args...), nil })
		},
		"html": func(args ...interface{}) (string, error) {
			return b.guard("html", args, func(in int) int { return 6*in + 6*len(args) }, func() (string, error) { return template.HTMLEscaper(args...), nil })
		},
		"js": func(args ...interface{}) (string, error) {
			return b.guard("js", args, func(in int) int { return 6*in + 6*len(args) }, func() (string, error) { return template.JSEscaper(args...), nil })
		},
		"urlquery": func(args ...interface{}) (string, error) {
			return b.guard("urlquery", args, func(in int) int { return 3*in + 3*len(args) }, func() (string, error) { return template.URLQueryEscaper(args...), nil })
		},
	}
	// The other builtins build nothing large, but each call is counted and
	// stops with the render: a FuncMap entry takes precedence over the
	// builtin of the same name. and and or then evaluate every operand (no
	// short-circuit), which only costs calls the budget counts.
	for name, fn := range countedBuiltins(b) {
		m[name] = fn
	}
	return m
}

// countedBuiltins replace and, or, not, eq, ne, lt, le, gt, ge, len, index
// and slice with versions that count against b. They accept what the
// template data and literals are: strings, numbers and bools.
func countedBuiltins(b *funcBudget) template.FuncMap {
	cmp := func(name string, ok func(c int) bool) func(a, c interface{}) (bool, error) {
		return func(a, c interface{}) (bool, error) {
			if err := b.tick(name); err != nil {
				return false, err
			}
			n, err := compareBasic(a, c)
			if err != nil {
				return false, fmt.Errorf("%s: %w", name, err)
			}
			return ok(n), nil
		}
	}
	return template.FuncMap{
		"and": func(first interface{}, rest ...interface{}) (interface{}, error) {
			if err := b.tick("and"); err != nil {
				return nil, err
			}
			v := first
			for _, r := range rest {
				if !truth(v) {
					return v, nil
				}
				v = r
			}
			return v, nil
		},
		"or": func(first interface{}, rest ...interface{}) (interface{}, error) {
			if err := b.tick("or"); err != nil {
				return nil, err
			}
			v := first
			for _, r := range rest {
				if truth(v) {
					return v, nil
				}
				v = r
			}
			return v, nil
		},
		"not": func(v interface{}) (bool, error) {
			if err := b.tick("not"); err != nil {
				return false, err
			}
			return !truth(v), nil
		},
		"eq": func(a interface{}, others ...interface{}) (bool, error) {
			if err := b.tick("eq"); err != nil {
				return false, err
			}
			if len(others) == 0 {
				return false, errors.New("eq: missing argument for comparison")
			}
			for _, o := range others {
				n, err := compareBasic(a, o)
				if err != nil {
					if errors.Is(err, errNotOrdered) {
						if equalBasic(a, o) {
							return true, nil
						}
						continue
					}
					return false, fmt.Errorf("eq: %w", err)
				}
				if n == 0 {
					return true, nil
				}
			}
			return false, nil
		},
		"ne": func(a, o interface{}) (bool, error) {
			if err := b.tick("ne"); err != nil {
				return false, err
			}
			n, err := compareBasic(a, o)
			if errors.Is(err, errNotOrdered) {
				return !equalBasic(a, o), nil
			}
			if err != nil {
				return false, fmt.Errorf("ne: %w", err)
			}
			return n != 0, nil
		},
		"lt": cmp("lt", func(n int) bool { return n < 0 }),
		"le": cmp("le", func(n int) bool { return n <= 0 }),
		"gt": cmp("gt", func(n int) bool { return n > 0 }),
		"ge": cmp("ge", func(n int) bool { return n >= 0 }),
		"len": func(v interface{}) (int, error) {
			if err := b.tick("len"); err != nil {
				return 0, err
			}
			rv := reflect.ValueOf(v)
			switch rv.Kind() {
			case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
				return rv.Len(), nil
			}
			return 0, fmt.Errorf("len of %T", v)
		},
		"index": func(v interface{}, idx ...int) (interface{}, error) {
			if err := b.tick("index"); err != nil {
				return nil, err
			}
			rv := reflect.ValueOf(v)
			for _, i := range idx {
				switch rv.Kind() {
				case reflect.String, reflect.Slice, reflect.Array:
					if i < 0 || i >= rv.Len() {
						return nil, fmt.Errorf("index %d out of range", i)
					}
					rv = rv.Index(i)
				default:
					return nil, fmt.Errorf("cannot index %s", rv.Kind())
				}
			}
			return rv.Interface(), nil
		},
		"slice": func(v string, idx ...int) (string, error) {
			if err := b.tick("slice"); err != nil {
				return "", err
			}
			lo, hi := 0, len(v)
			switch len(idx) {
			case 0:
			case 1:
				lo = idx[0]
			case 2:
				lo, hi = idx[0], idx[1]
			default:
				return "", errors.New("slice of a string takes at most 2 indexes")
			}
			if lo < 0 || hi > len(v) || lo > hi {
				return "", fmt.Errorf("slice [%d:%d] out of range of %d bytes", lo, hi, len(v))
			}
			return v[lo:hi], nil
		},
	}
}

// truth is text/template's notion of a true value.
func truth(v interface{}) bool {
	t, _ := template.IsTrue(v)
	return t
}

var errNotOrdered = errors.New("values are not ordered")

// compareBasic orders two strings, two numbers, or reports errNotOrdered for
// two bools; other values are an error.
func compareBasic(a, c interface{}) (int, error) {
	av, cv := reflect.ValueOf(a), reflect.ValueOf(c)
	switch {
	case av.Kind() == reflect.String && cv.Kind() == reflect.String:
		return strings.Compare(av.String(), cv.String()), nil
	case isNumber(av) && isNumber(cv):
		x, y := toFloat(av), toFloat(cv)
		switch {
		case x < y:
			return -1, nil
		case x > y:
			return 1, nil
		}
		return 0, nil
	case av.Kind() == reflect.Bool && cv.Kind() == reflect.Bool:
		return 0, errNotOrdered
	}
	return 0, fmt.Errorf("incompatible types for comparison: %T and %T", a, c)
}

func equalBasic(a, c interface{}) bool {
	av, cv := reflect.ValueOf(a), reflect.ValueOf(c)
	return av.Kind() == reflect.Bool && cv.Kind() == reflect.Bool && av.Bool() == cv.Bool()
}

func isNumber(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func toFloat(v reflect.Value) float64 {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	}
	return v.Float()
}

// truncateRunes returns s cut to at most n runes, with "…" when cut.
func truncateRunes(n int, s string) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// parseBodyTemplate parses a format: template body. To keep rendering
// linear in the template size, range (loops, including over integers),
// define, block and template (recursion) are refused, and so are variable
// declarations and assignments, with which a template could double a string
// on every action. A missing field is an error. text/template has no step
// limit or cancellation; with these rules a render runs each action of the
// (at most 16 KiB) template once, and the function budget bounds what the
// actions allocate.
func parseBodyTemplate(body string) (*template.Template, error) {
	t, err := template.New("body").Option("missingkey=error").Funcs(templateFuncs(nil)).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(t.Templates()) > 1 {
		return nil, errors.New("define and block are not allowed")
	}
	if t.Tree == nil || t.Root == nil {
		return nil, errors.New("empty template")
	}
	if err := checkNodes(t.Root); err != nil {
		return nil, err
	}
	return t, nil
}

// checkNodes walks every node of the tree and refuses range, template (and
// so define and block), variable declarations and assignments anywhere,
// including inside parenthesized pipelines and chains
// ({{print ($x := .).Message}}), and calls to printf.
func checkNodes(n parse.Node) error {
	switch x := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if x == nil {
			return nil
		}
		for _, c := range x.Nodes {
			if err := checkNodes(c); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return checkNodes(x.Pipe)
	case *parse.IfNode:
		return checkBranch(&x.BranchNode)
	case *parse.WithNode:
		return checkBranch(&x.BranchNode)
	case *parse.RangeNode:
		return errors.New("range is not allowed")
	case *parse.TemplateNode:
		return errors.New("template is not allowed")
	case *parse.PipeNode:
		if x == nil {
			return nil
		}
		if len(x.Decl) > 0 {
			if x.IsAssign {
				return errors.New("variable assignment is not allowed")
			}
			return errors.New("variable declaration is not allowed")
		}
		for _, c := range x.Cmds {
			if err := checkNodes(c); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range x.Args {
			if err := checkNodes(a); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		return checkNodes(x.Node)
	case *parse.IdentifierNode:
		switch x.Ident {
		case "printf":
			return errors.New("printf is not allowed: use print, which joins its operands")
		case "call":
			return errors.New("call is not allowed")
		}
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
	if err := checkNodes(b.Pipe); err != nil {
		return err
	}
	if err := checkNodes(b.List); err != nil {
		return err
	}
	if b.ElseList != nil {
		return checkNodes(b.ElseList)
	}
	return nil
}

// limitedBuffer fails writes past max bytes, or after the render's time ran
// out, which stops template execution.
type limitedBuffer struct {
	bytes.Buffer
	max     int
	stopped *atomic.Bool
}

var errBodyTooLarge = fmt.Errorf("rendered body is over %d bytes", maxRenderedBody)

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.stopped != nil && b.stopped.Load() {
		return 0, errRenderTime
	}
	if b.Len()+len(p) > b.max {
		return 0, errBodyTooLarge
	}
	return b.Buffer.Write(p)
}

// renderTemplate renders t over data. With a JSON content type the result
// must be valid JSON.
func renderTemplate(t *template.Template, data *TemplateData, contentType string) ([]byte, error) {
	// A clone per render gets functions bound to this render's budget.
	rt, err := t.Clone()
	if err != nil {
		return nil, fmt.Errorf("%w: clone: %w", errTemplate, err)
	}
	budget := &funcBudget{}
	rt.Funcs(templateFuncs(budget))
	buf := &limitedBuffer{max: maxRenderedBody, stopped: &budget.stopped}
	// Execute runs here, not on another goroutine; the timer only sets the
	// flag, and is stopped when Execute returns, so nothing is left running.
	timer := time.AfterFunc(renderDeadline, func() { budget.stopped.Store(true) })
	err = rt.Execute(buf, truncatedData(data))
	timer.Stop()
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, fmt.Errorf("%w: %w", errTemplate, errBodyTooLarge)
		}
		// Out of time is not the body's fault (a busy controller): the event
		// is retried with backoff, not given up on.
		if errors.Is(err, errRenderTime) {
			return nil, fmt.Errorf("render: %w", err)
		}
		return nil, fmt.Errorf("%w: render: %w", errTemplate, err)
	}
	if isJSONContentType(contentType) && !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("%w: rendered body is not valid JSON (content type %s); quote values with {{ json .Field }}",
			errTemplate, contentType)
	}
	return buf.Bytes(), nil
}

// truncatedData returns a copy of data with every field cut to maxDataField
// bytes (whole runes), so no field the template sees is large.
func truncatedData(data *TemplateData) *TemplateData {
	if data == nil {
		return nil
	}
	d := *data
	for _, f := range []*string{&d.Event, &d.Key, &d.Pipeline, &d.Bundle, &d.Environment, &d.Message,
		&d.Timestamp, &d.PRURL, &d.Hook, &d.Namespace} {
		*f = truncateBytes(*f, maxDataField)
	}
	return &d
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// isJSONContentType reports whether ct is application/json or a +json type.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}
