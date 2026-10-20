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
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// applicationGVK is Argo CD's Application.
var applicationGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}

// localPart is kardinal-promoter, Argo CD or kro, as quickstart --local
// --delete finds it: whether this install put it there (ours: its Helm
// release, or for Argo CD its namespace, carries the install ID), the
// objects of its API groups that are not the demo's (foreign: while there is
// one, the part is kept, CRDs and all, since deleting a CRD deletes every
// object of its kind), its CRDs, and its namespace when this install created
// it.
type localPart struct {
	name      string
	groups    []string
	ours      bool
	foreign   []string
	crds      []string
	namespace string
}

func (p localPart) remove() bool { return p.ours && len(p.foreign) == 0 }

// localPlan is what quickstart --local --delete removes besides the demo,
// all of it marked with the install ID of the record: the namespaces it
// created for the Applications, each part it installed that nothing else
// uses, and the git server.
type localPlan struct {
	id         string
	namespaces []string
	parts      []localPart // kardinal-promoter, Argo CD, kro, in that order
	gitServer  bool
}

// lines says what the plan removes and what it keeps, one line each, for
// the confirmation.
func (p localPlan) lines() []string {
	var out []string
	if len(p.namespaces) > 0 {
		out = append(out, "remove namespaces "+strings.Join(p.namespaces, ", ")+" (the Applications')")
	}
	for _, part := range p.parts {
		switch {
		case !part.ours:
			out = append(out, "keep "+part.name+": this quickstart did not install it")
		case len(part.foreign) > 0:
			out = append(out, fmt.Sprintf("keep %s and its CRDs: %d object(s) that are not the demo's (%s)",
				part.name, len(part.foreign), strings.Join(firstN(part.foreign, 3), ", ")))
		default:
			line := fmt.Sprintf("uninstall %s with its %d CRDs", part.name, len(part.crds))
			if part.namespace != "" {
				line += " and namespace " + part.namespace
			}
			out = append(out, line)
		}
	}
	if p.gitServer {
		out = append(out, "remove the git server and its repository (namespace "+localGitNS+")")
	}
	return out
}

// firstN is the first n of s, with "…" when there are more.
func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(slices.Clone(s[:n]), "…")
}

// localID is the install ID of quickstart --local: the record's, when the
// namespace localGitNS is quickstart's and carries the same ID ("" when
// there is no such install).
func (q *quickstart) localID() (string, error) {
	var ns corev1.Namespace
	switch err := q.c.Get(q.ctx, types.NamespacedName{Name: localGitNS}, &ns); {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("get namespace %s: %w", localGitNS, HumanError(err))
	}
	id := ns.Labels[quickstartIDLabel]
	if ns.Labels[quickstartMark] != quickstartMarkValue || id == "" {
		return "", nil
	}
	var cm corev1.ConfigMap
	if err := q.c.Get(q.ctx, types.NamespacedName{Namespace: localGitNS, Name: quickstartRecord}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get ConfigMap %s/%s: %w", localGitNS, quickstartRecord, HumanError(err))
	}
	if cm.Data["id"] != id {
		return "", nil
	}
	return id, nil
}

// ownedNamespace reports whether namespace name carries install ID id.
func (q *quickstart) ownedNamespace(name, id string) (bool, error) {
	var ns corev1.Namespace
	switch err := q.c.Get(q.ctx, types.NamespacedName{Name: name}, &ns); {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("get namespace %s: %w", name, HumanError(err))
	}
	return id != "" && ns.Labels[quickstartMark] == quickstartMarkValue && ns.Labels[quickstartIDLabel] == id, nil
}

// ownedRelease reports whether the latest revision of Helm release name in
// ns carries install ID id (helm --labels stores it on the release's
// Secret, owner=helm).
func (q *quickstart) ownedRelease(name, ns, id string) (bool, error) {
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("SecretList"))
	if err := q.c.List(q.ctx, &list, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"owner": "helm", "name": name}); err != nil {
		return false, fmt.Errorf("list the Helm release %s/%s: %w", ns, name, HumanError(err))
	}
	latest, version := map[string]string(nil), -1
	for _, s := range list.Items {
		v := 0
		if _, err := fmt.Sscan(s.Labels["version"], &v); err == nil && v > version {
			latest, version = s.Labels, v
		}
	}
	return id != "" && latest[quickstartIDLabel] == id, nil
}

// crdInfo is a CustomResourceDefinition, as kubectl get crd -o json shows it.
type crdInfo struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind     string `json:"kind"`
			ListKind string `json:"listKind"`
		} `json:"names"`
		Versions []struct {
			Name    string `json:"name"`
			Storage bool   `json:"storage"`
		} `json:"versions"`
	} `json:"spec"`
}

