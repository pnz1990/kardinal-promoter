// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package urlsecret knows which URL parameters carry credentials, and
// redacts them. It imports nothing of kardinal's, so the SCM redaction
// (pkg/scm), the Bundle API's validation (pkg/graph), the receipts
// (pkg/receipt) and the evidence pack share one rule. The Bundle CRD's
// rule (export_test.go's CRDRules builds it) and the UI (web/src/prLink.ts)
// repeat it; tests pin both copies to this one, and testdata/vectors.json
// is the table every layer must agree on.
package urlsecret

import (
	"net/url"
	"regexp"
	"strings"
)

// Suffixes, Exact and Harmless decide whether a parameter names a
// credential, after its name is percent-decoded, lower-cased, stripped of a
// trailing array index ("token[]", "api_key[0]") and of "-" and "_"
// (Access-Token, access_token and accesstoken are one name): it is an
// exact name, or it ends in a suffix and is not a harmless name that
// happens to (monkey, hotkey, sortkey, ...). A name that only contains a
// suffix (tokens, keyboard) does not count: the suffix must end the name.
var (
	Suffixes = []string{
		"token", "secret", "signature", "password", "passwd", "passphrase", "credential", "credentials",
		"key", "accesskeyid", "session", "sessionid", "assertion", "ticket", "bearer",
	}
	Exact    = []string{"sig", "auth", "authorization", "jwt", "code", "pass", "pwd", "sid"}
	Harmless = []string{
		"monkey", "donkey", "turkey", "jockey", "hockey", "whiskey", "hotkey", "sortkey", "primarykey",
		"foreignkey", "partitionkey", "groupkey", "cachekey", "lookupkey",
		// CI identifiers (Bamboo plan, build, job, result, chain and stage
		// keys; SonarQube project keys; Jira issue keys): a CI run URL from
		// a normal CI provider is never refused.
		"buildkey", "plankey", "jobkey", "resultkey", "buildresultkey", "chainkey", "stagekey", "projectkey",
		"issuekey", "branchkey",
	}
)

// arrayIndex is a trailing array index of a parameter name.
var arrayIndex = regexp.MustCompile(`(\[[^\]]*\])+$`)

// urlParam matches one key=value pair of a query or fragment, after "?",
// "&", ";" or "#", in a text: the value stops at a quote, so a URL quoted
// in a message keeps its closing quote.
var urlParam = regexp.MustCompile(`([?&;#])([^=&;#\s"']+)=([^&;#\s"']*)`)

// stringParam is urlParam for a single string (a field's value, not a
// text that quotes URLs): the value runs to the next "&", ";", "#" or
// whitespace, a quote included, so no part of a credential is left.
var stringParam = regexp.MustCompile(`([?&;#])([^=&;#\s]+)=([^&;#\s]*)`)

// knownToken matches credentials recognisable by their shape anywhere in a
// string, not only in a URL parameter: GitHub, GitLab and Slack tokens and
// AWS access key IDs.
var knownToken = regexp.MustCompile(`\b(?:(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[0-9A-Z]{16})\b`)

// schemeAuthority matches a URL's scheme and authority: everything after
// "://" up to the first "/" (a raw "#", "?" or space in a password is
// still the authority).
var schemeAuthority = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s]*`)

// normalize is a parameter name as the rule compares it.
func normalize(name string) string {
	if n, err := url.QueryUnescape(name); err == nil {
		name = n
	}
	name = arrayIndex.ReplaceAllString(name, "")
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
}

// IsParam reports whether name, a URL parameter name as written (it may be
// percent-encoded), names a credential.
func IsParam(name string) bool {
	n := normalize(name)
	for _, e := range Exact {
		if n == e {
			return true
		}
	}
	for _, h := range Harmless {
		if n == h {
			return false
		}
	}
	for _, s := range Suffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// Has reports whether s, a URL or a text that holds URLs, has a credential
// parameter in a query or a fragment, whatever its value.
func Has(s string) bool {
	for _, m := range stringParam.FindAllStringSubmatch(s, -1) {
		if IsParam(m[2]) {
			return true
		}
	}
	return false
}

// Redact replaces the value of every credential parameter in s, a text
// that may quote URLs (a message, a response body), by REDACTED.
func Redact(s string) string { return redactWith(urlParam, s) }

func redactWith(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		sub := re.FindStringSubmatch(m)
		if !IsParam(sub[2]) {
			return m
		}
		return sub[1] + sub[2] + "=REDACTED"
	})
}

// RedactString is s, one string such as a field's value (decoded, not a
// text quoting URLs), without credentials: the user info of each URL in it
// (up to the last "@" of its authority, so a password with a raw "#", "?"
// or space goes too), the value of each credential parameter (to the next
// "&", ";", "#" or whitespace), and tokens known by their shape (ghp_...,
// glpat-..., AKIA...). It is what a signed record (a promotion receipt)
// must hold; a token in free text of another shape is not recognised.
func RedactString(s string) string {
	s = schemeAuthority.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.Index(m, "://") + 3
		if at := strings.LastIndex(m[i:], "@"); at >= 0 {
			return m[:i] + m[i+at+1:]
		}
		return m
	})
	s = redactWith(stringParam, s)
	return knownToken.ReplaceAllString(s, "REDACTED")
}

// HasUserInfo reports whether s, a URL, has user info: an "@" in its
// authority (before the first "/" after "://").
func HasUserInfo(s string) bool {
	m := schemeAuthority.FindString(s)
	return m != "" && strings.Contains(m[strings.Index(m, "://")+3:], "@")
}
