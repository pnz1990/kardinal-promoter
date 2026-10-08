// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

// maxRenderedBody bounds a rendered template body.
const maxRenderedBody = 64 << 10

// maxFormatWidth bounds a printf width or precision, so a template cannot
// make fmt allocate a huge string before the size limit sees it.
const maxFormatWidth = 999

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

// templateFuncs are the functions a body template may call on top of the
// text/template builtins. printf replaces the builtin with one that bounds
// widths.
var templateFuncs = template.FuncMap{
	"json": func(v interface{}) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"lower":    strings.ToLower,
	"upper":    strings.ToUpper,
	"truncate": truncateRunes,
	"printf":   boundedSprintf,
}

// verbRe matches the flags, width and precision of a fmt verb.
var verbRe = regexp.MustCompile(`%[-+# 0]*(?:\[\d+\])?(\*|\d*)(?:\.(?:\[\d+\])?(\*|\d*))?`)

func boundedSprintf(format string, args ...interface{}) (string, error) {
	for _, m := range verbRe.FindAllStringSubmatch(format, -1) {
		for _, n := range m[1:] {
			if n == "*" {
				return "", fmt.Errorf("printf: * widths are not allowed")
			}
			if n != "" {
				if v, err := strconv.Atoi(n); err != nil || v > maxFormatWidth {
					return "", fmt.Errorf("printf: width or precision %s is over %d", n, maxFormatWidth)
				}
			}
		}
	}
	return fmt.Sprintf(format, args...), nil
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
// define, block and template (recursion) are refused. A missing field is an
// error.
func parseBodyTemplate(body string) (*template.Template, error) {
	t, err := template.New("body").Option("missingkey=error").Funcs(templateFuncs).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(t.Templates()) > 1 {
		return nil, errors.New("define and block are not allowed")
	}
	if t.Tree == nil || t.Tree.Root == nil {
		return nil, errors.New("empty template")
	}
	if err := checkNodes(t.Tree.Root); err != nil {
		return nil, err
	}
	return t, nil
}

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
	case *parse.IfNode:
		return checkBranch(&x.BranchNode)
	case *parse.WithNode:
		return checkBranch(&x.BranchNode)
	case *parse.RangeNode:
		return errors.New("range is not allowed")
	case *parse.TemplateNode:
		return errors.New("template is not allowed")
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
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
	buf := &limitedBuffer{max: maxRenderedBody}
	if err := t.Execute(buf, data); err != nil {
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
