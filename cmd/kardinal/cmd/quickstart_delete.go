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
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// quickstartFieldManager is the field manager quickstart applies with: an
// object it manages fields of is one quickstart applied.
const quickstartFieldManager = "kardinal-quickstart"

// quickstartDeleteWait bounds how long --delete waits for the demo's
// Bundles and PromotionSteps to go (the controller closes their open PRs
// first); a test shortens it.
var quickstartDeleteWait = 2 * time.Minute

// quickstartDeleteFn is kardinal quickstart --delete: behind the same
// local-context check as quickstart, it deletes the demo quickstart applied
// (its Bundles first, waiting for the controller to close their PRs, then the
// Pipeline, the gate, the ApplicationSet and the git Secret), and nothing
// else. An object quickstart did not apply is kept, and so is the git Secret
// while another Pipeline uses it. A re-run deletes nothing.
func quickstartDeleteFn(ctx context.Context, w io.Writer, confirm func(string) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	kctx, only, err := requireLocalContext()
	if err != nil {
		return err
	}
	c, err := quickstartClient(only)
	if err != nil {
		return fmt.Errorf("quickstart --delete: %w", err)
	}
	if err := confirm(fmt.Sprintf("Delete the kardinal demo (Pipeline %s/%s, its versions, gate, ApplicationSet and git Secret) from %s?",
		quickstartNS, quickstartPipeline, kctx)); err != nil {
		return err
	}
	q := &quickstart{ctx: ctx, w: w, c: c, kctx: kctx}
	_, _ = fmt.Fprintf(w, "Removing the kardinal demo from %s\n", kctx)

	pipeline := &unstructured.Unstructured{}
	pipeline.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Pipeline"))
	pipelineKey := types.NamespacedName{Namespace: quickstartNS, Name: quickstartPipeline}
	ours, err := q.quickstartOwned(pipeline, pipelineKey)
	if err != nil {
		return err
	}
	// 1. The versions, while the Pipeline and its Secret are there for the
	// controller to close their PRs with.
	if ours != "theirs" {
		if err := q.deleteDemoBundles(); err != nil {
			return err
		}
	}
	// 2. What quickstart applied.
	for _, o := range []struct {
		what string
		gvk  schema.GroupVersionKind
		key  types.NamespacedName
		note string
	}{
		{"Pipeline " + quickstartNS + "/" + quickstartPipeline, v1alpha1.GroupVersion.WithKind("Pipeline"), pipelineKey, ""},
		{"PolicyGate " + quickstartNS + "/" + quickstartGate, v1alpha1.GroupVersion.WithKind("PolicyGate"),
			types.NamespacedName{Namespace: quickstartNS, Name: quickstartGate}, ""},
		{"Argo CD ApplicationSet argocd/" + quickstartAppSet, applicationSetGVK,
			types.NamespacedName{Namespace: "argocd", Name: quickstartAppSet}, " (Argo CD deletes its Applications)"},
	} {
		if err := q.deleteApplied(o.what, o.gvk, o.key, o.note); err != nil {
			return err
		}
	}
	// 3. The git Secret, unless another Pipeline uses it.
	if err := q.deleteGitSecret(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, `Kept: kro, Argo CD, kardinal-promoter and its Secret %s/%s, and the namespaces the
Applications deployed to. To remove those too:
  kubectl delete namespace %s
To set the demo up again: kardinal quickstart --repo <your fork of kardinal-demo>
`, quickstartControllerNS, quickstartSCMSecret, strings.Join(quickstartWorkloadNamespaces(), " "))
	return nil
}

// quickstartWorkloadNamespaces are the namespaces the demo's Applications
// deploy to.
func quickstartWorkloadNamespaces() []string {
	out := make([]string, 0, len(quickstartEnvs))
	for _, e := range quickstartEnvs {
		out = append(out, quickstartAppSet+"-"+e)
	}
	return out
}

// quickstartOwned reads key into u and says whose it is: "gone", "ours"
// (quickstart applied it: kardinal-quickstart manages fields of it) or
// "theirs". A kind the cluster does not serve is gone.
func (q *quickstart) quickstartOwned(u *unstructured.Unstructured, key types.NamespacedName) (string, error) {
	if !q.served(u.GroupVersionKind()) {
		return "gone", nil
	}
	if err := q.c.Get(q.ctx, key, u); err != nil {
		if apierrors.IsNotFound(err) {
			return "gone", nil
		}
		return "", fmt.Errorf("quickstart --delete: get %s %s: %w", u.GetKind(), key, HumanError(err))
	}
	if slices.ContainsFunc(u.GetManagedFields(), func(f metav1.ManagedFieldsEntry) bool { return f.Manager == quickstartFieldManager }) {
		return "ours", nil
	}
	return "theirs", nil
}

// deleteApplied deletes the object key of gvk when quickstart applied it.
func (q *quickstart) deleteApplied(what string, gvk schema.GroupVersionKind, key types.NamespacedName, note string) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	owner, err := q.quickstartOwned(u, key)
	if err != nil {
		return err
	}
	switch owner {
	case "gone":
		q.say(what, "already gone")
		return nil
	case "theirs":
		q.say(what, "kept (quickstart did not create it)")
		return nil
	}
	if err := q.c.Delete(q.ctx, u, sigs_client.Preconditions{UID: ptrUID(u.GetUID())}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("quickstart --delete: delete %s: %w", what, HumanError(err))
	}
	q.say(what, "deleted"+note)
	return nil
}

