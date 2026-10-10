// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"reflect"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tmplsafe"
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

// maxFuncOutput bounds the bytes all function calls of one render may build
// together (tmplsafe.Limits.MaxBuild).
const maxFuncOutput = 4 * maxRenderedBody

// maxFuncCalls bounds the function calls of one render, builtins included.
// A template is at most 16 KiB and has no loops, so a real one makes a few
// dozen.
const maxFuncCalls = 2000

// maxRenderTime is a backstop on one render's wall-clock time. The call
// budget, the byte budgets and the scalar-only arguments are what stop a
// hostile template; a tight wall-clock limit would instead drop real
// notifications on a CPU-throttled controller.
const maxRenderTime = 500 * time.Millisecond

// renderDeadline is maxRenderTime; the deadline tests shorten it.
var renderDeadline = maxRenderTime

// maxDataField is the most bytes of each TemplateData field a template sees;
// longer values (a long Message) are cut.
const maxDataField = 4 << 10

// hookLimits are the tmplsafe limits of a body template.
func hookLimits() tmplsafe.Limits {
	return tmplsafe.Limits{
		MaxOutput: maxRenderedBody, MaxFuncOutput: maxRenderedBody, MaxBuild: maxFuncOutput,
		MaxFuncCalls: maxFuncCalls, MaxExecTime: renderDeadline,
		RangeHint: "an event is one notification, so the data has no lists to loop over",
	}
}

// templateFuncs are the functions a body template may call besides the
// tmplsafe builtins: tmplsafe.StringFuncs (lower, upper, trimSpace,
// trimPrefix, contains, hasPrefix, default, replace; not join: the data
// has no lists), json, and
// truncate, which ends a cut string with "…".
func templateFuncs() tmplsafe.FuncMap {
	funcs := tmplsafe.StringFuncs()
	// The hook data has no lists, so join has nothing to join.
	delete(funcs, "join")
	funcs["truncate"] = tmplsafe.Func{
		Fn: truncateRunes,
		Size: func(a []interface{}) (int, error) {
			s, _ := a[1].(string)
			return len(s) + len("…"), nil
		},
	}
	// json quotes and escapes a scalar; escaping turns a byte into at most 6
	// (\u003c). Like every function it takes strings, numbers and bools
	// only, refused before anything formats a struct, list or map.
	funcs["json"] = tmplsafe.Func{
		Fn: func(v interface{}) (string, error) {
			out, err := json.Marshal(v)
			return string(out), err
		},
		Size: func(a []interface{}) (int, error) {
			n, err := scalarLen(a[0])
			return 6*n + 2, err
		},
	}
	return funcs
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

// parseBodyTemplate parses a format: template body with tmplsafe: no range,
// define, block, template, variables, printf or call, so a render runs each
// action of the (at most 16 KiB) template once, and every function call is
// counted and its output bounded before it runs (docs/notifications.md,
// Templated body). A missing field is an error.
func parseBodyTemplate(body string) (*tmplsafe.Template, error) {
	return parseTemplate(body, nil)
}

// parseTemplate is parseBodyTemplate with extra functions (tests).
func parseTemplate(body string, extra tmplsafe.FuncMap) (*tmplsafe.Template, error) {
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("empty template")
	}
	funcs := templateFuncs()
	for k, f := range extra {
		funcs[k] = f
	}
	t, err := tmplsafe.Parse("body", body, funcs, hookLimits())
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return t, nil
}

// renderTemplate renders t over data. With a JSON content type the result
// must be valid JSON. A render that ran out of time is not the body's fault
// (a busy controller): its error is not errTemplate, so the event is retried
// with backoff. Every other failure is errTemplate.
func renderTemplate(t *tmplsafe.Template, data *TemplateData, contentType string) ([]byte, error) {
	out, err := t.Execute(truncatedData(data))
	if err != nil {
		if errors.Is(err, tmplsafe.ErrStopped) {
			return nil, fmt.Errorf("render: %w", err)
		}
		return nil, fmt.Errorf("%w: render: %w", errTemplate, err)
	}
	if isJSONContentType(contentType) && !json.Valid([]byte(out)) {
		return nil, fmt.Errorf("%w: rendered body is not valid JSON (content type %s); quote values with {{ json .Field }}",
			errTemplate, contentType)
	}
	return []byte(out), nil
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
