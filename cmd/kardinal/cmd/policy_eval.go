// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

// policy_eval.go runs the controller's PolicyGate reconciler inside the CLI, so
// `policy simulate`, `policy test` and `validate` evaluate a gate with exactly
// the CEL environment and context the controller uses. Nothing here talks to
// CEL directly.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// Reason prefixes written by the PolicyGate reconciler: the template syntax
// check (reconcileTemplate) and instance evaluation (evaluate).
// TestGateEvaluator_ReasonPrefixes pins them.
const (
	celSyntaxErrorPrefix = "CEL syntax error"
	celSyntaxValidPrefix = "valid CEL syntax"
	celEvalErrorPrefix   = "CEL evaluation error"
)

// gateEvaluator evaluates PolicyGate instances with the controller's reconciler
// at a chosen time.
type gateEvaluator struct {
	c   sigs_client.Client
	r   *policygate.Reconciler
	now time.Time
}

func newGateEvaluator(c sigs_client.Client) (*gateEvaluator, error) {
	r, err := policygate.NewReconciler(c)
	if err != nil {
		return nil, fmt.Errorf("new policygate reconciler: %w", err)
	}
	e := &gateEvaluator{c: c, r: r}
	r.NowFn = func() time.Time { return e.now }
	return e, nil
}

// evaluate reconciles the gate instance key at time now and returns the
// status.ready and status.reason the reconciler wrote.
func (e *gateEvaluator) evaluate(ctx context.Context, key types.NamespacedName, now time.Time) (bool, string, error) {
	e.now = now.UTC()
	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		return false, "", fmt.Errorf("evaluate gate %s: %w", key.Name, err)
	}
	var g v1alpha1.PolicyGate
	if err := e.c.Get(ctx, key, &g); err != nil {
		return false, "", fmt.Errorf("read gate %s: %w", key.Name, err)
	}
	return g.Status.Ready, g.Status.Reason, nil
}

// newGateInstance returns a PolicyGate instance for gate, labelled the way the
// Graph builder labels instances so the reconciler evaluates it.
func newGateInstance(gate v1alpha1.PolicyGate, ns, pipeline, bundle, env string) *v1alpha1.PolicyGate {
	inst := &v1alpha1.PolicyGate{}
	inst.Name = gate.Name
	inst.Namespace = ns
	inst.Labels = map[string]string{
		"kardinal.io/pipeline":      pipeline,
		"kardinal.io/bundle":        bundle,
		"kardinal.io/environment":   env,
		"kardinal.io/gate-template": gate.Name,
		"kardinal.io/gate-name":     gate.Name,
	}
	inst.Spec = gate.Spec
	return inst
}

// localGateCheck evaluates a single gate expression with no cluster: an empty
// bundle, no metrics, no upstream history, at time now.
func localGateCheck(ctx context.Context, gate v1alpha1.PolicyGate, env string, now time.Time) (bool, string, error) {
	const ns, bundleName = "default", "local-check"
	bundle := &v1alpha1.Bundle{}
	bundle.Name, bundle.Namespace = bundleName, ns
	bundle.Spec.Type = "image"
	inst := newGateInstance(gate, ns, "", bundleName, env)
	if inst.Name == "" {
		inst.Name = "gate"
	}
	sim := newSimulationClient(rootScheme, nil, bundle, inst)
	ev, err := newGateEvaluator(sim)
	if err != nil {
		return false, "", err
	}
	return ev.evaluate(ctx, sigs_client.ObjectKeyFromObject(inst), now)
}

// celSyntaxCheck compiles expr the way the controller checks a template gate
// and returns the compile error message when it does not compile.
func celSyntaxCheck(ctx context.Context, expr string) (string, bool, error) {
	tmpl := &v1alpha1.PolicyGate{}
	tmpl.Name, tmpl.Namespace = "gate", "default"
	tmpl.Spec.Expression = expr
	ev, err := newGateEvaluator(newSimulationClient(rootScheme, nil, tmpl))
	if err != nil {
		return "", false, err
	}
	_, reason, err := ev.evaluate(ctx, sigs_client.ObjectKeyFromObject(tmpl), time.Time{})
	if err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(reason, celSyntaxErrorPrefix) {
		return "", false, nil
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(reason, celSyntaxErrorPrefix), ":")), true, nil
}

