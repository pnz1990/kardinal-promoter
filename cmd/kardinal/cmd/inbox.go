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

	"github.com/spf13/cobra"
	authorizationv1 "k8s.io/api/authorization/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func newInboxCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List what waits for a person: on you first, then on others",
		Long: `List every environment that waits for a person, in the namespace (every
namespace with -A): an approval, a gate that does not open by itself, a hold,
a paused pipeline, a PR to merge. What you can move on comes first, with the
command that does it; then what waits on someone else, and who.`,
		Example: `  kardinal inbox
  kardinal inbox -A
  kardinal inbox -o json`,
		Annotations: map[string]string{ReferenceAnnotation: inboxReference, outputAnnotation: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("inbox: %w", err)
			}
			if all {
				ns = ""
			}
			return inboxFn(contextOf(cmd), cmd.OutOrStdout(), c, ns)
		},
	}
	cmd.Flags().BoolVarP(&all, "all-namespaces", "A", false, "Every namespace")
	return cmd
}

// inboxItem is one thing that waits for a person (inbox -o json).
type inboxItem struct {
	Namespace   string `json:"namespace"`
	Pipeline    string `json:"pipeline"`
	Environment string `json:"environment,omitempty"`
	Bundle      string `json:"bundle,omitempty"`
	Version     string `json:"version,omitempty"`
	// Kind is approval, gate, hold, paused or pr.
	Kind string `json:"kind"`
	// Waiting says what for, as a person reads it.
	Waiting string `json:"waiting"`
	// OnYou is true when you can move it on (the allow-lists and your
	// Kubernetes rights say so).
	OnYou bool `json:"onYou"`
	// Who may move it on, when it is not you (allowed users and groups, the
	// PR's reviewers).
	Who []string `json:"who,omitempty"`
	// Next is the command (or, for a PR, the URL) that moves it on.
	Next string `json:"next"`
	// Since is when the environment entered its state, when known.
	Since *time.Time `json:"since,omitempty"`
}

// inboxOutput is inbox -o json|yaml.
type inboxOutput struct {
	OnYou  []inboxItem `json:"onYou"`
	Others []inboxItem `json:"others"`
}

// canI reports whether the caller may verb resource in ns, from a
// SelfSubjectAccessReview; a test replaces it. An error is "no".
var canI = func(ctx context.Context, c sigs_client.Client, verb, resource, ns string) bool {
	r := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Group: v1alpha1.GroupVersion.Group, Resource: resource, Verb: verb, Namespace: ns},
	}}
	if err := c.Create(ctx, r); err != nil {
		return false
	}
	return r.Status.Allowed
}

// inboxFn writes what waits for a person in ns ("" for every namespace).
func inboxFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) error {
	items, err := inboxItems(ctx, c, ns)
	if err != nil {
		return err
	}
	out := inboxOutput{OnYou: []inboxItem{}, Others: []inboxItem{}}
	for _, it := range items {
		if it.OnYou {
			it.Who = nil
			out.OnYou = append(out.OnYou, it)
		} else {
			out.Others = append(out.Others, it)
		}
	}
	if structured() {
		return writeStructured(w, out)
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "Nothing waits for a person.")
		return err
	}
	section := func(title string, list []inboxItem, nextHeader string) error {
		if len(list) == 0 {
			return nil
		}
		if _, err := fmt.Fprintf(w, "%s (%d):\n", title, len(list)); err != nil {
			return err
		}
		tw := style.NewTable(w, 3)
		header := "  PIPELINE\tENV\tVERSION\tWAITING FOR\t" + nextHeader
		_, _ = fmt.Fprintln(tw, header)
		for _, it := range list {
			name := it.Pipeline
			if ns == "" {
				name = it.Namespace + "/" + it.Pipeline
			}
			next := it.Next
			if !it.OnYou {
				next = strings.Join(it.Who, ", ")
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", name, orDash(it.Environment),
				orDash(versionLabel(it.Version, it.Bundle)), it.Waiting, next)
		}
		return tw.Flush()
	}
	if err := section("Waiting on you", out.OnYou, "NEXT"); err != nil {
		return err
	}
	if len(out.OnYou) > 0 && len(out.Others) > 0 {
		_, _ = fmt.Fprintln(w)
	}
	return section("Waiting on someone else", out.Others, "WHO")
}

