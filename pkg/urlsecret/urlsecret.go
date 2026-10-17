// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package urlsecret knows which URL parameters carry credentials. It
// imports nothing of kardinal's, so the SCM redaction (pkg/scm), the Bundle
// API's validation (pkg/graph) and the receipts (pkg/receipt) share one
// list.
package urlsecret

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// params are the URL parameter names (compared lower-cased, after
// percent-decoding) whose values are credentials.
var params = map[string]bool{
	"access_token": true, "token": true, "private_token": true, "password": true,
	"api_key": true, "client_secret": true, "x-amz-signature": true, "x-amz-credential": true,
	"x-amz-security-token": true, "sig": true, "refresh_token": true, "apikey": true, "key": true,
}

// urlParam matches one key=value pair of a query or fragment, after "?",
// "&", ";" or "#".
var urlParam = regexp.MustCompile(`([?&;#])([^=&;#\s"']+)=([^&;#\s"']*)`)

// Params are the credential parameter names, sorted.
func Params() []string {
	out := make([]string, 0, len(params))
	for p := range params {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// IsParam reports whether key, a URL parameter name as written (it may be
// percent-encoded), names a credential.
func IsParam(key string) bool {
	if k, err := url.QueryUnescape(key); err == nil {
		key = k
	}
	return params[strings.ToLower(key)]
}

// Has reports whether s, a URL or a text that holds URLs, has a credential
// parameter in a query or a fragment, whatever its value.
func Has(s string) bool {
	for _, m := range urlParam.FindAllStringSubmatch(s, -1) {
		if IsParam(m[2]) {
			return true
		}
	}
	return false
}

// Redact replaces the value of every credential parameter in s, in a query
// or a fragment, "&"- or ";"-separated, with its key percent-encoded or
// not, by REDACTED.
func Redact(s string) string {
	return urlParam.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlParam.FindStringSubmatch(m)
		if !IsParam(sub[2]) {
			return m
		}
		return sub[1] + sub[2] + "=REDACTED"
	})
}
