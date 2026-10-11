// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// errBundleRef is the error of a version or Bundle name that names no
// Bundle, or more than one (errors.Is matches a bundleRefError).
var errBundleRef = errors.New("no single Bundle")

// bundleRefError is resolveBundleRef's error: its message says what was
// found, and it is errBundleRef.
type bundleRefError struct{ msg string }

func (e *bundleRefError) Error() string        { return e.msg }
func (e *bundleRefError) Is(target error) bool { return target == errBundleRef }

// refErrorf is a bundleRefError with a formatted message.
func refErrorf(format string, args ...any) error {
	return &bundleRefError{msg: fmt.Sprintf(format, args...)}
}

// maxCandidates is how many Bundles a not-found or ambiguous error lists.
const maxCandidates = 5

// refScope is where resolveBundleRef looks and how strict it is.
type refScope struct {
	// ns is the namespace; "" is every namespace (get auditevents -A).
	ns string
	// pipeline, when set, keeps the Bundles of that pipeline.
	pipeline string
	// strict refuses a ref that is one Bundle's name and another's version:
	// for a command that rejects or deletes, the user must say which.
	strict bool
}

// resolveBundleRef is the Bundle ref names in ns (refScope{ns, pipeline}).
func resolveBundleRef(ctx context.Context, c sigs_client.Client, ns, pipeline, ref string) (string, error) {
	name, _, err := resolveBundleRefIn(ctx, c, refScope{ns: ns, pipeline: pipeline}, ref)
	return name, err
}

// resolveBundleRefIn is the Bundle ref names in s, and its namespace: a
// Bundle of that name (of s.pipeline, when set), or else the one Bundle whose
// version, image tag, chart version or config commit is ref. None, or more
// than one, is an error that lists the candidates (the matches, or the
// newest Bundles), each as "1.5.0 (my-app-x7k2p) [Verified]". With s.strict a
// name that is also another Bundle's version is refused, listing both. A
// user who may get Bundles but not list them gets the get's not found.
func resolveBundleRefIn(ctx context.Context, c sigs_client.Client, s refScope, ref string) (string, string, error) {
	if ref == "" {
		return "", s.ns, nil
	}
	var named *v1alpha1.Bundle
	if s.ns != "" {
		var b v1alpha1.Bundle
		err := c.Get(ctx, types.NamespacedName{Namespace: s.ns, Name: ref}, &b)
		switch {
		case err == nil:
			if s.pipeline == "" || b.Spec.Pipeline == s.pipeline {
				named = &b
			}
		case !apierrors.IsNotFound(err):
			return "", "", fmt.Errorf("get bundle %q: %w", ref, HumanError(err))
		}
	}
	var opts []sigs_client.ListOption
	if s.ns != "" {
		opts = append(opts, sigs_client.InNamespace(s.ns))
	}
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, opts...); err != nil {
		if named != nil {
			return named.Name, named.Namespace, nil
		}
		if apierrors.IsForbidden(err) && s.ns != "" {
			return "", "", fmt.Errorf("bundle %q not found in namespace %s (and you may not list Bundles there to look for a version %q)", ref, s.ns, ref)
		}
		return "", "", fmt.Errorf("list bundles: %w", HumanError(err))
	}
	bundles := list.Items
	if s.pipeline != "" {
		bundles = slices.DeleteFunc(bundles, func(b v1alpha1.Bundle) bool { return b.Spec.Pipeline != s.pipeline })
	}
	slices.SortFunc(bundles, func(a, b v1alpha1.Bundle) int { return lifecycle.CompareCreation(&b, &a) })
	var matches []v1alpha1.Bundle
	for _, b := range bundles {
		if (named == nil || b.Name != named.Name || b.Namespace != named.Namespace) && bundleNamedBy(&b, ref) {
			matches = append(matches, b)
		}
		if s.ns == "" && b.Name == ref && named == nil {
			nb := b
			named = &nb
		}
	}
	where := "in " + s.ns
	switch {
	case s.pipeline != "":
		where = "of pipeline " + s.pipeline
	case s.ns == "":
		where = "in any namespace"
	}
	withPipeline := s.pipeline == ""
	if named != nil {
		if s.strict && len(matches) > 0 {
			return "", "", refErrorf("%q is the name of %s and a version of %s; say which with --pipeline, or give the other's Bundle name",
				ref, candidateList([]v1alpha1.Bundle{*named}, withPipeline), candidateList(matches, withPipeline))
		}
		return named.Name, named.Namespace, nil
	}
	switch len(matches) {
	case 1:
		return matches[0].Name, matches[0].Namespace, nil
	case 0:
		if len(bundles) == 0 {
			return "", "", refErrorf("no Bundle named %q, and no version %q %s: there are no Bundles %s", ref, ref, where, where)
		}
		return "", "", refErrorf("no Bundle named %q, and no version %q %s; the newest are: %s",
			ref, ref, where, candidateList(bundles, withPipeline))
	}
	return "", "", refErrorf("version %q names %d Bundles %s: %s; give the Bundle's name",
		ref, len(matches), where, candidateList(matches, withPipeline))
}

// bundleNamedBy reports whether ref names b's version: its version, one of
// its image tags, its chart version, or its config commit (7 characters or
// more of it).
func bundleNamedBy(b *v1alpha1.Bundle, ref string) bool {
	if createdOf(b).Version == ref {
		return true
	}
	for _, img := range b.Spec.Images {
		if img.Tag == ref {
			return true
		}
	}
	if b.Spec.Chart != nil && b.Spec.Chart.Version == ref {
		return true
	}
	if cr := b.Spec.ConfigRef; cr != nil && len(ref) >= 7 && strings.HasPrefix(cr.CommitSHA, ref) {
		return true
	}
	return false
}

// candidateList is up to maxCandidates of bundles, newest first, each as
// "1.5.0 (name) [Phase]" (with its pipeline when withPipeline), and how many
// more there are.
func candidateList(bundles []v1alpha1.Bundle, withPipeline bool) string {
	var out []string
	for i := range bundles {
		if i == maxCandidates {
			out = append(out, fmt.Sprintf("and %d more (kardinal get bundles)", len(bundles)-maxCandidates))
			break
		}
		b := &bundles[i]
		s := versionLabel(createdOf(b).Version, b.Name)
		if withPipeline && b.Spec.Pipeline != "" {
			s += " of " + b.Spec.Pipeline
		}
		if b.Status.Phase != "" {
			s += " [" + b.Status.Phase + "]"
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

// bundleRefLabel is Bundle name of ns as a person reads it ("1.5.0
// (my-app-x7k2p)"), or its name when it cannot be read.
func bundleRefLabel(ctx context.Context, c sigs_client.Client, ns, name string) string {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		return name
	}
	return versionLabel(createdOf(&b).Version, name)
}
