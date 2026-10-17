// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// lastAppliedAnnotation is kubectl apply's copy of the object as applied:
// the spec again, unredacted.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactedValue replaces a literal environment variable value.
const redactedValue = "<redacted>"

// packRedactions is manifest.json's redactions: the rules every exported
// object went through, and how often each one changed something.
type packRedactions struct {
	Rules []string `json:"rules"`
	// URLCredentials counts strings that held a URL's userinfo or a secret
	// query parameter (scm.RedactText).
	URLCredentials int `json:"urlCredentials"`
	// EnvValues counts literal environment variable values replaced.
	EnvValues int `json:"envValues"`
	// LastApplied counts last-applied-configuration annotations removed.
	LastApplied int `json:"lastAppliedAnnotations"`
	// Commands is how container command and args were exported: "digest"
	// (the default: each replaced by its SHA-256 and length) or "redacted"
	// (--include-commands: the arguments, with credential-named values
	// redacted).
	Commands string `json:"commands"`
	// CommandDigests counts command and args lists replaced by a digest.
	CommandDigests int `json:"commandDigests"`
	// FlagValues counts credential values redacted in command and args
	// (--include-commands).
	FlagValues int `json:"flagValues"`
}

const (
	commandsDigest   = "digest"
	commandsRedacted = "redacted"
)

// commonRules are the rules of both modes, in manifest.json's words.
var commonRules = []string{
	"every string: the userinfo (user name, token, password) of each URL and the value of secret query parameters are removed, as kardinal does for status, CLI output and logs",
	"every container environment variable (env[]), in typed specs and in hook Job specs: a literal value is replaced by " + redactedValue + "; its name and valueFrom are kept",
	"the " + lastAppliedAnnotation + " annotation is removed: it repeats the spec unredacted",
}

// digestRule and redactedRules describe how command and args were exported.
var (
	digestRule = "every command and args list (containers, init containers, probes and lifecycle handlers of hook Job specs) is replaced by " +
		`{"sha256": <hex>, "length": <arguments>}: SHA-256 of each argument followed by a NUL byte (printf '%s\0' ARG... | sha256sum). ` +
		"The digest is unkeyed so that anyone can check it: a short or guessable secret in a command can be found from it by trying candidates"
	redactedRules = []string{
		"command and args are exported (--include-commands); the value of a flag, of a NAME=value assignment (export included) or of an " +
			"HTTP header (Name: value) whose name contains token, password, passwd, passphrase, pwd, secret, apikey, api-key, api_key, key, " +
			"credential or authorization, or has the part auth, bearer, pat or pass, a Bearer token, and the password of -u/--user " +
			"user:password are replaced by " + redactedValue + ", also inside one shell string",
		"best-effort: a secret passed as a positional argument, by a short flag such as -p, or under a name these rules do not recognise is exported as written",
	}
)

// rules are the rules a pack's redactor applied.
func (r *redactor) rules() []string {
	if r.includeCommands {
		return append(append([]string{}, commonRules...), redactedRules...)
	}
	return append(append([]string{}, commonRules...), digestRule)
}

// redactor redacts the objects of a pack and counts what it changed.
// includeCommands exports command and args redacted instead of as digests.
type redactor struct {
	counts          packRedactions
	includeCommands bool
}

var (
	envVarType    = reflect.TypeOf(corev1.EnvVar{})
	containerType = reflect.TypeOf(corev1.Container{})
	rawExtType    = reflect.TypeOf(runtime.RawExtension{})
)

// credentialWords are the words a flag's or variable's name contains when
// its value is a credential; credentialParts must be a whole part of the
// name (split at "-", "_" and "."), so --author and --path are not one.
var (
	credentialWords = []string{"token", "password", "passwd", "passphrase", "pwd", "secret", "apikey", "api-key", "api_key", "key", "credential", "authorization"}
	credentialParts = map[string]bool{"auth": true, "bearer": true, "pat": true, "pass": true}
)

// credentialName reports whether a flag's or variable's name says its value
// is a credential.
func credentialName(name string) bool {
	n := strings.ToLower(strings.TrimLeft(name, "-"))
	for _, w := range credentialWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	for _, part := range strings.FieldsFunc(n, func(c rune) bool { return c == '-' || c == '_' || c == '.' }) {
		if credentialParts[part] {
			return true
		}
	}
	return false
}

