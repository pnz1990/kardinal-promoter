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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
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
	cmd.Flags().BoolVarP(&all, "all-namespaces", "A", false, "Every namespace you may read")
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
	// OnYou is true when you can move it on (the server's approval tally and
	// your Kubernetes rights say so).
	OnYou bool `json:"onYou"`
	// Who may move it on, when it is not you.
	Who []string `json:"who,omitempty"`
	// Next is the command (or, for a PR, its URL) that moves it on.
	Next string `json:"next"`
	// Reason is the server's word for why no one can move it on, when so.
	Reason string `json:"reason,omitempty"`
	// NeedsReason is true when Next also needs --reason <why>.
	NeedsReason bool `json:"needsReason,omitempty"`
	// Since is when the environment entered its state, when known.
	Since *time.Time `json:"since,omitempty"`
}

// inboxOutput is inbox -o json|yaml.
type inboxOutput struct {
	OnYou  []inboxItem `json:"onYou"`
	Others []inboxItem `json:"others"`
	// Skipped are the namespaces (or "namespace kind") inbox could not read.
	Skipped []string `json:"skipped,omitempty"`
}

// canI reports whether the caller may verb resource (with its
// subresource, when given) in ns, from a SelfSubjectAccessReview; a test
// replaces it. An error is "no".
var canI = func(ctx context.Context, c sigs_client.Client, verb, resource, subresource, ns string) bool {
	r := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Group: v1alpha1.GroupVersion.Group, Resource: resource,
			Subresource: subresource, Verb: verb, Namespace: ns},
	}}
	if err := c.Create(ctx, r); err != nil {
		return false
	}
	return r.Status.Allowed
}

// contextNamespace is the kubeconfig context's namespace (default without
// one), which a command runs in without -n; a test replaces it.
var contextNamespace = func() string {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		rules.ExplicitPath = globalKubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if globalContext != "" {
		overrides.CurrentContext = globalContext
	}
	ns, _, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).Namespace()
	if err != nil || ns == "" {
		return "default"
	}
	return ns
}

// inboxRights are what each kind of item needs the caller to be allowed, as
// the API server and the chart's admission policies check them.
var inboxRights = map[string][][3]string{
	"approval": {{"create", "approvals", ""}},
	"gate":     {{"patch", "policygates", ""}, {"update", "policygates", "override"}},
	"hold":     {{"update", "pipelines", ""}, {"update", "pipelines", "hold"}},
	"paused":   {{"patch", "pipelines", ""}, {"update", "pipelines", "pause"}},
}