// inboxItems is what waits for a person in ns ("" for every namespace), in
// namespace, pipeline and promotion order.
func inboxItems(ctx context.Context, c sigs_client.Client, ns string) ([]inboxItem, error) {
	var opts []sigs_client.ListOption
	if ns != "" {
		opts = append(opts, sigs_client.InNamespace(ns))
	}
	var pipelines v1alpha1.PipelineList
	if err := c.List(ctx, &pipelines, opts...); err != nil {
		return nil, fmt.Errorf("list pipelines: %w", HumanError(err))
	}
	var bundles v1alpha1.BundleList
	var steps v1alpha1.PromotionStepList
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &bundles, opts...); err != nil {
		return nil, fmt.Errorf("list bundles: %w", HumanError(err))
	}
	if err := c.List(ctx, &steps, opts...); err != nil {
		return nil, fmt.Errorf("list promotion steps: %w", HumanError(err))
	}
	if err := c.List(ctx, &gates, opts...); err != nil {
		return nil, fmt.Errorf("list policy gates: %w", HumanError(err))
	}
	var me *Identity
	if id, err := identityOf(ctx, c); err == nil {
		me = &id
	}
	byName := map[string]*v1alpha1.Bundle{}
	for i := range bundles.Items {
		b := &bundles.Items[i]
		byName[b.Namespace+"/"+b.Name] = b
	}
	// Rights are asked once per namespace and verb.
	type right struct{ verb, resource, ns string }
	rights := map[right]bool{}
	may := func(verb, resource, ns string) bool {
		k := right{verb, resource, ns}
		if v, ok := rights[k]; ok {
			return v
		}
		rights[k] = canI(ctx, c, verb, resource, ns)
		return rights[k]
	}

	pls := pipelines.Items
	slices.SortFunc(pls, func(a, b v1alpha1.Pipeline) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	var items []inboxItem
	for i := range pls {
		p := &pls[i]
		if p.Spec.Paused {
			items = append(items, inboxItem{Namespace: p.Namespace, Pipeline: p.Name, Kind: "paused",
				Waiting: "the pipeline is paused", OnYou: may("update", "pipelines", p.Namespace), Who: []string{"someone who may resume it"},
				Next: "kardinal resume " + p.Name + inboxNS(p.Namespace, ns)})
			continue
		}
		envs := p.Status.Environments
		if len(envs) == 0 {
			var own []v1alpha1.PromotionStep
			for _, s := range steps.Items {
				if s.Namespace == p.Namespace && s.Spec.PipelineName == p.Name {
					own = append(own, s)
				}
			}
			var mine []v1alpha1.Bundle
			for _, b := range bundles.Items {
				if b.Namespace == p.Namespace && b.Spec.Pipeline == p.Name {
					mine = append(mine, b)
				}
			}
			envs = pipelinerec.ProjectEnvironments(p, mine, own, gates.Items, nil, time.Time{})
		}
		for _, e := range envs {
			items = append(items, inboxEnvironment(p, e, gates.Items, byName, me, ns, may)...)
		}
	}
	return items, nil
}

// inboxNS is " -n <ns>" when the command must name the namespace (inbox -A,
// or another namespace than the current one), else "".
func inboxNS(itemNS, listNS string) string {
	if listNS == "" && itemNS != "" {
		return " -n " + itemNS
	}
	return ""
}

// inboxEnvironment is what environment e of p waits a person for: a hold, an
// approval, a gate that does not open by itself, or a PR to merge.
func inboxEnvironment(p *v1alpha1.Pipeline, e v1alpha1.PipelineEnvironmentStatus, gates []v1alpha1.PolicyGate,
	byName map[string]*v1alpha1.Bundle, me *Identity, listNS string, may func(verb, resource, ns string) bool) []inboxItem {
	incoming, version := e.IncomingBundle, e.IncomingVersion
	if incoming == "" && e.State != "" && e.State != "Verified" {
		incoming, version = e.Bundle, e.Version
	}
	var since *time.Time
	if e.Since != nil {
		t := e.Since.Time
		since = &t
	}
	base := inboxItem{Namespace: p.Namespace, Pipeline: p.Name, Environment: e.Name, Bundle: incoming, Version: version, Since: since}
	nsFlag := inboxNS(p.Namespace, listNS)
	if h := lifecycle.HoldOf(p, e.Name); h != nil && !h.Expired(time.Now()) {
		it := base
		it.Kind = "hold"
		held := versionLabel(versionOf(bundlesOf(byName, p.Namespace), h.Bundle), h.Bundle)
		it.Waiting = "held on " + held
		if h.CreatedBy != "" {
			it.Waiting += " by " + h.CreatedBy
		}
		if h.Reason != "" {
			it.Waiting += " (" + h.Reason + ")"
		}
		it.OnYou = may("update", "pipelines", p.Namespace)
		it.Who = []string{"someone who may unhold it"}
		it.Next = fmt.Sprintf("kardinal unhold %s %s%s", p.Name, e.Name, nsFlag)
		return []inboxItem{it}
	}
	var out []inboxItem
	for i := range gates {
		g := &gates[i]
		if g.Namespace != p.Namespace || g.Labels["kardinal.io/pipeline"] != p.Name || g.Labels["kardinal.io/environment"] != e.Name ||
			g.Labels["kardinal.io/bundle"] != incoming || incoming == "" || g.Status.Ready || g.Status.LastEvaluatedAt == nil ||
			!slices.Contains(e.BlockedBy, gateDisplayName(*g)) {
			continue
		}
		it := base
		name := gateDisplayName(*g)
		if a := g.Spec.Approval; a != nil {
			it.Kind = "approval"
			approved := 0
			for _, d := range g.Status.Approvals {
				if d.Counted && d.Decision == "approve" {
					approved++
				}
			}
			it.Waiting = fmt.Sprintf("approval at %s · %d of %d", name, approved, max(a.Required, 1))
			creator := ""
			if b := byName[p.Namespace+"/"+incoming]; b != nil {
				creator = b.Annotations[lifecycle.AnnotationCreatedBy]
			}
			allowed, who := inboxMayApprove(a, me, creator)
			it.OnYou = allowed && may("create", "approvals", p.Namespace)
			it.Who = who
			it.Next = fmt.Sprintf("kardinal approve %s %s%s", p.Name, e.Name, nsFlag)
		} else {
			if rechecks(g) {
				continue // it opens by itself
			}
			it.Kind = "gate"
			it.Waiting = "gate " + name + ", which does not open by itself"
			it.OnYou = may("update", "policygates", p.Namespace)
			it.Who = []string{"someone who may override it"}
			it.Next = fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>%s", p.Name, e.Name, name, nsFlag)
		}
		out = append(out, it)
	}
	if len(out) > 0 {
		return out
	}
	if e.State == "WaitingForMerge" {
		if spec := inboxEnvSpec(p, e.Name); spec != nil && spec.PR != nil && spec.PR.Merge != nil && spec.PR.Merge.Auto {
			return nil // the SCM merges it
		}
		it := base
		it.Kind = "pr"
		it.Waiting = "its PR to be merged"
		it.Who = []string{"the PR's reviewers"}
		it.Next = e.PRURL
		if it.Next == "" {
			it.Next = fmt.Sprintf("kardinal why %s %s%s", p.Name, e.Name, nsFlag)
		}
		return []inboxItem{it}
	}
	return nil
}

