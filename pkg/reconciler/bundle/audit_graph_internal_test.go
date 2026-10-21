// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// fakeGraphs is a GraphChecker that reads Graphs (graphReader): Get returns
// graphs[name], or err when set, or NotFound.
type fakeGraphs struct {
	graphs map[string]*graph.Graph
	err    error
	asked  []string
}

func (f *fakeGraphs) GraphExists(_ context.Context, _, name string) (bool, error) {
	_, ok := f.graphs[name]
	return ok, f.err
}

func (f *fakeGraphs) Get(_ context.Context, _, name string) (*graph.Graph, error) {
	f.asked = append(f.asked, name)
	if f.err != nil {
		return nil, f.err
	}
	if g, ok := f.graphs[name]; ok {
		return g, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "kro.run", Resource: "graphs"}, name)
}

// existsOnly is a GraphChecker that cannot read a Graph, only say it exists.
type existsOnly struct{}

func (existsOnly) GraphExists(context.Context, string, string) (bool, error) { return true, nil }

// logLines are the JSON lines zerolog wrote to buf, as level -> messages.
func logLines(t *testing.T, buf *bytes.Buffer) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var e struct{ Level, Message string }
		require.NoError(t, json.Unmarshal([]byte(l), &e), l)
		out[e.Level] = append(out[e.Level], e.Message)
	}
	return out
}

// TestAuditGraph_MissingGraphIsNothingToCompare (#2461): a Bundle with no
// Graph under its name, a version a v0.8.1 controller started included, has
// no gate copies to compare: auditGraph returns nil and logs at debug. A
// read that fails for any other reason (Forbidden, a timeout, a reader that
// cannot read Graphs) still warns, since a gate change then goes unrecorded.
func TestAuditGraph_MissingGraphIsNothingToCompare(t *testing.T) {
	t.Parallel()
	ns := "team"
	current := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: ns},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}, Status: kardinalv1alpha1.BundleStatus{GraphRef: "app-v2-graph"}}
	// A version a v0.8.1 controller started: no graphRef, so its name is
	// computed, and no Graph exists under it.
	legacy := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "e2e-testupgrade-fromv081-wztpt", Namespace: ns},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "e2e-testupgrade-fromv081"}}
	found := &graph.Graph{}
	found.Name = "app-v2-graph"
	for _, tc := range []struct {
		name    string
		checker GraphChecker
		b       *kardinalv1alpha1.Bundle
		want    *graph.Graph
		level   string // the level of the one line logged; "" for none
	}{
		{"the Graph is read", &fakeGraphs{graphs: map[string]*graph.Graph{"app-v2-graph": found}}, current, found, ""},
		{"NotFound: nothing to compare", &fakeGraphs{}, current, nil, "debug"},
		{"a v0.8.1-era Bundle with no Graph", &fakeGraphs{}, legacy, nil, "debug"},
		{"Forbidden still warns", &fakeGraphs{err: apierrors.NewForbidden(schema.GroupResource{Group: "kro.run", Resource: "graphs"},
			"app-v2-graph", nil)}, current, nil, "warn"},
		{"a timeout still warns", &fakeGraphs{err: apierrors.NewTimeoutError("get graphs", 1)}, current, nil, "warn"},
		{"a context deadline still warns", &fakeGraphs{err: context.DeadlineExceeded}, current, nil, "warn"},
		{"a reader that cannot read Graphs still warns", existsOnly{}, current, nil, "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := zerolog.New(&buf).Level(zerolog.DebugLevel)
			r := &Reconciler{GraphChecker: tc.checker}
			got := r.auditGraph(context.Background(), log, tc.b)
			assert.Same(t, tc.want, got)
			lines := logLines(t, &buf)
			if tc.level == "" {
				assert.Empty(t, lines)
				return
			}
			total := 0
			for _, m := range lines {
				total += len(m)
			}
			assert.Equal(t, 1, total, "one line: %v", lines)
			require.Len(t, lines[tc.level], 1, "logged at %s: %v", tc.level, lines)
			if tc.level == "warn" {
				assert.Contains(t, lines["warn"][0], "cannot read the Graph's gate copies")
			}
		})
	}
	// The legacy Bundle's Graph is looked up under its computed name.
	f := &fakeGraphs{}
	(&Reconciler{GraphChecker: f}).auditGraph(context.Background(), zerolog.Nop(), legacy)
	assert.Equal(t, []string{graph.GraphNameFrom(legacy.Spec.Pipeline, legacy.Name)}, f.asked)
}
