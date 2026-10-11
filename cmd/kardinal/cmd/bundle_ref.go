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

// resolveBundleRef is the Bundle ref names in ns: a Bundle of that name, or
// else the one Bundle (of pipeline, when it is not "") whose version or one
// of whose image tags is ref. None, or more than one, is an error that lists
// the candidates (the matches, or the newest Bundles), each as
// "1.5.0 (my-app-x7k2p), Verified".
func resolveBundleRef(ctx context.Context, c sigs_client.Client, ns, pipeline, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	var b v1alpha1.Bundle
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref}, &b)
	switch {
	case err == nil:
		return ref, nil
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("get bundle %q: %w", ref, HumanError(err))
	}
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
		return "", fmt.Errorf("list bundles: %w", HumanError(err))
	}
	bundles := list.Items
	if pipeline != "" {
		bundles = slices.DeleteFunc(bundles, func(b v1alpha1.Bundle) bool { return b.Spec.Pipeline != pipeline })
	}
	slices.SortFunc(bundles, func(a, b v1alpha1.Bundle) int { return lifecycle.CompareCreation(&b, &a) })
	var matches []v1alpha1.Bundle
	for _, b := range bundles {
		if bundleNamedBy(&b, ref) {
			matches = append(matches, b)
		}
	}
	where := "in " + ns
	if pipeline != "" {
		where = "of pipeline " + pipeline
	}
	switch len(matches) {
	case 1:
		return matches[0].Name, nil
	case 0:
		if len(bundles) == 0 {
			return "", refErrorf("no Bundle named %q, and no version %q %s: there are no Bundles %s", ref, ref, where, where)
		}
		return "", refErrorf("no Bundle named %q, and no version %q %s; the newest are: %s",
			ref, ref, where, candidateList(bundles, pipeline == ""))
	}
	return "", refErrorf("version %q names %d Bundles %s: %s; give the Bundle's name",
		ref, len(matches), where, candidateList(matches, pipeline == ""))
}

// bundleNamedBy reports whether ref is b's version or one of its image tags.
func bundleNamedBy(b *v1alpha1.Bundle, ref string) bool {
	if createdOf(b).Version == ref {
		return true
	}
	for _, img := range b.Spec.Images {
		if img.Tag == ref {
			return true
		}
	}
	return false
}

// candidateList is up to maxCandidates of bundles, newest first, each as
// "1.5.0 (name), Phase" (with its pipeline when withPipeline), and how many
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
			s += ", " + b.Status.Phase
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