// buildableBundle returns a copy of b that graph.Builder.Build accepts: a
// placeholder image or config commit stands in for the ones b's type needs
// and lacks. Build refuses a Bundle with nothing to promote (#1285), and the
// simulated Bundle of policy simulate and the one of validate carry none. The
// Graph's environments and gates do not depend on the artifacts, only on the
// Pipeline, the gates, the type and the intent.
func buildableBundle(b *v1alpha1.Bundle) *v1alpha1.Bundle {
	out := b.DeepCopy()
	typ := out.Spec.Type
	if (typ == "" || typ == "image" || typ == "mixed") && len(out.Spec.Images) == 0 {
		out.Spec.Images = []v1alpha1.ImageRef{{Repository: "placeholder.invalid/image", Tag: "placeholder"}}
	}
	if (typ == "config" || typ == "mixed") && (out.Spec.ConfigRef == nil || out.Spec.ConfigRef.CommitSHA == "") {
		out.Spec.ConfigRef = &v1alpha1.ConfigRef{CommitSHA: "placeholder"}
	}
	return out
}

// gatesForEnv builds the Graph the controller would build for bundle and returns
// the PolicyGate instances it stamps for env, and the environments promoted
// before env (its upstream path). policyNS is the controller's
// --policy-namespaces list: only skip-permission gates there let a Bundle skip
// an org-gated environment, as in the controller.
func gatesForEnv(pipe *v1alpha1.Pipeline, bundle *v1alpha1.Bundle,
	templates []v1alpha1.PolicyGate, policyNS []string, env string) ([]v1alpha1.PolicyGate, []string, error) {
	// Target env so the Graph holds exactly env and the environments before it.
	targeted := buildableBundle(bundle)
	targeted.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: env}
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: pipe, Bundle: targeted, PolicyGates: templates, PolicyNamespaces: policyNS,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build graph: %w", err)
	}

	var gates []v1alpha1.PolicyGate
	var upstreams []string
	for _, n := range res.Graph.Spec.Nodes {
		kind, _ := n.Template["kind"].(string)
		switch kind {
		case "PromotionStep":
			// Only the labels: the step spec holds ${...} references.
			var step struct {
				Metadata metav1.ObjectMeta `json:"metadata"`
			}
			if err := fromTemplate(n.Template, &step); err != nil {
				return nil, nil, fmt.Errorf("graph node %s: %w", n.ID, err)
			}
			if e := step.Metadata.Labels["kardinal.io/environment"]; e != env && !containsString(upstreams, e) {
				upstreams = append(upstreams, e)
			}
		case "PolicyGate":
			var g v1alpha1.PolicyGate
			if err := fromTemplate(n.Template, &g); err != nil {
				return nil, nil, fmt.Errorf("graph node %s: %w", n.ID, err)
			}
			if g.Labels["kardinal.io/environment"] == env {
				gates = append(gates, g)
			}
		}
	}
	return gates, upstreams, nil
}