var (
	// flagArg is a whole argument that is a flag with no value (--token):
	// the next argument is its value.
	flagArg = regexp.MustCompile(`^-{1,2}[A-Za-z0-9_.-]+$`)
	// flagValue is a flag and its value inside an argument: --token=v, or
	// --token v within a shell string. A value never starts with "-".
	flagValue = regexp.MustCompile(`(^|\s)(-{1,2}[A-Za-z0-9_.-]+)(=|\s+)("[^"]*"|'[^']*'|[^\s"'-][^\s"';&|]*)`)
	// assignment is NAME=value, after export or not, at the start of an
	// argument or after a separator of a shell string.
	assignment = regexp.MustCompile(`(^|[\s;&|(])((?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s"';&|)]*)`)
	// header is an HTTP header in an argument ("-H 'Name: value'"), with an
	// optional scheme before the value.
	header = regexp.MustCompile(`(^|[\s'"])([A-Za-z][A-Za-z0-9-]*)(:[ \t]*)((?i:bearer|basic|token)[ \t]+)?([^\s"';,]+)`)
	// bearer is a Bearer credential outside a header: eight token
	// characters at least.
	bearer = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// userPass is curl's -u/--user user:password; the password goes.
	userPass = regexp.MustCompile(`(^|\s)(-u|--user)(=|\s+)([^\s:"']+):([^\s"']+)`)
	// userPassArg is the user:password argument after a whole -u/--user.
	userPassArg = regexp.MustCompile(`^([^\s:"']+):(.+)$`)
)

// args redacts the credential values in a command or args list
// (--include-commands).
func (r *redactor) args(list []string) {
	for i := range list {
		orig := list[i]
		s := flagValue.ReplaceAllStringFunc(orig, func(m string) string {
			sub := flagValue.FindStringSubmatch(m)
			if !credentialName(sub[2]) {
				return m
			}
			r.counts.FlagValues++
			return sub[1] + sub[2] + sub[3] + redactedValue
		})
		s = header.ReplaceAllStringFunc(s, func(m string) string {
			sub := header.FindStringSubmatch(m)
			if !credentialName(sub[2]) || sub[5] == redactedValue {
				return m
			}
			r.counts.FlagValues++
			return sub[1] + sub[2] + sub[3] + sub[4] + redactedValue
		})
		s = bearer.ReplaceAllStringFunc(s, func(m string) string {
			r.counts.FlagValues++
			return bearer.FindStringSubmatch(m)[1] + " " + redactedValue
		})
		s = userPass.ReplaceAllStringFunc(s, func(m string) string {
			sub := userPass.FindStringSubmatch(m)
			r.counts.FlagValues++
			return sub[1] + sub[2] + sub[3] + sub[4] + ":" + redactedValue
		})
		s = assignment.ReplaceAllStringFunc(s, func(m string) string {
			sub := assignment.FindStringSubmatch(m)
			if !credentialName(sub[3]) || sub[4] == "" || sub[4] == redactedValue {
				return m
			}
			r.counts.FlagValues++
			return sub[1] + sub[2] + sub[3] + "=" + redactedValue
		})
		list[i] = s
		if s != orig || i+1 >= len(list) || strings.HasPrefix(list[i+1], "-") || list[i+1] == redactedValue {
			continue
		}
		switch {
		case orig == "-u" || orig == "--user":
			if sub := userPassArg.FindStringSubmatch(list[i+1]); sub != nil {
				list[i+1] = sub[1] + ":" + redactedValue
				r.counts.FlagValues++
			}
		case flagArg.MatchString(orig) && credentialName(orig):
			list[i+1] = redactedValue
			r.counts.FlagValues++
		}
	}
}

// commandDigest is how a command or args list is exported by default: its
// SHA-256 (each argument followed by a NUL byte) and how many arguments it
// has, so an auditor can check it against a known value without the pack
// holding it.
func commandDigest(list []string) map[string]interface{} {
	h := sha256.New()
	for _, a := range list {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	return map[string]interface{}{"sha256": hex.EncodeToString(h.Sum(nil)), "length": len(list)}
}

// command exports a command or args list of a decoded JSON tree: a digest,
// or the arguments redacted with --include-commands. A list that is not of
// strings is left to tree.
func (r *redactor) command(list []interface{}) (interface{}, bool) {
	strs := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		strs[i] = s
	}
	if !r.includeCommands {
		r.counts.CommandDigests++
		return commandDigest(strs), true
	}
	r.args(strs)
	out := make([]interface{}, len(strs))
	for i := range strs {
		out[i] = strs[i]
	}
	return out, true
}

