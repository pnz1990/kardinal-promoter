// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionreceipt

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PerNamespace modes of the OCI sink (--receipt-oci-per-namespace, #2285):
// whether a receipt's namespace bounds the repositories its referrers go to,
// within the controller-wide AllowedRepositories.
const (
	// PerNamespaceOff: the controller-wide list alone, as in v0.13.
	PerNamespaceOff = "off"
	// PerNamespaceNarrow: a namespace with entries is held to them; one
	// without is held to the controller-wide list alone.
	PerNamespaceNarrow = "narrow"
	// PerNamespaceRequired: a namespace without entries gets no referrer.
	PerNamespaceRequired = "required"
)

// AnnotationOCIRepositories on a Namespace lists, comma-separated, the
// repositories or path prefixes (ghcr.io/team-a/) its receipts' referrers
// may go to (#2285). It is as strong as the right to edit the Namespace;
// --receipt-oci-namespace-annotation=false ignores it.
const AnnotationOCIRepositories = "kardinal.io/receipt-oci-repositories"

// ValidatePerNamespace checks a --receipt-oci-per-namespace value.
func ValidatePerNamespace(mode string) error {
	switch mode {
	case PerNamespaceOff, PerNamespaceNarrow, PerNamespaceRequired:
		return nil
	}
	return fmt.Errorf("%q: off, narrow or required", mode)
}

// ParseNamespaceRepositories parses --receipt-oci-namespace-repositories:
// namespace=entry,entry;namespace=entry. Every namespace must be a valid
// name, listed once, with at least one entry, and every entry an
// allowedRepositories entry (a registry, or a registry and a path prefix):
// a bad map stops the controller at start rather than refusing receipts.
func ParseNamespaceRepositories(v string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, item := range strings.Split(v, ";") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		ns, list, ok := strings.Cut(item, "=")
		ns = strings.TrimSpace(ns)
		if !ok {
			return nil, fmt.Errorf("%q: namespace=repository,repository", item)
		}
		if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
			return nil, fmt.Errorf("namespace %q: %s", ns, strings.Join(errs, "; "))
		}
		if _, dup := out[ns]; dup {
			return nil, fmt.Errorf("namespace %q listed twice", ns)
		}
		entries, err := parseEntryList(list)
		if err != nil {
			return nil, fmt.Errorf("namespace %s: %w", ns, err)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("namespace %s: no repositories", ns)
		}
		out[ns] = entries
	}
	return out, nil
}

// parseEntryList splits a comma-separated list of allowedRepositories
// entries, refusing one that does not parse, or whose registry is not a
// host name with an optional port: "*" parses as a registry, matches
// nothing and would be taken for a wildcard (QA-2 on #2382).
func parseEntryList(list string) ([]string, error) {
	var out []string
	for _, e := range strings.Split(list, ",") {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		reg, _, ok := parseAllowed(e)
		if !ok || !registryHost(reg) {
			return nil, fmt.Errorf("%q is not a registry or a registry and path prefix (ghcr.io/acme/); there are no wildcards", e)
		}
		out = append(out, e)
	}
	return out, nil
}

// registryHost reports whether reg is a host name (DNS subdomain) with an
// optional port.
func registryHost(reg string) bool {
	host, port, hasPort := strings.Cut(reg, ":")
	if hasPort {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return len(validation.IsDNS1123Subdomain(host)) == 0
}

// matchesAny reports whether repo's parsed registry is an entry's and its
// path starts with the entry's path components.
func matchesAny(entries []string, repo string) bool {
	reg, path, ok := parseRepository(repo)
	if !ok {
		return false
	}
	for _, p := range entries {
		ereg, epath, ok := parseAllowed(p)
		if !ok || ereg != reg || len(epath) > len(path) {
			continue
		}
		if slices.Equal(epath, path[:len(epath)]) {
			return true
		}
	}
	return false
}

// ociScope is where the OCI sink may push the referrers of one namespace's
// receipts: the controller-wide list, and with PerNamespace narrow or
// required the namespace's entries (the flag map and, unless ignored, its
// Namespace's annotation).
type ociScope struct {
	sink      *OCISink
	namespace string
	// entries are the namespace's; nil when it has none.
	entries []string
	// invalid is an annotation entry that does not parse: nothing is
	// pushed for the namespace until it is fixed.
	invalid string
}

// ociScopeFor reads ns's entries. An error is the Namespace's read failing:
// the caller retries, and pushes nothing meanwhile.
func (r *Reconciler) ociScopeFor(ctx context.Context, ns string) (*ociScope, error) {
	s := &ociScope{sink: r.OCI, namespace: ns}
	if r.OCI.Repository != "" || r.OCI.PerNamespace == "" || r.OCI.PerNamespace == PerNamespaceOff {
		return s, nil
	}
	s.entries = slices.Clone(r.OCI.NamespaceRepositories[ns])
	if r.OCI.NamespaceAnnotation {
		var n corev1.Namespace
		// A Namespace gone (its receipts with it) has no annotation.
		if err := r.reader().Get(ctx, types.NamespacedName{Name: ns}, &n); client.IgnoreNotFound(err) != nil {
			return nil, fmt.Errorf("read Namespace %s for its %s annotation: %w", ns, AnnotationOCIRepositories, err)
		}
		if v, ok := n.Annotations[AnnotationOCIRepositories]; ok {
			entries, err := parseEntryList(v)
			if err != nil {
				s.invalid = err.Error()
			}
			s.entries = append(s.entries, entries...)
		}
	}
	return s, nil
}

// allowed reports whether the sink may push a referrer of an image in repo,
// and when not, why.
func (s *ociScope) allowed(repo string) (bool, string) {
	o := s.sink
	if o.Repository != "" {
		return true, "" // the admin's own repository
	}
	if len(o.AllowedRepositories) > 0 && !matchesAny(o.AllowedRepositories, repo) {
		return false, "not in the controller's allowed repositories (sinks.oci.allowedRepositories)"
	}
	switch o.PerNamespace {
	case "", PerNamespaceOff:
		return true, ""
	}
	switch {
	case s.invalid != "":
		return false, fmt.Sprintf("namespace %s's %s annotation has an entry that does not parse (%s): nothing is pushed until it is fixed",
			s.namespace, AnnotationOCIRepositories, s.invalid)
	case s.entries == nil && o.PerNamespace == PerNamespaceRequired:
		return false, fmt.Sprintf("namespace %s has no receipt OCI repositories (sinks.oci.perNamespace required)", s.namespace)
	case s.entries == nil:
		return true, ""
	case !matchesAny(s.entries, repo):
		return false, fmt.Sprintf("not in namespace %s's receipt OCI repositories", s.namespace)
	}
	return true, ""
}
