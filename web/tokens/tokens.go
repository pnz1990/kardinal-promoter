// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package tokens reads the design tokens (tokens.json, the source of
// web/src/styles/tokens.css and the docs' tokens.css) for Go code, so the
// CLI's colours are the UI's and cannot drift from them.
package tokens

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed tokens.json
var raw []byte

// Pair is a colour for a light and a dark background, as #RRGGBB.
type Pair struct{ Light, Dark string }

// tree is tokens.json parsed.
var tree = func() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(fmt.Sprintf("web/tokens/tokens.json: %v", err))
	}
	return m
}()

// Color is the colour token at path ("color.status.failed.fg"), with
// references ({color.thread.text}) resolved. ok is false when the token is
// missing or not a light/dark colour.
func Color(path string) (Pair, bool) { return color(path, 0) }

func color(path string, depth int) (Pair, bool) {
	if depth > 8 {
		return Pair{}, false
	}
	v, ok := value(path)
	if !ok {
		return Pair{}, false
	}
	switch x := v.(type) {
	case string:
		if ref, ok := reference(x); ok {
			return color(ref, depth+1)
		}
	case map[string]any:
		light, lok := side(x["light"], depth, false)
		dark, dok := side(x["dark"], depth, true)
		if lok && dok {
			return Pair{Light: light, Dark: dark}, true
		}
	}
	return Pair{}, false
}

// side is one side (dark, else light) of a colour: a #RRGGBB, or a
// reference to a token whose same side it takes.
func side(v any, depth int, dark bool) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	if ref, ok := reference(s); ok {
		p, ok := color(ref, depth+1)
		if dark {
			return p.Dark, ok
		}
		return p.Light, ok
	}
	return s, strings.HasPrefix(s, "#") && len(s) == 7
}

// reference is the path of "{a.b.c}".
func reference(s string) (string, bool) {
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"), true
	}
	return "", false
}

// value is the $value of the token at path; a group's DEFAULT token stands
// for the group.
func value(path string) (any, bool) {
	var cur any = tree
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, false
	}
	if v, ok := m["$value"]; ok {
		return v, true
	}
	if d, ok := m["DEFAULT"].(map[string]any); ok {
		v, ok := d["$value"]
		return v, ok
	}
	return nil, false
}
