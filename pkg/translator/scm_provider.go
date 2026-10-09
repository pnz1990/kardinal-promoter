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

package translator

import (
	"context"
	"fmt"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// WithProviders sets the registry whose checks (allowedNamespaces through
// its Namespace cache, the apiURL scheme, allowedRepositories) the
// translator applies to a Pipeline's spec.git.providerRef. Without one it
// uses a registry over the translator's reader.
func (t *Translator) WithProviders(r *scm.Registry) *Translator {
	t.providers = r
	return t
}

// resolveProvider returns the identity of the Pipeline's spec.git.providerRef,
// or nil when it has none. It checks that the provider exists and that the
// registry allows its use by this Pipeline (Registry.Check): a
// ClusterScmProvider's allowedNamespaces, the apiURL scheme and the
// provider's allowedRepositories.
func (t *Translator) resolveProvider(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline) (*kardinalv1alpha1.ScmProviderIdentity, error) {
	ref := pipeline.Spec.Git.ProviderRef
	if ref == nil {
		return nil, nil
	}
	reg := t.providers
	if reg == nil {
		reg = &scm.Registry{Client: t.k8s}
	}
	spec, err := scm.GetProvider(ctx, reg.Client, pipeline.Namespace, ref.Kind, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("spec.git.providerRef: %w", err)
	}
	repo, err := scm.RepoFromURL(pipeline.Spec.Git.URL)
	if err != nil {
		return nil, fmt.Errorf("spec.git.url: %w", err)
	}
	if err := reg.Check(ctx, spec, pipeline.Namespace, repo); err != nil {
		return nil, fmt.Errorf("spec.git.providerRef: %w", err)
	}
	id := spec.Identity
	return &id, nil
}
