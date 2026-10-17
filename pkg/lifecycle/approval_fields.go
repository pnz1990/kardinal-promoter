// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Structured approval fields (docs/design/23-change-requests.md section 8).
// A gate's approval.fields declares what an approve must carry; the
// Approval's spec.fields carries it. One function decides whether a
// decision's fields satisfy the declaration, called by the tally (an approve
// that does not satisfy it does not count), by kardinal approve and by the
// UI API before they write, so the three cannot disagree.

// MaxApprovalFields is how many fields an Approval carries and a gate
// declares (the CRDs' maxProperties and maxItems).
const MaxApprovalFields = 16

// MaxApprovalFieldValue is the longest value, in bytes (the Approval CRD's
// rule).
const MaxApprovalFieldValue = 1024

// MaxRecordedFieldValue is how much of a value status.approvals and an
// AuditEvent's message keep.
const MaxRecordedFieldValue = 256

// FieldProblem is why one declared field is not satisfied.
type FieldProblem struct {
	// Name is the field.
	Name string
	// Problem says what is wrong, in words ("is required", "must be true").
	Problem string
	// Missing is true when the field is required and not given.
	Missing bool
}

// String is "<name> <problem>".
func (p FieldProblem) String() string { return p.Name + " " + p.Problem }

// ApprovalFieldProblems is what is wrong with fields under policy p's
// declared fields, sorted by field name; nil when they satisfy it (or p
// declares none). Fields p does not declare are ignored: they are not
// policy inputs.
func ApprovalFieldProblems(p *kardinalv1alpha1.GateApprovalPolicy, fields map[string]string) []FieldProblem {
	if p == nil || len(p.Fields) == 0 {
		return nil
	}
	var out []FieldProblem
	for _, f := range p.Fields {
		v, given := fields[f.Name]
		if !given || v == "" {
			if f.Required {
				out = append(out, FieldProblem{Name: f.Name, Problem: "is required" + describe(f), Missing: true})
			}
			continue
		}
		if problem := fieldValueProblem(f, v); problem != "" {
			out = append(out, FieldProblem{Name: f.Name, Problem: problem})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ApprovalFieldsValid reports whether fields satisfy policy p's declared
// fields, and why not: "approval.fields: ticketID is required; ...".
func ApprovalFieldsValid(p *kardinalv1alpha1.GateApprovalPolicy, fields map[string]string) (bool, string) {
	problems := ApprovalFieldProblems(p, fields)
	if len(problems) == 0 {
		return true, ""
	}
	parts := make([]string, len(problems))
	for i, pr := range problems {
		parts[i] = pr.String()
	}
	return false, "approval.fields: " + strings.Join(parts, "; ")
}

// describe is " (<description>)" for a field with one.
func describe(f kardinalv1alpha1.ApprovalField) string {
	if f.Description == "" {
		return ""
	}
	return " (" + f.Description + ")"
}

// fieldType is f's type, string when unset.
func fieldType(f kardinalv1alpha1.ApprovalField) string {
	if f.Type == "" {
		return kardinalv1alpha1.ApprovalFieldString
	}
	return f.Type
}

// fieldValueProblem is what is wrong with value v of declared field f, or "".
func fieldValueProblem(f kardinalv1alpha1.ApprovalField, v string) string {
	if len(v) > MaxApprovalFieldValue {
		return fmt.Sprintf("is longer than %d bytes", MaxApprovalFieldValue)
	}
	if !utf8.ValidString(v) {
		return "is not valid UTF-8"
	}
	switch fieldType(f) {
	case kardinalv1alpha1.ApprovalFieldBool:
		if v != "true" && v != "false" {
			return fmt.Sprintf("must be true or false, not %q", truncate(v, 64))
		}
	case kardinalv1alpha1.ApprovalFieldInt:
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return fmt.Sprintf("must be an integer, not %q", truncate(v, 64))
		}
	case kardinalv1alpha1.ApprovalFieldURL:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Sprintf("must be an http or https URL with a host, not %q", truncate(v, 64))
		}
	case kardinalv1alpha1.ApprovalFieldEnum:
		found := false
		for _, allowed := range f.Values {
			found = found || allowed == v
		}
		if !found {
			return fmt.Sprintf("must be one of %s, not %q", strings.Join(f.Values, ", "), truncate(v, 64))
		}
	case kardinalv1alpha1.ApprovalFieldString:
	default:
		return fmt.Sprintf("has unknown type %q", f.Type)
	}
	if f.Pattern != "" {
		re, err := regexp.Compile(f.Pattern)
		if err != nil {
			return fmt.Sprintf("cannot be satisfied: its pattern does not compile (%v)", err)
		}
		if !re.MatchString(v) {
			return fmt.Sprintf("must match %s, not %q", f.Pattern, truncate(v, 64))
		}
	}
	if f.MustBe != "" && v != f.MustBe {
		return fmt.Sprintf("must be %s", f.MustBe)
	}
	return ""
}

// DeclaredFields is fields cut to the ones p declares, each value cut to
// limit bytes (at a character boundary); nil when none remain.
func DeclaredFields(p *kardinalv1alpha1.GateApprovalPolicy, fields map[string]string, limit int) map[string]string {
	if p == nil || len(p.Fields) == 0 || len(fields) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, f := range p.Fields {
		if v, ok := fields[f.Name]; ok {
			out[f.Name] = truncate(v, limit)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// truncate is s cut to at most n bytes at a character boundary, with "…"
// when cut; n <= 0 keeps it whole.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
