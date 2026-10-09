// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Naming conventions.
//
// Two kinds of names are generated:
//   - Node IDs appear in CEL expressions. kro requires ^[A-Za-z][A-Za-z0-9]*$,
//     so they are camelCase (CELSafeSlug). ValidateNodeIDs rejects collisions.
//   - metadata.name values are DNS-1123 subdomains (kebab-case, slugify).
//     boundedName keeps the readable form when it is valid, fits, and lost no
//     characters, and otherwise switches to a cut prefix plus a hash of the
//     raw inputs, so distinct inputs cannot share an object name.

const (
	// maxGraphNameLen keeps Graph names to the label-value limit so the name
	// stays usable in selectors and in Bundle.status.graphRef consumers.
	maxGraphNameLen = validation.LabelValueMaxLength
	// maxObjectNameLen is the metadata.name limit for the generated objects.
	maxObjectNameLen = validation.DNS1123SubdomainMaxLength
	// nameHashLen is the number of hex characters of the hash suffix.
	nameHashLen = 8
)

// boundedName returns preferred when exact is true and preferred is a valid
// DNS-1123 subdomain of at most max characters. Otherwise it returns a prefix
// of slugify(preferred), cut so the result fits in max and trimmed of '-' and
// '.', followed by "-" and a hash of key. key must identify the object
// uniquely (see nameKey); exact must be false when preferred was built with a
// lossy transformation, because then two different keys can share preferred.
func boundedName(preferred string, exact bool, key string, max int) string {
	if exact && len(preferred) <= max && len(validation.IsDNS1123Subdomain(preferred)) == 0 {
		return preferred
	}
	sum := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(sum[:])[:nameHashLen]
	prefix := slugify(preferred)
	if n := max - nameHashLen - 1; len(prefix) > n {
		prefix = prefix[:n]
	}
	prefix = strings.Trim(prefix, "-.")
	if prefix == "" {
		return suffix
	}
	return prefix + "-" + suffix
}

// nameKey joins the raw name parts into a hash key. NUL cannot appear in a
// Kubernetes name or label value, so distinct part lists give distinct keys.
func nameKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}

// isSlug reports whether s is used in a generated name unchanged.
func isSlug(s string) bool {
	return s != "" && slugify(s) == s
}

// GraphNameFrom returns the deterministic Graph CR name for a (pipeline, bundle) pair.
// This is exported so the Bundle reconciler can compute the expected graph name
// without importing translator-layer code.
func GraphNameFrom(pipeline, bundle string) string {
	return graphNameFrom(pipeline, bundle)
}

// graphNameFrom returns "<pipeline>-<bundle>" when both names are already
// slugs and the result fits in 63 characters, and a hash-suffixed name
// otherwise (for example "app-v1.2", or a long GenerateName Bundle), so two
// Bundles of one Pipeline never share a Graph name through truncation or
// slugging. Graph ownership (GraphClient.Create) refuses the remaining case,
// a pipeline/bundle split of the same string.
func graphNameFrom(pipeline, bundle string) string {
	preferred := slugify(pipeline) + "-" + slugify(bundle)
	return boundedName(preferred, isSlug(pipeline) && isSlug(bundle),
		nameKey("graph", pipeline, bundle), maxGraphNameLen)
}

// promotionStepK8sName returns the PromotionStep metadata.name
// "<pipeline>-<bundle>-<env>", hash-suffixed when the environment name is not
// a slug (for example "Prod" or "prod_eu") or the name is too long.
func promotionStepK8sName(pipeline, bundle, env string) string {
	preferred := pipeline + "-" + slugify(bundle) + "-" + slugify(env)
	return boundedName(preferred, isSlug(bundle) && isSlug(env),
		nameKey("step", pipeline, bundle, env), maxObjectNameLen)
}

// prStatusNodeK8sName returns the PRStatus metadata.name
// "prstatus-<bundle>-<env>", hash-suffixed when it would lose characters or
// exceed 63 characters.
func prStatusNodeK8sName(bundle, envName string) string {
	preferred := "prstatus-" + slugify(bundle) + "-" + slugify(envName)
	return boundedName(preferred, isSlug(bundle) && isSlug(envName),
		nameKey("prstatus", bundle, envName), validation.DNS1123LabelMaxLength)
}

// gateNodeK8sName returns the metadata.name of a PolicyGate instance,
// "<gate>-<namespace>-<env>--<bundle>", hash-suffixed when it would lose
// characters (a dotted gate name, an uppercase environment) or is too long.
func gateNodeK8sName(bundle, gateName, gateNS, envName string) string {
	ns := gateNS
	if ns == "" {
		ns = "default"
	}
	preferred := fmt.Sprintf("%s-%s-%s--%s", gateName, ns, envName, bundle)
	exact := isSlug(gateName) && isSlug(ns) && isSlug(envName) && isSlug(bundle)
	return boundedName(preferred, exact, nameKey("gate", ns, gateName, envName, bundle), maxObjectNameLen)
}

// slugify replaces characters not valid in Kubernetes names with dashes.
// Produces kebab-case (hyphens) suitable for metadata.name fields.
// Do NOT use for CEL expression identifiers — use CELSafeSlug instead.
func slugify(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-':
			b.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			b.WriteRune(c - 'A' + 'a')
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// CELSafeSlug creates an identifier safe for use as both a CEL variable name
// and as a kro Graph node ID.
//
// kro requires node IDs to match ^[A-Za-z][A-Za-z0-9]*$ (no hyphens or
// underscores) because other nodes reference them as CEL identifiers.
//
// Solution: camelCase. Non-alphanumeric characters (hyphens, underscores, dots,
// etc.) become word boundaries — the following letter is capitalised and the
// separator is dropped. The result always matches [a-zA-Z][a-zA-Z0-9]*.
// Different inputs can give the same slug ("prod-eu", "prod_eu", "prodEu");
// ValidateNodeIDs rejects the resulting duplicate IDs.
//
// Examples:
//
//	"kardinal-test-app-uat"  → "kardinalTestAppUat"
//	"no_weekend_deploys"     → "noWeekendDeploys"
//	"prod-eu"                → "prodEu"
//	"0bad"                   → "x0bad"  (leading digit guarded by "x" prefix)
//	"MyApp"                  → "myApp"  (leading uppercase lowercased)
func CELSafeSlug(s string) string {
	var b strings.Builder
	upperNext := false
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			if upperNext && b.Len() > 0 {
				b.WriteRune(c - 'a' + 'A')
			} else {
				b.WriteRune(c)
			}
			upperNext = false
		case c >= 'A' && c <= 'Z':
			if b.Len() == 0 {
				b.WriteRune(c - 'A' + 'a') // first char always lowercase
			} else {
				b.WriteRune(c)
			}
			upperNext = false
		case c >= '0' && c <= '9':
			if b.Len() == 0 {
				b.WriteString("x") // guard leading digit with a safe prefix
			}
			b.WriteRune(c)
			upperNext = false
		default:
			// hyphens, underscores, spaces, dots → camelCase word boundary
			upperNext = true
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}