func fromTemplate(tmpl map[string]interface{}, obj interface{}) error {
	raw, err := json.Marshal(tmpl)
	if err != nil {
		return fmt.Errorf("marshal template: %w", err)
	}
	if err := json.Unmarshal(raw, obj); err != nil {
		return fmt.Errorf("decode template: %w", err)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// simulationClient lets the controller's reconciler run against a live cluster
// without writing to it. Reads go to the in-memory overlay first and then to the
// cluster; every write lands in the overlay. The cluster is held as a
// client.Reader, so nothing can be written to it.
type simulationClient struct {
	sigs_client.Client // overlay: synthetic Bundle, gate instances, and all writes
	cluster            sigs_client.Reader
}

func newSimulationClient(scheme *runtime.Scheme, cluster sigs_client.Reader, objs ...sigs_client.Object) *simulationClient {
	overlay := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PolicyGate{}).
		Build()
	s := &simulationClient{Client: overlay}
	if cluster != nil {
		s.cluster = &cachedReader{Reader: cluster, cache: map[string]cachedRead{}}
	}
	return s
}

func (s *simulationClient) Get(ctx context.Context, key sigs_client.ObjectKey, obj sigs_client.Object, opts ...sigs_client.GetOption) error {
	err := s.Client.Get(ctx, key, obj, opts...)
	if apierrors.IsNotFound(err) && s.cluster != nil {
		return s.cluster.Get(ctx, key, obj, opts...)
	}
	return err
}

func (s *simulationClient) List(ctx context.Context, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
	if s.cluster == nil {
		return s.Client.List(ctx, list, opts...)
	}
	if err := s.cluster.List(ctx, list, opts...); err != nil {
		return err
	}
	local, ok := list.DeepCopyObject().(sigs_client.ObjectList)
	if !ok {
		return fmt.Errorf("copy %T", list)
	}
	if err := s.Client.List(ctx, local, opts...); err != nil {
		return err
	}
	return overlayList(list, local)
}

// overlayList replaces items of dst with same-named items of overlay and
// appends the rest.
func overlayList(dst, overlay sigs_client.ObjectList) error {
	base, err := apimeta.ExtractList(dst)
	if err != nil {
		return fmt.Errorf("extract list: %w", err)
	}
	extra, err := apimeta.ExtractList(overlay)
	if err != nil {
		return fmt.Errorf("extract overlay list: %w", err)
	}
	index := make(map[string]int, len(base))
	for i, o := range base {
		if a, aerr := apimeta.Accessor(o); aerr == nil {
			index[a.GetNamespace()+"/"+a.GetName()] = i
		}
	}
	for _, o := range extra {
		a, aerr := apimeta.Accessor(o)
		if aerr != nil {
			continue
		}
		if i, ok := index[a.GetNamespace()+"/"+a.GetName()]; ok {
			base[i] = o
		} else {
			base = append(base, o)
		}
	}
	if err := apimeta.SetList(dst, base); err != nil {
		return fmt.Errorf("set list: %w", err)
	}
	return nil
}

// cachedReader memoises cluster reads so repeated evaluations (the next-window
// search re-evaluates a gate up to once per hour for a week) read each object
// or list once.
type cachedReader struct {
	sigs_client.Reader
	mu    sync.Mutex
	cache map[string]cachedRead
}

type cachedRead struct {
	obj runtime.Object
	err error
}

func (r *cachedReader) Get(ctx context.Context, key sigs_client.ObjectKey, obj sigs_client.Object, opts ...sigs_client.GetOption) error {
	return r.read(fmt.Sprintf("get|%T|%s", obj, key), obj, func() error {
		return r.Reader.Get(ctx, key, obj, opts...)
	})
}

func (r *cachedReader) List(ctx context.Context, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
	lo := &sigs_client.ListOptions{}
	lo.ApplyOptions(opts)
	var sel []string
	if lo.LabelSelector != nil {
		sel = append(sel, lo.LabelSelector.String())
	}
	if lo.FieldSelector != nil {
		sel = append(sel, lo.FieldSelector.String())
	}
	key := fmt.Sprintf("list|%T|%s|%s", list, lo.Namespace, strings.Join(sel, ";"))
	return r.read(key, list, func() error {
		return r.Reader.List(ctx, list, opts...)
	})
}

func (r *cachedReader) read(key string, into runtime.Object, fetch func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hit, ok := r.cache[key]; ok {
		if hit.err != nil {
			return hit.err
		}
		reflect.ValueOf(into).Elem().Set(reflect.ValueOf(hit.obj.DeepCopyObject()).Elem())
		return nil
	}
	err := fetch()
	entry := cachedRead{err: err}
	if err == nil {
		entry.obj = into.DeepCopyObject()
	}
	r.cache[key] = entry
	return err
}
