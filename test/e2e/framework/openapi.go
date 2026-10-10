// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// OpenAPI is the controller's OpenAPI document (docs/reference/openapi.json),
// for checking live responses against it.
type OpenAPI struct {
	Raw []byte
	doc map[string]interface{}
}

// LoadOpenAPI reads docs/reference/openapi.json from the checkout.
func LoadOpenAPI(t *testing.T) *OpenAPI {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootDir(t), "docs", "reference", "openapi.json"))
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}
	o := &OpenAPI{Raw: raw}
	if err := json.Unmarshal(raw, &o.doc); err != nil {
		t.Fatalf("parse the OpenAPI document: %v", err)
	}
	return o
}

// repoRootDir is the checkout's root: the directory holding go.mod above the
// test's working directory.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// ResponseSchema is the JSON schema of method path's status response; path
// is the template ("/api/v1/ui/bundles/{bundle}/graph").
func (o *OpenAPI) ResponseSchema(t *testing.T, method, path string, status int) map[string]interface{} {
	t.Helper()
	op := asMap(asMap(asMap(o.doc["paths"])[path])[strings.ToLower(method)])
	if op == nil {
		t.Fatalf("the OpenAPI document has no %s %s", method, path)
	}
	resp := asMap(asMap(op["responses"])[strconv.Itoa(status)])
	schema := asMap(asMap(asMap(resp["content"])["application/json"])["schema"])
	if schema == nil {
		t.Fatalf("the OpenAPI document has no JSON %d response for %s %s", status, method, path)
	}
	return schema
}

func (o *OpenAPI) resolve(ref string) map[string]interface{} {
	node := interface{}(o.doc)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		node = asMap(node)[part]
	}
	return asMap(node)
}

// Validate checks the JSON body against schema and returns every mismatch,
// each prefixed with its JSON path. It supports what the document uses:
// $ref, allOf, type (one or a list), properties, required,
// additionalProperties (false or a schema), items and format date-time.
func (o *OpenAPI) Validate(schema map[string]interface{}, body []byte) []string {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return []string{"$: not JSON: " + err.Error()}
	}
	var errs []string
	o.validate("$", schema, v, &errs)
	return errs
}

func (o *OpenAPI) validate(at string, s map[string]interface{}, v interface{}, errs *[]string) {
	if ref, ok := s["$ref"].(string); ok {
		o.validate(at, o.resolve(ref), v, errs)
		return
	}
	for _, sub := range asSlice(s["allOf"]) {
		o.validate(at, asMap(sub), v, errs)
	}
	if t, ok := s["type"]; ok {
		types := []string{}
		switch tt := t.(type) {
		case string:
			types = append(types, tt)
		case []interface{}:
			for _, x := range tt {
				types = append(types, fmt.Sprint(x))
			}
		}
		if !typeMatches(types, v) {
			*errs = append(*errs, fmt.Sprintf("%s: %v is not %v", at, describe(v), types))
			return
		}
	}
	if s["format"] == "date-time" {
		if str, ok := v.(string); ok {
			if _, err := time.Parse(time.RFC3339, str); err != nil {
				*errs = append(*errs, fmt.Sprintf("%s: %q is not an RFC 3339 date-time", at, str))
			}
		}
	}
	switch val := v.(type) {
	case map[string]interface{}:
		props := asMap(s["properties"])
		for _, r := range asSlice(s["required"]) {
			if _, ok := val[fmt.Sprint(r)]; !ok {
				*errs = append(*errs, fmt.Sprintf("%s: required property %q missing", at, r))
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k]; ok {
				o.validate(at+"."+k, asMap(ps), val[k], errs)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap && props != nil {
					*errs = append(*errs, fmt.Sprintf("%s: property %q is not in the document", at, k))
				}
			case map[string]interface{}:
				o.validate(at+"."+k, ap, val[k], errs)
			}
		}
	case []interface{}:
		if items := asMap(s["items"]); items != nil {
			for i, x := range val {
				o.validate(fmt.Sprintf("%s[%d]", at, i), items, x, errs)
			}
		}
	}
}

func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

func typeMatches(types []string, v interface{}) bool {
	for _, t := range types {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				return true
			}
		case "object":
			if _, ok := v.(map[string]interface{}); ok {
				return true
			}
		case "array":
			if _, ok := v.([]interface{}); ok {
				return true
			}
		}
	}
	return false
}

func describe(v interface{}) string {
	b, _ := json.Marshal(v)
	if len(b) > 80 {
		return string(b[:77]) + "..."
	}
	return string(b)
}