// object redacts o in place: o must be a copy the caller owns.
func (r *redactor) object(o sigs_client.Object) {
	if a := o.GetAnnotations(); a != nil {
		if _, ok := a[lastAppliedAnnotation]; ok {
			delete(a, lastAppliedAnnotation)
			o.SetAnnotations(a)
			r.counts.LastApplied++
		}
	}
	r.walk(reflect.ValueOf(o))
}

// value redacts v, a record built for the pack (a struct or a map it
// owns), in place, and returns it.
func (r *redactor) value(v interface{}) interface{} {
	rv := reflect.ValueOf(&v).Elem()
	r.walk(rv)
	return v
}

// walk redacts every string reachable from v that can be set.
func (r *redactor) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if !v.CanSet() {
			return
		}
		if s := v.String(); s != "" {
			if n := scm.RedactText(s); n != s {
				v.SetString(n)
				r.counts.URLCredentials++
			}
		}
	case reflect.Ptr:
		if !v.IsNil() {
			r.walk(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		elem := v.Elem()
		cp := reflect.New(elem.Type()).Elem()
		cp.Set(elem)
		r.walk(cp)
		if v.CanSet() {
			v.Set(cp)
		}
	case reflect.Struct:
		switch v.Type() {
		case envVarType:
			if f := v.FieldByName("Value"); f.String() != "" && f.CanSet() {
				f.SetString(redactedValue)
				r.counts.EnvValues++
			}
		case rawExtType:
			r.raw(v.FieldByName("Raw"))
			return
		case containerType:
			// No kardinal kind holds a typed container today; were one to,
			// a typed []string cannot hold a digest, so the default drops
			// command and args.
			for _, f := range []string{"Command", "Args"} {
				field := v.FieldByName(f)
				list, _ := field.Interface().([]string)
				if len(list) == 0 || !field.CanSet() {
					continue
				}
				if r.includeCommands {
					r.args(list)
				} else {
					field.Set(reflect.Zero(field.Type()))
					r.counts.CommandDigests++
				}
			}
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				r.walk(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return // bytes
		}
		for i := 0; i < v.Len(); i++ {
			r.walk(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			val := v.MapIndex(k)
			cp := reflect.New(val.Type()).Elem()
			cp.Set(val)
			r.walk(cp)
			v.SetMapIndex(k, cp)
		}
	}
}

// raw redacts the JSON of a RawExtension (a hook's Job spec).
func (r *redactor) raw(f reflect.Value) {
	if f.Len() == 0 || !f.CanSet() {
		return
	}
	var tree interface{}
	if err := json.Unmarshal(f.Bytes(), &tree); err != nil {
		// Not JSON: redact it as text.
		f.SetBytes([]byte(r.text(string(f.Bytes()))))
		return
	}
	tree = r.tree(tree, false)
	b, err := json.Marshal(tree)
	if err == nil {
		f.SetBytes(b)
	}
}

// tree redacts decoded JSON: every string, and the literal value of each
// entry of an env list.
func (r *redactor) tree(x interface{}, env bool) interface{} {
	switch t := x.(type) {
	case map[string]interface{}:
		if env {
			if s, ok := t["value"].(string); ok && s != "" {
				t["value"] = redactedValue
				r.counts.EnvValues++
			}
		}
		for k, v := range t {
			if list, ok := v.([]interface{}); ok && (k == "command" || k == "args") {
				if out, ok := r.command(list); ok {
					t[k] = r.tree(out, false)
					continue
				}
			}
			t[k] = r.tree(v, k == "env")
		}
	case []interface{}:
		for i := range t {
			t[i] = r.tree(t[i], env)
		}
	case string:
		return r.text(t)
	}
	return x
}

func (r *redactor) text(s string) string {
	n := scm.RedactText(s)
	if n != s {
		r.counts.URLCredentials++
	}
	return n
}