// crds are the cluster's CRDs.
func (q *quickstart) crds() ([]crdInfo, error) {
	out, err := quickstartRun(q.ctx, "kubectl", append([]string{"get", "crd", "-o", "json"}, q.kubectlFlags()...)...)
	if err != nil {
		return nil, fmt.Errorf("list CRDs: %w", err)
	}
	var list struct {
		Items []crdInfo `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("read the CRDs: %w", err)
	}
	return list.Items, nil
}

// objectsOf lists every object, in every namespace, of the CRDs of groups.
func (q *quickstart) objectsOf(crds []crdInfo, groups []string) ([]unstructured.Unstructured, error) {
	var all []unstructured.Unstructured
	for _, c := range crds {
		if !slices.Contains(groups, c.Spec.Group) {
			continue
		}
		version := ""
		for _, v := range c.Spec.Versions {
			if v.Storage || version == "" {
				version = v.Name
			}
		}
		listKind := c.Spec.Names.ListKind
		if listKind == "" {
			listKind = c.Spec.Names.Kind + "List"
		}
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: c.Spec.Group, Version: version, Kind: listKind})
		if err := q.c.List(q.ctx, &list); err != nil {
			if apierrors.IsNotFound(err) || strings.Contains(err.Error(), "no matches for kind") {
				continue
			}
			return nil, fmt.Errorf("list %s: %w", c.Metadata.Name, HumanError(err))
		}
		for _, u := range list.Items {
			if u.GetKind() == "" {
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: c.Spec.Group, Version: version, Kind: c.Spec.Names.Kind})
			}
			all = append(all, u)
		}
	}
	return all, nil
}

// demoObject reports whether u is the demo's or quickstart's own: an
// object of the demo Pipeline in quickstartNS (named demo or prod-approval,
// labelled with the Pipeline or quickstartMark, or whose spec.pipeline or
// spec.pipelineName is demo), an Argo CD object of install id (the
// ApplicationSet and its Applications carry its label) and Argo CD's
// default AppProject, an object of the kardinal-promoter release (Helm's
// labels), or one whose owner is one of these (owned, by UID).
func demoObject(u unstructured.Unstructured, owned map[types.UID]bool, id string) bool {
	ns, name, labels := u.GetNamespace(), u.GetName(), u.GetLabels()
	for _, o := range u.GetOwnerReferences() {
		if owned[o.UID] {
			return true
		}
	}
	if labels["app.kubernetes.io/managed-by"] == "Helm" && labels["app.kubernetes.io/instance"] == "kardinal-promoter" {
		return true
	}
	if u.GroupVersionKind().Group == "argoproj.io" {
		if ns != "argocd" {
			return false
		}
		if u.GetKind() == "AppProject" {
			return name == "default"
		}
		return id != "" && labels[quickstartIDLabel] == id
	}
	if ns != quickstartNS {
		return false
	}
	if labels[lifecyclePipelineLabel] == quickstartPipeline || labels[quickstartMark] == quickstartMarkValue {
		return true
	}
	// spec.pipeline (a Bundle) or spec.pipelineName (an AuditEvent).
	for _, f := range []string{"pipeline", "pipelineName"} {
		if p, _, _ := unstructured.NestedString(u.Object, "spec", f); p == quickstartPipeline {
			return true
		}
	}
	switch u.GetKind() {
	case "Pipeline":
		return name == quickstartPipeline
	case "PolicyGate":
		return name == quickstartGate
	}
	return false
}

// lifecyclePipelineLabel is the label of an object kardinal makes for a
// Pipeline's promotions (pkg/lifecycle LabelPipeline).
const lifecyclePipelineLabel = "kardinal.io/pipeline"

// foreignObjects are the objects of groups' CRDs that are not the demo's
// (demoObject), as "Kind namespace/name".
func (q *quickstart) foreignObjects(crds []crdInfo, groups []string, id string) ([]string, error) {
	objs, err := q.objectsOf(crds, groups)
	if err != nil {
		return nil, err
	}
	// An object is the demo's when its owner is (to any depth): a Graph of a
	// demo Bundle, a GraphRevision of that Graph. Owners are matched by UID;
	// objects are told apart by kind, namespace and name.
	key := func(u unstructured.Unstructured) string {
		return u.GroupVersionKind().GroupKind().String() + "/" + u.GetNamespace() + "/" + u.GetName()
	}
	demo := map[string]bool{}
	ownerUIDs := map[types.UID]bool{}
	for changed := true; changed; {
		changed = false
		for _, u := range objs {
			if !demo[key(u)] && demoObject(u, ownerUIDs, id) {
				demo[key(u)] = true
				if u.GetUID() != "" {
					ownerUIDs[u.GetUID()] = true
				}
				changed = true
			}
		}
	}
	var out []string
	for _, u := range objs {
		if demo[key(u)] {
			continue
		}
		ref := u.GetName()
		if u.GetNamespace() != "" {
			ref = u.GetNamespace() + "/" + ref
		}
		out = append(out, u.GetKind()+" "+ref)
	}
	slices.Sort(out)
	return out, nil
}

// planLocal is what quickstart --local --delete removes for install id,
// besides the demo.
func (q *quickstart) planLocal(id string) (localPlan, error) {
	plan := localPlan{id: id}
	if id == "" {
		return plan, nil
	}
	for _, ns := range quickstartWorkloadNamespaces() {
		ok, err := q.ownedNamespace(ns, id)
		if err != nil {
			return plan, err
		}
		if ok {
			plan.namespaces = append(plan.namespaces, ns)
		}
	}
	crds, err := q.crds()
	if err != nil {
		return plan, err
	}
	for _, part := range []struct {
		name, release, ns string
		groups            []string
	}{
		{"kardinal-promoter", "kardinal-promoter", quickstartControllerNS, []string{v1alpha1.GroupVersion.Group}},
		{"Argo CD " + argoCDRelease, "", "argocd", []string{"argoproj.io"}},
		{"kro " + quickstartKroVersion, "kro", kroNamespace, []string{"kro.run", "internal.kro.run"}},
	} {
		p := localPart{name: part.name, groups: part.groups}
		nsOurs, err := q.ownedNamespace(part.ns, id)
		if err != nil {
			return plan, err
		}
		if part.release != "" {
			if p.ours, err = q.ownedRelease(part.release, part.ns, id); err != nil {
				return plan, err
			}
		} else {
			// Argo CD is installed from its manifests, into the namespace
			// this install created.
			p.ours = nsOurs
		}
		if nsOurs {
			p.namespace = part.ns
		}
		for _, c := range crds {
			if slices.Contains(part.groups, c.Spec.Group) {
				p.crds = append(p.crds, "customresourcedefinition.apiextensions.k8s.io/"+c.Metadata.Name)
			}
		}
		if p.ours {
			if p.foreign, err = q.foreignObjects(crds, part.groups, id); err != nil {
				return plan, err
			}
		}
		plan.parts = append(plan.parts, p)
	}
	plan.gitServer = true
	return plan, nil
}

// deleteLocal is the end of quickstart --local --delete, after the demo is
// gone: the plan's namespaces, then each part it removes, after checking
// again that nothing but the demo uses it, then the git server.
func (q *quickstart) deleteLocal(plan localPlan) error {
	if plan.id == "" {
		_, _ = fmt.Fprintf(q.w, "No quickstart --local install here (no namespace %s with its record): only the demo was deleted.\n", localGitNS)
		return nil
	}
	// Argo CD deletes the ApplicationSet's Applications; until they are gone
	// it could create their namespaces again (CreateNamespace=true).
	if err := q.waitApplicationsGone(plan.id); err != nil {
		return err
	}
	for _, ns := range plan.namespaces {
		if err := q.deleteNamespace(ns, plan.id); err != nil {
			return err
		}
	}
	crds, err := q.crds()
	if err != nil {
		return fmt.Errorf("quickstart --local --delete: %w", err)
	}
	var kept []string
	for _, part := range plan.parts {
		if !part.remove() {
			kept = append(kept, part.name)
			continue
		}
		// The demo is gone now: anything else of the part's groups is not
		// the demo's.
		foreign, err := q.foreignObjects(crds, part.groups, plan.id)
		if err != nil {
			return fmt.Errorf("quickstart --local --delete: %w", err)
		}
		if len(foreign) > 0 {
			q.say(part.name, fmt.Sprintf("kept, with its CRDs: %d object(s) that are not the demo's (%s)",
				len(foreign), strings.Join(firstN(foreign, 3), ", ")))
			kept = append(kept, part.name)
			continue
		}
		if err := q.uninstallPart(part); err != nil {
			return err
		}
		if part.namespace != "" {
			if err := q.deleteNamespace(part.namespace, plan.id); err != nil {
				return err
			}
		}
	}
	if !slices.Contains(kept, "kardinal-promoter") {
		// Its namespace went with it, or the Secret is deleted here.
		if err := q.deleteLocalSCMSecret(plan.id); err != nil {
			return err
		}
	}
	if err := q.deleteNamespace(localGitNS, plan.id); err != nil {
		return err
	}
	if len(kept) > 0 {
		_, _ = fmt.Fprintf(q.w, "Kept: %s.\n", strings.Join(kept, ", "))
	}
	_, _ = fmt.Fprintln(q.w, "To set the demo up again: kardinal quickstart --local")
	return nil
}

// uninstallPart uninstalls kardinal-promoter, Argo CD or kro, with its CRDs.
func (q *quickstart) uninstallPart(part localPart) error {
	run := func(what, name string, args ...string) error {
		if _, err := quickstartRun(q.ctx, name, args...); err != nil {
			return fmt.Errorf("quickstart --local --delete: %s: %w", what, err)
		}
		return nil
	}
	switch {
	case strings.HasPrefix(part.name, "kardinal-promoter"):
		if err := run("uninstall kardinal-promoter", "helm", append([]string{"uninstall", "kardinal-promoter", "--namespace",
			quickstartControllerNS, "--ignore-not-found", "--wait", "--timeout", "300s"}, q.helmFlags()...)...); err != nil {
			return err
		}
	case strings.HasPrefix(part.name, "Argo CD"):
		manifests := "https://raw.githubusercontent.com/argoproj/argo-cd/" + argoCDRelease + "/manifests/install.yaml"
		if err := run("uninstall Argo CD", "kubectl", append([]string{"delete", "-n", "argocd", "-f", manifests,
			"--ignore-not-found", "--wait=false"}, q.kubectlFlags()...)...); err != nil {
			return err
		}
	case strings.HasPrefix(part.name, "kro"):
		if err := run("uninstall kro", "helm", append([]string{"uninstall", "kro", "--namespace", kroNamespace, "--ignore-not-found",
			"--wait", "--timeout", "180s"}, q.helmFlags()...)...); err != nil {
			return err
		}
	}
	if len(part.crds) > 0 {
		if err := run("delete the CRDs of "+part.name, "kubectl", append(append([]string{"delete", "--ignore-not-found", "--wait=false"},
			part.crds...), q.kubectlFlags()...)...); err != nil {
			return err
		}
	}
	q.say(part.name, fmt.Sprintf("uninstalled, with its %d CRDs", len(part.crds)))
	return nil
}

// waitApplicationsGone waits, up to quickstartDeleteWait, for the demo's
// Argo CD Applications, the ones of install id, to go.
func (q *quickstart) waitApplicationsGone(id string) error {
	if !q.served(applicationGVK) {
		return nil
	}
	deadline := time.Now().Add(quickstartDeleteWait)
	for {
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(applicationGVK.GroupVersion().WithKind("ApplicationList"))
		if err := q.c.List(q.ctx, &list, sigs_client.InNamespace("argocd"), sigs_client.MatchingLabels{quickstartIDLabel: id}); err != nil {
			return fmt.Errorf("quickstart --local --delete: list the demo's Applications: %w", HumanError(err))
		}
		left := len(list.Items)
		if left == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("quickstart --local --delete: %d Argo CD Application(s) of the demo still there after %s; is Argo CD running? "+
				"Run kardinal quickstart --local --delete again", left, quickstartDeleteWait)
		}
		select {
		case <-q.ctx.Done():
			return q.ctx.Err()
		case <-time.After(shipPollInterval):
		}
	}
}

// deleteLocalSCMSecret deletes the controller's token Secret when this
// install created it (quickstartMark and the install ID).
func (q *quickstart) deleteLocalSCMSecret(id string) error {
	what := "Secret " + quickstartControllerNS + "/" + quickstartSCMSecret
	var s corev1.Secret
	if err := q.c.Get(q.ctx, types.NamespacedName{Namespace: quickstartControllerNS, Name: quickstartSCMSecret}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("quickstart --local --delete: get %s: %w", what, HumanError(err))
	}
	if s.Labels[quickstartMark] != quickstartMarkValue || s.Labels[quickstartIDLabel] != id {
		q.say(what, "kept (this quickstart did not create it)")
		return nil
	}
	if err := q.c.Delete(q.ctx, &s, sigs_client.Preconditions{UID: ptrUID(s.UID)}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("quickstart --local --delete: delete %s: %w", what, HumanError(err))
	}
	q.say(what, "deleted")
	return nil
}

// deleteNamespace deletes namespace name when it carries install ID id
// (checked again here, on the object it deletes). It does not wait for the
// namespace to finish terminating.
func (q *quickstart) deleteNamespace(name, id string) error {
	what := "Namespace " + name
	var ns corev1.Namespace
	if err := q.c.Get(q.ctx, types.NamespacedName{Name: name}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			q.say(what, "already gone")
			return nil
		}
		return fmt.Errorf("quickstart --local --delete: get %s: %w", what, HumanError(err))
	}
	if ns.Labels[quickstartMark] != quickstartMarkValue || ns.Labels[quickstartIDLabel] != id {
		q.say(what, "kept (this quickstart did not create it)")
		return nil
	}
	if ns.DeletionTimestamp == nil {
		if err := q.c.Delete(q.ctx, &ns, sigs_client.Preconditions{UID: ptrUID(ns.UID)}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("quickstart --local --delete: delete %s: %w", what, HumanError(err))
		}
	}
	q.say(what, "deleted")
	return nil
}
