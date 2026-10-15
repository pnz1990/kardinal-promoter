// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// graphStepNode is a PromotionStep node of the Graph the translator built:
// its template, and the step nodes its template reads (upstream), which kro
// waits for before it creates the step.
type graphStepNode struct {
	id       string
	env      string
	template map[string]interface{}
	upstream []string
}

// nodeRef finds the node IDs a CEL expression of a template reads
// (<id>.status...). The Bundle ref node is not a step.
var nodeRef = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9]*)\.status\.`)

// translateJourney runs the real translator on pipeline and bundle, which
// writes the Bundle's Graph to a fake dynamic client, and returns the
// Graph's PromotionStep nodes by node ID. c must hold the Pipeline's
// namespace objects the translator reads (PolicyGates).
func translateJourney(t *testing.T, c client.Client, pipeline *v1alpha1.Pipeline, bundle *v1alpha1.Bundle) map[string]*graphStepNode {
	t.Helper()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	ctx := zerolog.Nop().WithContext(context.Background())
	name, err := translator.New(gc, graph.NewBuilder(), c, nil, zerolog.Nop()).Translate(ctx, pipeline, bundle)
	require.NoError(t, err, "translate %s", bundle.Name)
	g, err := gc.Get(ctx, bundle.Namespace, name)
	require.NoError(t, err)

	nodes := map[string]*graphStepNode{}
	for _, n := range g.Spec.Nodes {
		if n.Template == nil || n.Template["kind"] != "PromotionStep" {
			continue
		}
		spec, _ := n.Template["spec"].(map[string]interface{})
		env, _ := spec["environment"].(string)
		nodes[n.ID] = &graphStepNode{id: n.ID, env: env, template: n.Template}
	}
	for _, sn := range nodes {
		raw, err := json.Marshal(sn.template)
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, m := range nodeRef.FindAllStringSubmatch(string(raw), -1) {
			if id := m[1]; nodes[id] != nil && id != sn.id && !seen[id] {
				seen[id] = true
				sn.upstream = append(sn.upstream, id)
			}
		}
		sort.Strings(sn.upstream)
	}
	return nodes
}

// materialize is the PromotionStep kro creates from sn's template once
// every upstream step is Verified: spec.bundleName resolves to the Bundle
// and each spec.upstreamStates entry to "Verified". A template with any
// other expression fails the test: this executor resolves only those two.
func materialize(t *testing.T, sn *graphStepNode, bundle *v1alpha1.Bundle) *v1alpha1.PromotionStep {
	t.Helper()
	tmpl := runtime.DeepCopyJSON(sn.template)
	spec := tmpl["spec"].(map[string]interface{})
	spec["bundleName"] = bundle.Name
	if ups, ok := spec["upstreamStates"].([]interface{}); ok {
		for i := range ups {
			ups[i] = "Verified"
		}
	}
	raw, err := json.Marshal(tmpl)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "${", "node %s: an expression this executor does not resolve", sn.id)
	var ps v1alpha1.PromotionStep
	require.NoError(t, json.Unmarshal(raw, &ps))
	ps.Namespace = bundle.Namespace
	return &ps
}

// runGraph plays kro's part for nodes: in rounds, it creates each step
// whose upstream steps are all Verified, then drives the round's steps with
// the PromotionStep reconciler until they are Verified. It returns the
// environments created in each round, in order: the DAG order the Graph
// gives the promotion.
func runGraph(t *testing.T, c client.Client, rec *psrec.Reconciler, nodes map[string]*graphStepNode,
	bundle *v1alpha1.Bundle) [][]string {
	t.Helper()
	ctx := context.Background()
	created := map[string]*v1alpha1.PromotionStep{}
	verified := func(id string) bool {
		ps, ok := created[id]
		if !ok {
			return false
		}
		var now v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ps), &now))
		return now.Status.State == "Verified"
	}
	var rounds [][]string
	for len(created) < len(nodes) {
		var ready []string
		for id, sn := range nodes {
			if created[id] != nil {
				continue
			}
			ok := true
			for _, up := range sn.upstream {
				ok = ok && verified(up)
			}
			if ok {
				ready = append(ready, id)
			}
		}
		require.NotEmpty(t, ready, "no step can start: the Graph is stuck after %v", rounds)
		sort.Strings(ready)
		var envs []string
		for _, id := range ready {
			ps := materialize(t, nodes[id], bundle)
			require.NoError(t, c.Create(ctx, ps), "kro creates %s", ps.Name)
			created[id] = ps
			envs = append(envs, nodes[id].env)
		}
		rounds = append(rounds, envs)
		// kro created the round's steps together; the reconciler drives each
		// to Verified in turn, since the steps of one Bundle share the
		// reconciler's test work directory (WorkDirFn has no environment).
		for _, id := range ready {
			for i := 0; i < 30 && !verified(id); i++ {
				_, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(created[id])})
				require.NoError(t, err, "reconcile %s", created[id].Name)
			}
		}
		for _, id := range ready {
			var ps v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(created[id]), &ps))
			require.Equal(t, "Verified", ps.Status.State, "%s: %s", ps.Name, ps.Status.Message)
		}
	}
	return rounds
}

// joinRounds renders runGraph's rounds as "test > uat > prod-eu,prod-us".
func joinRounds(rounds [][]string) string {
	parts := make([]string, len(rounds))
	for i, r := range rounds {
		parts[i] = strings.Join(r, ",")
	}
	return strings.Join(parts, " > ")
}

// journeyGit is a git client whose clone holds a kustomization for each of
// envs (environments/<env>/kustomization.yaml), and which records the
// kustomization each commit wrote, by environment.
type journeyGit struct {
	envs []string
	mu   sync.Mutex
	// committed is the last committed kustomization of each environment
	// that changed.
	committed map[string]string
}

func (g *journeyGit) Clone(_ context.Context, _, _, dir string, _ scm.GitAuth) error {
	for _, env := range g.envs {
		d := filepath.Join(dir, "environments", env)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		k := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [deployment.yaml]\n"
		if err := os.WriteFile(filepath.Join(d, "kustomization.yaml"), []byte(k), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (g *journeyGit) CloneAt(ctx context.Context, url, _, dir string, auth scm.GitAuth) error {
	return g.Clone(ctx, url, "", dir, auth)
}

func (g *journeyGit) CommitAll(_ context.Context, dir, _, _, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed == nil {
		g.committed = map[string]string{}
	}
	for _, env := range g.envs {
		raw, err := os.ReadFile(filepath.Join(dir, "environments", env, "kustomization.yaml"))
		if err == nil && strings.Contains(string(raw), "images:") {
			g.committed[env] = string(raw)
		}
	}
	return nil
}

func (g *journeyGit) Push(context.Context, string, string, string, scm.GitAuth, bool) error {
	return nil
}

// kustomization is the last kustomization committed for env.
func (g *journeyGit) kustomization(env string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed[env]
}
