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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// resolveProvider returns the identity of the Pipeline's spec.git.providerRef,
// or nil when it has none. It checks that the provider exists, that a
// ClusterScmProvider's allowedNamespaces selects the Pipeline's namespace, and
// that the Pipeline's repository is in the provider's allowedRepositories.
func (t *Translator) resolveProvider(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline) (*kardinalv1alpha1.ScmProviderIdentity, error) {
	ref := pipeline.Spec.Git.ProviderRef
	if ref == nil {
		return nil, nil
	}
	spec, cluster, err := scm.GetProvider(ctx, t.k8s, pipeline.Namespace, ref.Kind, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("spec.git.providerRef: %w", err)
	}
	if cluster != nil {
		if err := t.namespaceAllowed(ctx, cluster, pipeline.Namespace); err != nil {
			return nil, fmt.Errorf("spec.git.providerRef: %w", err)
		}
	}
	repo, err := scm.RepoFromURL(pipeline.Spec.Git.URL)
	if err != nil {
		return nil, fmt.Errorf("spec.git.url: %w", err)
	}
	if !scm.RepositoryAllowed(spec.Spec.AllowedRepositories, repo) {
		return nil, fmt.Errorf("spec.git.providerRef: %s %s: repository %s: %w", spec.Identity.Kind, ref.Name, repo, scm.ErrRepositoryNotAllowed)
	}
	id := spec.Identity
	return &id, nil
}

// namespaceAllowed checks the ClusterScmProvider's allowedNamespaces against
// the labels of namespace ns. No selector allows no namespace.
func (t *Translator) namespaceAllowed(ctx context.Context, p *kardinalv1alpha1.ClusterScmProvider, ns string) error {
	if p.Spec.AllowedNamespaces == nil {
		return fmt.Errorf("ClusterScmProvider %s has no spec.allowedNamespaces: %w", p.Name, scm.ErrNamespaceNotAllowed)
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.AllowedNamespaces)
	if err != nil {
		return fmt.Errorf("ClusterScmProvider %s: spec.allowedNamespaces: %v: %w", p.Name, err, scm.ErrNamespaceNotAllowed)
	}
	reader := t.apiReader
	if reader == nil {
		reader = t.k8s
	}
	var n corev1.Namespace
	if err := reader.Get(ctx, types.NamespacedName{Name: ns}, &n); err != nil {
		return fmt.Errorf("read namespace %s: %w", ns, err)
	}
	if !sel.Matches(labels.Set(n.Labels)) {
		return fmt.Errorf("ClusterScmProvider %s, namespace %s: %w", p.Name, ns, scm.ErrNamespaceNotAllowed)
	}
	return nil
}
