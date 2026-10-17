// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/urlsecret"
)

// redactRecord is rec with redact applied to every string in it (the user
// info of a URL, the value of a credential parameter, a token known by its
// shape), and the JSON paths of the strings it changed. A
// receipt is signed, and its Rekor sink publishes it for good: a token a
// Bundle carries (in provenance.ciRunURL, which the Bundle API refuses but
// a Bundle created with kubectl may hold) must never reach it, and a
// signed record cannot be redacted afterwards. rec is copied first: its
// pointers are the Bundle's and the step's, from the cache.
func redactRecord(rec Record) (Record, []string, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return rec, nil, fmt.Errorf("copy the record: %w", err)
	}
	var out Record
	if err := json.Unmarshal(raw, &out); err != nil {
		return rec, nil, fmt.Errorf("copy the record: %w", err)
	}
	var paths []string
	redactValue(reflect.ValueOf(&out).Elem(), "kardinalPromotion", &paths)
	sort.Strings(paths)
	return out, paths, nil
}

// redact is s without credentials: scm.RedactText (a text's URLs) and
// urlsecret.RedactString (one string's), so what it returns is also
// unchanged by scm.RedactText.
func redact(s string) string { return urlsecret.RedactString(scm.RedactText(s)) }

// redactValue applies redact to every settable string under v, recording
// the path of each it changed.
func redactValue(v reflect.Value, path string, paths *[]string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			redactValue(v.Elem(), path, paths)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			p := path
			if name != "" {
				p += "." + name
			}
			redactValue(v.Field(i), p, paths)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			redactValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i), paths)
		}
	case reflect.Map:
		if v.Type().Elem().Kind() != reflect.String {
			for _, k := range v.MapKeys() {
				e := reflect.New(v.Type().Elem()).Elem()
				e.Set(v.MapIndex(k))
				redactValue(e, fmt.Sprintf("%s[%v]", path, k), paths)
				v.SetMapIndex(k, e)
			}
			return
		}
		for _, k := range v.MapKeys() {
			if s := v.MapIndex(k).String(); redact(s) != s {
				v.SetMapIndex(k, reflect.ValueOf(redact(s)).Convert(v.Type().Elem()))
				*paths = append(*paths, fmt.Sprintf("%s[%v]", path, k))
			}
		}
	case reflect.String:
		if s := v.String(); v.CanSet() && redact(s) != s {
			v.SetString(redact(s))
			*paths = append(*paths, path)
		}
	}
}