// inboxMayApprove reports whether the caller may give approval policy a's
// approval (in allowedUsers or allowedGroups, or both empty; with
// excludeAuthor, not the version's creator), and who may otherwise.
func inboxMayApprove(a *v1alpha1.GateApprovalPolicy, me *Identity, creator string) (bool, []string) {
	var who []string
	who = append(who, a.AllowedUsers...)
	for _, g := range a.AllowedGroups {
		who = append(who, "group "+g)
	}
	if me == nil {
		return false, who
	}
	if a.ExcludeAuthor && creator != "" && creator == me.Username {
		return false, append(who, "someone other than you (you created this version)")
	}
	if len(a.AllowedUsers) == 0 && len(a.AllowedGroups) == 0 {
		return true, nil
	}
	if slices.Contains(a.AllowedUsers, me.Username) {
		return true, nil
	}
	for _, g := range me.Groups {
		if slices.Contains(a.AllowedGroups, g) {
			return true, nil
		}
	}
	return false, who
}

// inboxEnvSpec is environment env of p's spec, or nil.
func inboxEnvSpec(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentSpec {
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == env {
			return &p.Spec.Environments[i]
		}
	}
	return nil
}

// bundlesOf is byName (namespace/name) restricted to ns, keyed by name.
func bundlesOf(byName map[string]*v1alpha1.Bundle, ns string) map[string]*v1alpha1.Bundle {
	out := map[string]*v1alpha1.Bundle{}
	for k, b := range byName {
		if strings.HasPrefix(k, ns+"/") {
			out[b.Name] = b
		}
	}
	return out
}

// inboxReference is the detail of inbox for its reference page.
const inboxReference = `inbox lists, per environment, what waits for a person, from each Pipeline's
status.environments, its gate instances and holds:

  approval  an approval gate that has not its quorum (n of m)
  gate      a gate that does not pass and does not open by itself (its
            expression reads nothing that changes with time; see ship)
  hold      a hold (kardinal hold, rollback --hold)
  paused    the pipeline is paused
  pr        its PR waits for a person to merge it (not pr.merge.auto)

"Waiting on you" is what you can move on: an approval when you are in its
allowedUsers or allowedGroups (or both are empty) and did not create the
version under excludeAuthor, and you may create approvals; a gate, a hold
or a pause when your Kubernetes rights let you override, unhold or resume
(a SelfSubjectAccessReview). Its NEXT column is the command. Everything else
is "Waiting on someone else", with WHO: the allowed users and groups, or the
PR's reviewers. A PR is never on you: the CLI cannot tell who may merge.
Gates that open by themselves (a soak, a schedule) and promotions in flight
are not listed: nothing is waiting for a person.

-A lists every namespace (with -n in the commands). -o json|yaml prints
{onYou: [...], others: [...]}, each item with namespace, pipeline,
environment, bundle, version, kind, waiting, onYou, who, next and since.`
