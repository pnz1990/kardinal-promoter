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

// TestAuditGraph_MissingGraphIsNothingToCompare (#2461): before this
// reconcile writes the Graph, a Bundle with no Graph under its name has no
// gate copies to compare: auditGraph returns nil and logs at debug. That is a
// version a v0.8.1 controller started, whose graphRef names a Graph in
// experimental.kro.run. After the write (wrote), NotFound means the Graph was
// deleted mid-reconcile, so the gate change goes unrecorded and it warns. A
// read that fails for any other reason (Forbidden, a timeout, a reader that
// cannot read Graphs) warns either way.
func TestAuditGraph_MissingGraphIsNothingToCompare(t *testing.T) {
	t.Parallel()
	ns := "team"
	current := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: ns},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}, Status: kardinalv1alpha1.BundleStatus{GraphRef: "app-v2-graph"}}
	// A version a v0.8.1 controller started: its graphRef names the Graph it
	// rendered in experimental.kro.run, and kro.run has none of that name.
	legacy := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "e2e-testupgrade-fromv081-wztpt", Namespace: ns},
		Spec:   kardinalv1alpha1.BundleSpec{Pipeline: "e2e-testupgrade-fromv081"},
		Status: kardinalv1alpha1.BundleStatus{GraphRef: "e2e-testupgrade-fromv081-e2e-testupgrade-fromv081--tpt"}}
	// No graphRef: the name is computed.
	unnamed := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v3", Namespace: ns},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}}
	found := &graph.Graph{}
	found.Name = "app-v2-graph"
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "kro.run", Resource: "graphs"}, "app-v2-graph", nil)
	for _, tc := range []struct {
		name    string
		checker GraphChecker
		b       *kardinalv1alpha1.Bundle
		wrote   bool
		want    *graph.Graph
		level   string // the level of the one line logged; "" for none
	}{
		{"the Graph is read", &fakeGraphs{graphs: map[string]*graph.Graph{"app-v2-graph": found}}, current, false, found, ""},
		{"the Graph is read after the write", &fakeGraphs{graphs: map[string]*graph.Graph{"app-v2-graph": found}}, current, true, found, ""},
		{"NotFound before the write: nothing to compare", &fakeGraphs{}, current, false, nil, "debug"},
		{"a v0.8.1-era Bundle with no Graph in kro.run", &fakeGraphs{}, legacy, false, nil, "debug"},
		{"a Bundle with no graphRef and no Graph", &fakeGraphs{}, unnamed, false, nil, "debug"},
		{"NotFound after the write: deleted mid-reconcile, warns", &fakeGraphs{}, current, true, nil, "warn"},
		{"Forbidden still warns", &fakeGraphs{err: forbidden}, current, false, nil, "warn"},
		{"Forbidden after the write still warns", &fakeGraphs{err: forbidden}, current, true, nil, "warn"},
		{"a timeout still warns", &fakeGraphs{err: apierrors.NewTimeoutError("get graphs", 1)}, current, false, nil, "warn"},
		{"a context deadline still warns", &fakeGraphs{err: context.DeadlineExceeded}, current, false, nil, "warn"},
		{"a reader that cannot read Graphs still warns", existsOnly{}, current, false, nil, "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := zerolog.New(&buf).Level(zerolog.DebugLevel)
			r := &Reconciler{GraphChecker: tc.checker}
			got := r.auditGraph(context.Background(), log, tc.b, tc.wrote)
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
	// The Graph is looked up under graphRef, else under the computed name.
	for _, b := range []*kardinalv1alpha1.Bundle{legacy, unnamed} {
		f := &fakeGraphs{}
		(&Reconciler{GraphChecker: f}).auditGraph(context.Background(), zerolog.Nop(), b, false)
		want := b.Status.GraphRef
		if want == "" {
			want = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
		}
		assert.Equal(t, []string{want}, f.asked, b.Name)
	}
}