// inboxFn writes what waits for a person in ns ("" for every namespace).
func inboxFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) error {
	var me *lifecycle.ApproverIdentity
	if id, err := identityOf(ctx, c); err == nil {
		me = &lifecycle.ApproverIdentity{User: id.Username, Groups: id.Groups}
	} else {
		_, _ = fmt.Fprintf(errOut, "%s could not tell who you are (%v): approvals are listed under someone else\n", sty.Warning("note:"), HumanError(err))
	}
	items, skipped, err := inboxItems(ctx, c, ns, me)
	if err != nil {
		return err
	}
	out := inboxOutput{OnYou: []inboxItem{}, Others: []inboxItem{}, Skipped: skipped}
	for _, it := range items {
		if it.OnYou {
			it.Who = nil
			out.OnYou = append(out.OnYou, it)
		} else {
			out.Others = append(out.Others, it)
		}
	}
	if len(skipped) > 0 {
		_, _ = fmt.Fprintf(errOut, "%s skipped what you may not read: %s\n", sty.Warning("note:"), strings.Join(skipped, ", "))
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
		_, _ = fmt.Fprintln(tw, "  PIPELINE\tENV\tVERSION\tWAITING FOR\t"+nextHeader)
		for _, it := range list {
			name := it.Pipeline
			if ns == "" {
				name = it.Namespace + "/" + it.Pipeline
			}
			next := it.Next
			if it.NeedsReason {
				next += " --reason <why>"
			}
			if !it.OnYou {
				next = strings.Join(it.Who, ", ")
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", name, orDash(it.Environment),
				orDash(versionLabel(it.Version, it.Bundle)), it.Waiting, orDash(next))
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

// inboxNamespaces are the namespaces inbox reads for ns: ns itself, or for
// "" (-A, every namespace) "" when a cluster-wide List of Pipelines is
// allowed; else -A fails with the fix-it, as status -A does.
func inboxNamespaces(ctx context.Context, c sigs_client.Client, ns string) ([]string, error) {
	if ns != "" {
		return []string{ns}, nil
	}
	var probe v1alpha1.PipelineList
	err := c.List(ctx, &probe, sigs_client.Limit(1))
	switch {
	case err == nil:
		return []string{""}, nil
	case apierrors.IsForbidden(err):
		return nil, fmt.Errorf("you may not list Pipelines in every namespace: run kardinal inbox -n <namespace>")
	}
	return nil, fmt.Errorf("list pipelines: %w", HumanError(err))
}

// inboxItems is what waits for a person in ns ("" for every namespace),
// oldest first (by Since; unknown last), and what could not be read.
func inboxItems(ctx context.Context, c sigs_client.Client, ns string, me *lifecycle.ApproverIdentity) ([]inboxItem, []string, error) {
	namespaces, err := inboxNamespaces(ctx, c, ns)
	if err != nil {
		return nil, nil, err
	}
	r := &inboxReader{ctx: ctx, c: c, me: me, here: contextNamespace(), rights: map[string]bool{}, bundles: map[string]*v1alpha1.Bundle{}}
	var items []inboxItem
	var skipped []string
	for _, n := range namespaces {
		got, skip, err := r.namespace(n, ns == "" && len(namespaces) > 1)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, got...)
		skipped = append(skipped, skip...)
	}
	slices.SortStableFunc(items, func(a, b inboxItem) int {
		switch {
		case a.Since == nil && b.Since == nil:
			return 0
		case a.Since == nil:
			return 1
		case b.Since == nil:
			return -1
		}
		return a.Since.Compare(*b.Since)
	})
	return items, skipped, nil
}

// inboxReader reads one inbox: the caller, what they may do and the Bundles
// it needed, each asked once.
type inboxReader struct {
	ctx     context.Context
	c       sigs_client.Client
	me      *lifecycle.ApproverIdentity
	here    string
	rights  map[string]bool
	bundles map[string]*v1alpha1.Bundle
}

// may reports whether the caller holds every right kind needs in ns.
func (r *inboxReader) may(kind, ns string) bool {
	key := kind + "/" + ns
	if v, ok := r.rights[key]; ok {
		return v
	}
	ok := true
	for _, right := range inboxRights[kind] {
		ok = ok && canI(r.ctx, r.c, right[0], right[1], right[2], ns)
	}
	r.rights[key] = ok
	return ok
}

// bundle is Bundle name of ns, or nil when it cannot be read.
func (r *inboxReader) bundle(ns, name string) *v1alpha1.Bundle {
	key := ns + "/" + name
	if b, ok := r.bundles[key]; ok {
		return b
	}
	var b v1alpha1.Bundle
	var got *v1alpha1.Bundle
	if err := r.c.Get(r.ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err == nil {
		got = &b
	}
	r.bundles[key] = got
	return got
}

// nsFlag is " -n <ns>" when a command for ns must name it: ns is not the
// kubeconfig context's namespace.
func (r *inboxReader) nsFlag(ns string) string {
	if ns != r.here {
		return " -n " + ns
	}
	return ""
}

// namespace is what waits in ns ("" for a cluster-wide read). forbiddenOK
// skips a namespace whose Pipelines may not be read, instead of failing.
func (r *inboxReader) namespace(ns string, forbiddenOK bool) ([]inboxItem, []string, error) {
	var opts []sigs_client.ListOption
	if ns != "" {
		opts = append(opts, sigs_client.InNamespace(ns))
	}
	var pipelines v1alpha1.PipelineList
	if err := r.c.List(r.ctx, &pipelines, opts...); err != nil {
		if apierrors.IsForbidden(err) && forbiddenOK {
			return nil, []string{ns}, nil
		}
		return nil, nil, fmt.Errorf("list pipelines: %w", HumanError(err))
	}
	var skipped []string
	type envKey struct{ ns, pipeline, env, bundle string }
	// The gate instances of the versions that wait, by label: not the
	// history.
	incoming := map[string][]string{} // namespace → bundles
	pls := pipelines.Items
	envsOf := map[string][]v1alpha1.PipelineEnvironmentStatus{}
	for i := range pls {
		p := &pls[i]
		envs := p.Status.Environments
		if len(envs) == 0 && !p.Spec.Paused {
			// A controller that predates status.environments: project them.
			projected, skip := r.project(p)
			envs, skipped = projected, append(skipped, skip...)
		}
		envsOf[p.Namespace+"/"+p.Name] = envs
		for _, e := range envs {
			if b := waitingBundleOf(e); b != "" && len(e.BlockedBy) > 0 && !slices.Contains(incoming[p.Namespace], b) {
				incoming[p.Namespace] = append(incoming[p.Namespace], b)
			}
		}
	}
	gates := map[envKey][]v1alpha1.PolicyGate{}
	for gns, bundles := range incoming {
		req, err := labels.NewRequirement("kardinal.io/bundle", selection.In, bundles)
		if err != nil {
			continue
		}
		var list v1alpha1.PolicyGateList
		if err := r.c.List(r.ctx, &list, sigs_client.InNamespace(gns), sigs_client.MatchingLabelsSelector{Selector: labels.NewSelector().Add(*req)}); err != nil {
			if apierrors.IsForbidden(err) {
				skipped = append(skipped, gns+" policygates")
				continue
			}
			return nil, nil, fmt.Errorf("list policy gates: %w", HumanError(err))
		}
		for _, g := range list.Items {
			k := envKey{g.Namespace, g.Labels["kardinal.io/pipeline"], g.Labels["kardinal.io/environment"], g.Labels["kardinal.io/bundle"]}
			gates[k] = append(gates[k], g)
		}
	}
	var items []inboxItem
	for i := range pls {
		p := &pls[i]
		if p.Spec.Paused {
			items = append(items, inboxItem{Namespace: p.Namespace, Pipeline: p.Name, Kind: "paused",
				Waiting: "the pipeline is paused", OnYou: r.may("paused", p.Namespace), Who: []string{"someone who may resume it"},
				Next: "kardinal resume " + p.Name + r.nsFlag(p.Namespace)})
			continue
		}
		for _, e := range envsOf[p.Namespace+"/"+p.Name] {
			items = append(items, r.environment(p, e, gates[envKey{p.Namespace, p.Name, e.Name, waitingBundleOf(e)}])...)
		}
	}
	return items, skipped, nil
}

// project is p's environments from its Bundles, steps and gates, as the
// controller computes them, for a controller that does not write
// status.environments; what may not be read is skipped.
func (r *inboxReader) project(p *v1alpha1.Pipeline) ([]v1alpha1.PipelineEnvironmentStatus, []string) {
	sel := []sigs_client.ListOption{sigs_client.InNamespace(p.Namespace), sigs_client.MatchingLabels{"kardinal.io/pipeline": p.Name}}
	var bundles v1alpha1.BundleList
	var steps v1alpha1.PromotionStepList
	var gates v1alpha1.PolicyGateList
	var skipped []string
	var all v1alpha1.BundleList
	if err := r.c.List(r.ctx, &all, sigs_client.InNamespace(p.Namespace)); err != nil {
		return nil, []string{p.Namespace + " bundles"}
	}
	for _, b := range all.Items {
		if b.Spec.Pipeline == p.Name {
			bundles.Items = append(bundles.Items, b)
		}
	}
	if err := r.c.List(r.ctx, &steps, sel...); err != nil {
		skipped = append(skipped, p.Namespace+" promotionsteps")
	}
	if err := r.c.List(r.ctx, &gates, sel...); err != nil {
		skipped = append(skipped, p.Namespace+" policygates")
	}
	return pipelinerec.ProjectEnvironments(p, bundles.Items, steps.Items, gates.Items, nil, time.Time{}), skipped
}

// waitingBundleOf is the Bundle environment e waits with: its incoming one,
// or the one it runs while that is not Verified.
func waitingBundleOf(e v1alpha1.PipelineEnvironmentStatus) string {
	if e.IncomingBundle != "" {
		return e.IncomingBundle
	}
	if e.State != "" && e.State != "Verified" {
		return e.Bundle
	}
	return ""
}

// environment is what environment e of p waits a person for: a hold, an
// approval, a gate that does not open by itself, or a PR to merge. gates are
// the gate instances of e's waiting Bundle.
func (r *inboxReader) environment(p *v1alpha1.Pipeline, e v1alpha1.PipelineEnvironmentStatus, gates []v1alpha1.PolicyGate) []inboxItem {
	bundle, version := waitingBundleOf(e), e.IncomingVersion
	if e.IncomingBundle == "" {
		version = e.Version
	}
	var since *time.Time
	if e.Since != nil {
		t := e.Since.Time
		since = &t
	}
	base := inboxItem{Namespace: p.Namespace, Pipeline: p.Name, Environment: e.Name, Bundle: bundle, Version: version, Since: since}
	nsFlag := r.nsFlag(p.Namespace)
	if h := lifecycle.HoldOf(p, e.Name); h != nil && !h.Expired(time.Now()) {
		it := base
		it.Kind = "hold"
		held := h.Bundle
		if b := r.bundle(p.Namespace, h.Bundle); b != nil {
			held = versionLabel(createdOf(b).Version, h.Bundle)
		}
		it.Waiting = "held on " + held
		if h.CreatedBy != "" {
			it.Waiting += " by " + h.CreatedBy
		}
		if h.Reason != "" {
			it.Waiting += " (" + h.Reason + ")"
		}
		it.OnYou = r.may("hold", p.Namespace)
		it.Who = []string{"someone who may unhold it"}
		it.Next = fmt.Sprintf("kardinal unhold %s %s%s", p.Name, e.Name, nsFlag)
		return []inboxItem{it}
	}
	var out []inboxItem
	for i := range gates {
		g := &gates[i]
		if g.Status.Ready || g.Status.LastEvaluatedAt == nil || !slices.Contains(e.BlockedBy, gateDisplayName(*g)) {
			continue
		}
		it := base
		name := gateDisplayName(*g)
		if g.Spec.Approval != nil {
			creator, unread := "", false
			if b := r.bundle(p.Namespace, bundle); b != nil {
				creator = b.Annotations[lifecycle.AnnotationCreatedBy]
			} else {
				unread = true // its creator is unknown, not absent
			}
			st := lifecycle.ApprovalStandingOf(g, creator, r.me)
			creatorHeld := st.Block == lifecycle.ApprovalNoCreator || st.Block == lifecycle.ApprovalComponentCreator
			if st.Block == lifecycle.ApprovalRejected || (creatorHeld && unread && len(st.RejectedBy) > 0) {
				// Rejected: nobody's approval opens it (as the UI). With the
				// version not read, a known rejection still says so.
				continue
			}
			it.Kind = "approval"
			it.Waiting = fmt.Sprintf("approval at %s · %d of %d", name, st.Approved, st.Required)
			it.Next = fmt.Sprintf("kardinal approve %s %s%s", p.Name, e.Name, nsFlag)
			it.OnYou = st.CanApprove && r.may("approval", p.Namespace)
			it.Who = st.Who
			switch {
			case creatorHeld && unread:
				it.Waiting = fmt.Sprintf("approval at %s · %d of %d (could not read the version %s to see who created it)", name, st.Approved, st.Required, bundle)
				it.Reason = "could not read Bundle " + p.Namespace + "/" + bundle
				it.OnYou = false
			case creatorHeld:
				short := "no verified creator"
				if creator != "" {
					short = "created by " + creator
				}
				it.Waiting = fmt.Sprintf("approval at %s, which no one can give (excludeAuthor, %s)", name, short)
				it.Reason = st.Reason
				it.Who = []string{"no one: ship it again as a person"}
			case st.Block == lifecycle.ApprovalTooMany:
				it.Waiting = fmt.Sprintf("approval at %s, which no one can give (more than %d Approvals)", name, lifecycle.MaxGateApprovals)
				it.Reason = st.Reason
				it.Who = []string{fmt.Sprintf("someone who deletes the stale ones (kubectl get approvals%s)", nsFlag)}
			case st.Block == lifecycle.ApprovalCounted:
				it.Waiting += " (you approved; it needs someone else)"
				it.Who = []string{"someone else who may approve"}
			case st.Block == lifecycle.ApprovalAuthor:
				it.Who = append(it.Who, "someone other than you (you created this version)")
			}
			if len(it.Who) == 0 {
				it.Who = []string{"anyone who may create Approvals"}
			}
		} else {
			if rechecks(g) {
				continue // it opens by itself
			}
			it.Kind = "gate"
			it.Waiting = "gate " + name + ", which does not open by itself"
			it.OnYou = r.may("gate", p.Namespace)
			it.Who = []string{"someone who may override it"}
			it.Next = fmt.Sprintf("kardinal override %s %s --gate %s%s", p.Name, e.Name, name, nsFlag)
			it.NeedsReason = true
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

// inboxEnvSpec is environment env of p's spec, or nil.
func inboxEnvSpec(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentSpec {
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == env {
			return &p.Spec.Environments[i]
		}
	}
	return nil
}

// inboxReference is the detail of inbox for its reference page.
const inboxReference = `inbox lists, per environment, what waits for a person, from each Pipeline's
status.environments, the gate instances of the versions that wait, and its
holds:

  approval  an approval gate that has not its quorum (n of m) and nobody
            rejected
  gate      a gate that does not pass and does not open by itself (its
            expression reads nothing that changes with time; see ship)
  hold      a hold (kardinal hold, rollback --hold)
  paused    the pipeline is paused
  pr        its PR waits for a person to merge it (not pr.merge.auto)

"Waiting on you" is what you can move on, with the command in NEXT: an
approval your approval would count for, by the same tally the server keeps
(allowedUsers, allowedGroups, excludeAuthor, one approval per person), when
you may create Approvals; a gate, a hold or a pause when you hold the rights
the API server and the chart's admission policies check (patch policygates
and update policygates/override; update pipelines and pipelines/hold; patch
pipelines and update pipelines/pause), asked with SelfSubjectAccessReviews.
Everything else is "Waiting on someone else", with WHO: the allowed users
and groups, someone other than you when you created the version, or the PR's
reviewers. An approval excludeAuthor cannot enforce (no verified creator, or
a kardinal component created the version) is on no one. A PR is never on
you: the CLI cannot tell who may merge. Gates that open by themselves and
promotions in flight are not listed. The oldest wait comes first.

A command carries -n when its namespace is not the kubeconfig context's. -A
lists every namespace; when you may not list Pipelines cluster-wide it
fails and says to use -n. A namespace whose gates or Bundles it may not
read is noted on stderr.
-o json|yaml prints {onYou, others, skipped}; each item has namespace,
pipeline, environment, bundle, version, kind, waiting, onYou, who, next
(without --reason <why>: needsReason says it needs one) and since.`
