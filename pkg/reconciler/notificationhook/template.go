// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
	"text/template"
	"text/template/parse"
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

// funcBudget counts the bytes the functions of one render have produced.
// Nil (parse time only) means unlimited: the functions are not called then.
type funcBudget struct{ used int }

// argLen is the size of a function's input, checked before it builds its
// result. Template data fields are strings; other values (numbers, bools)
// are formatted to measure them, which is cheap for them.
func argLen(args ...interface{}) int {
	n := 0
	for _, a := range args {
		if s, ok := a.(string); ok {
			n += len(s)
		} else {
			n += len(fmt.Sprint(a))
		}
	}
	return n
}

// guard runs a string-producing function. Its input must be at most
// maxRenderedBody bytes, and bound(input size), the most the function can
// produce from that input, is charged to the render's budget before the
// function runs, so no call allocates past the budget.
func (b *funcBudget) guard(name string, args []interface{}, bound func(in int) int, build func() (string, error)) (string, error) {
	in := argLen(args...)
	if in > maxRenderedBody {
		return "", fmt.Errorf("%s: input is %d bytes, over %d", name, in, maxRenderedBody)
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
	return template.FuncMap{
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
		if x.Ident == "printf" {
			return errors.New("printf is not allowed: use print, which joins its operands")
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

// limitedBuffer fails writes past max bytes, which stops template execution.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

var errBodyTooLarge = fmt.Errorf("rendered body is over %d bytes", maxRenderedBody)

func (b *limitedBuffer) Write(p []byte) (int, error) {
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
	rt.Funcs(templateFuncs(&funcBudget{}))
	buf := &limitedBuffer{max: maxRenderedBody}
	if err := rt.Execute(buf, data); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, fmt.Errorf("%w: %w", errTemplate, errBodyTooLarge)
		}
		return nil, fmt.Errorf("%w: render: %w", errTemplate, err)
	}
	if isJSONContentType(contentType) && !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("%w: rendered body is not valid JSON (content type %s); quote values with {{ json .Field }}",
			errTemplate, contentType)
	}
	return buf.Bytes(), nil
}

// isJSONContentType reports whether ct is application/json or a +json type.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}