// deleteDemoBundles deletes the Bundles of the demo Pipeline and waits,
// up to quickstartDeleteWait, until they and their PromotionSteps are gone.
func (q *quickstart) deleteDemoBundles() error {
	what := "Versions of " + quickstartNS + "/" + quickstartPipeline
	bundles, err := q.demoBundles()
	if err != nil {
		return err
	}
	var names []string
	for i := range bundles {
		b := &bundles[i]
		if b.DeletionTimestamp == nil {
			if err := q.c.Delete(q.ctx, b); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("quickstart --delete: delete Bundle %s/%s: %w", b.Namespace, b.Name, HumanError(err))
			}
		}
		names = append(names, versionLabel(createdOf(b).Version, b.Name))
	}
	deadline := time.Now().Add(quickstartDeleteWait)
	for {
		left, err := q.demoLeft()
		if err != nil {
			return err
		}
		if left == "" {
			break
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("quickstart --delete: %s still there after %s: the controller closes their open PRs first; "+
				"is it running (kardinal doctor)? Nothing else was deleted. Run kardinal quickstart --delete again", left, quickstartDeleteWait)
		}
		select {
		case <-q.ctx.Done():
			return q.ctx.Err()
		case <-time.After(shipPollInterval):
		}
	}
	if len(names) == 0 {
		q.say(what, "already gone")
		return nil
	}
	q.say(what, fmt.Sprintf("deleted %d (%s)", len(names), strings.Join(names, ", ")))
	return nil
}

// demoBundles are the Bundles of the demo Pipeline.
func (q *quickstart) demoBundles() ([]v1alpha1.Bundle, error) {
	var list v1alpha1.BundleList
	if err := q.c.List(q.ctx, &list, sigs_client.InNamespace(quickstartNS)); err != nil {
		return nil, fmt.Errorf("quickstart --delete: list Bundles: %w", HumanError(err))
	}
	return slices.DeleteFunc(list.Items, func(b v1alpha1.Bundle) bool { return b.Spec.Pipeline != quickstartPipeline }), nil
}

// demoLeft says what of the demo's versions is still being deleted ("" when
// nothing): Bundles, and PromotionSteps of the Pipeline.
func (q *quickstart) demoLeft() (string, error) {
	bundles, err := q.demoBundles()
	if err != nil {
		return "", err
	}
	var steps v1alpha1.PromotionStepList
	if err := q.c.List(q.ctx, &steps, sigs_client.InNamespace(quickstartNS),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": quickstartPipeline}); err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("quickstart --delete: list PromotionSteps: %w", HumanError(err))
	}
	var left []string
	if n := len(bundles); n > 0 {
		left = append(left, fmt.Sprintf("%d Bundle(s)", n))
	}
	if n := len(steps.Items); n > 0 {
		left = append(left, fmt.Sprintf("%d PromotionStep(s)", n))
	}
	return strings.Join(left, " and "), nil
}

// deleteGitSecret deletes the demo's git Secret unless a Pipeline other than
// the demo's refers to it.
func (q *quickstart) deleteGitSecret() error {
	what := "Secret " + quickstartNS + "/" + quickstartSecret
	var s corev1.Secret
	if err := q.c.Get(q.ctx, types.NamespacedName{Namespace: quickstartNS, Name: quickstartSecret}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			q.say(what, "already gone")
			return nil
		}
		return fmt.Errorf("quickstart --delete: get %s: %w", what, HumanError(err))
	}
	var pipelines v1alpha1.PipelineList
	if err := q.c.List(q.ctx, &pipelines, sigs_client.InNamespace(quickstartNS)); err != nil {
		return fmt.Errorf("quickstart --delete: list Pipelines (to see who uses %s): %w", what, HumanError(err))
	}
	var users []string
	for i := range pipelines.Items {
		p := &pipelines.Items[i]
		if p.DeletionTimestamp == nil && referencesSecret(p, quickstartSecret) {
			users = append(users, p.Name)
		}
	}
	if len(users) > 0 {
		q.say(what, "kept (used by Pipeline "+strings.Join(users, ", ")+")")
		return nil
	}
	if err := q.c.Delete(q.ctx, &s, sigs_client.Preconditions{UID: ptrUID(s.UID)}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("quickstart --delete: delete %s: %w", what, HumanError(err))
	}
	q.say(what, "deleted")
	return nil
}

// referencesSecret reports whether a secretRef anywhere in p's spec names
// the Secret name (of p's namespace: a git secretRef may not name another).
func referencesSecret(p *v1alpha1.Pipeline, name string) bool {
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&p.Spec)
	if err != nil {
		return true // unreadable: assume it does, and keep the Secret
	}
	var walk func(v any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if ref, ok := child.(map[string]any); ok && k == "secretRef" && ref["name"] == name {
					if ns, _ := ref["namespace"].(string); ns == "" || ns == p.Namespace {
						return true
					}
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range t {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(spec)
}

// ptrUID is a pointer to uid, for a delete precondition: what was checked
// is what is deleted.
func ptrUID(uid types.UID) *types.UID { return &uid }
