// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/randfill"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// TestIdentityChangedFiltersOnlyWhatTheBuildIgnores ties IdentityChanged to
// the build. An AgentIdentity update the predicate filters must leave the
// build as it was: the index and verdicts (actor.Evaluate), and the status
// writes (writeStatuses: none, the identity being as built).
//
// Each case fills every field of a built identity at random, then puts back
// only what the predicate keys on:
//   - the spec, with its generation (the API server bumps the generation on
//     any spec change, the status being a subresource);
//   - the immutable name, uid and creation time, and the kind;
//   - the status fields writeStatuses writes.
//
// So a build that comes to read anything else (a label, an annotation,
// another condition, or a new status field such as v0.14's budget usage,
// docs/design/24-agent-budgets.md section 14.1) fails here until
// IdentityChanged passes that field's changes too.
// Covers ACTOR-INDEX-06.
func TestIdentityChangedFiltersOnlyWhatTheBuildIgnores(t *testing.T) {
	ctx := context.Background()
	// The build's own statuses, as on a cluster after a build.
	built := func(objs ...*v1alpha1.AgentIdentity) []v1alpha1.AgentIdentity {
		t.Helper()
		var patches atomic.Int32
		cobjs := make([]client.Object, len(objs))
		for i, o := range objs {
			cobjs[i] = o
		}
		c := countingClient(t, &patches, cobjs...)
		var list v1alpha1.AgentIdentityList
		require.NoError(t, c.List(ctx, &list))
		require.NoError(t, agentidentity.WriteStatusesForTest(ctx, c, list.Items))
		require.NoError(t, c.List(ctx, &list))
		return list.Items
	}
	ids := built(id("fixer", 0, "alice", sa("ci", "fixer")),
		suspended(id("reviewer", 1, "bob", v1alpha1.AgentSubject{Kind: "User", Name: "oidc:reviewer"})))
	for i := range ids {
		require.NotNil(t, meta.FindStatusCondition(ids[i].Status.Conditions, agentidentity.ConditionReady), "built")
	}
	want := actor.Evaluate(ids, actor.MaxIndexBytes)

	for seed := int64(0); seed < 200; seed++ {
		for i := range ids {
			old := &ids[i]
			next := old.DeepCopy()
			randfill.NewWithSeed(seed).NilChance(0.2).NumElements(1, 3).Fill(next)
			next.TypeMeta = old.TypeMeta
			next.Name, next.Namespace, next.UID, next.CreationTimestamp = old.Name, old.Namespace, old.UID, old.CreationTimestamp
			next.Spec, next.Generation = *old.Spec.DeepCopy(), old.Generation
			next.Status.Ready, next.Status.Reason, next.Status.ObservedGeneration =
				old.Status.Ready, old.Status.Reason, old.Status.ObservedGeneration
			conds := []metav1.Condition{*meta.FindStatusCondition(old.Status.Conditions, agentidentity.ConditionReady)}
			for _, c := range next.Status.Conditions {
				if c.Type != agentidentity.ConditionReady {
					conds = append(conds, c)
				}
			}
			next.Status.Conditions = conds

			require.False(t, agentidentity.IdentityChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: next}),
				"seed %d, %s: a field other than the spec and the owned status passed the predicate: "+
					"put it back above too, and say why in IdentityChanged", seed, old.Name)
			after := append([]v1alpha1.AgentIdentity(nil), ids...)
			after[i] = *next
			require.Equal(t, want, actor.Evaluate(after, actor.MaxIndexBytes),
				"seed %d, %s: the index changed with a field IdentityChanged filters: make the predicate pass it", seed, old.Name)

			// The status writes: none, on a cluster holding next. The
			// resourceVersion, managed fields and deletion time are the
			// API server's (a deletion time bumps the generation).
			stored := next.DeepCopy()
			stored.ResourceVersion, stored.ManagedFields, stored.DeletionTimestamp, stored.DeletionGracePeriodSeconds = "", nil, nil, nil
			var patches atomic.Int32
			others := make([]client.Object, 0, len(ids))
			for j := range ids {
				if j != i {
					others = append(others, ids[j].DeepCopy())
				}
			}
			c := countingClient(t, &patches, append(others, stored)...)
			var list v1alpha1.AgentIdentityList
			require.NoError(t, c.List(ctx, &list))
			require.NoError(t, agentidentity.WriteStatusesForTest(ctx, c, list.Items))
			require.Zero(t, patches.Load(),
				"seed %d, %s: the build rewrote a status IdentityChanged filters a change of: make the predicate pass it", seed, old.Name)
		}
	}
}

func suspended(a *v1alpha1.AgentIdentity) *v1alpha1.AgentIdentity {
	a.Spec.Suspended = true
	return a
}

// TestTheBuildReadsNoFieldIdentityChangedFilters is the keyed half of
// TestIdentityChangedFiltersOnlyWhatTheBuildIgnores, whose random values miss
// a read of one label, annotation, condition or map entry by its key. It
// reads the build's source:
//   - pkg/actor, where actor.Evaluate is, may read no AgentIdentity's labels,
//     annotations, status or other server-written metadata (Source.Index,
//     which reads the AgentIndex's status, is not the build);
//   - evaluate and writeStatuses here may read no metadata of that kind, and
//     only the status fields writeStatuses writes.
//
// A new read fails here: make IdentityChanged pass that field's changes,
// then allow it below.
// Covers ACTOR-INDEX-06.
func TestTheBuildReadsNoFieldIdentityChangedFilters(t *testing.T) {
	metadata := map[string]bool{"Labels": true, "Annotations": true, "GetLabels": true, "GetAnnotations": true,
		"DeletionTimestamp": true, "GetDeletionTimestamp": true, "Finalizers": true, "GetFinalizers": true,
		"OwnerReferences": true, "GetOwnerReferences": true, "ManagedFields": true, "GetManagedFields": true}
	owned := map[string]bool{"Ready": true, "Reason": true, "ObservedGeneration": true, "Conditions": true}
	// The names in writeStatuses that are not an AgentIdentity.
	notIdentity := map[string]bool{
		"c":    true, // the client: c.Status() is its status writer
		"cond": true, // the Ready condition writeStatuses writes
	}
	fset := token.NewFileSet()
	var reads []string
	// check records fn's reads of metadata, and of status fields not in
	// status.
	check := func(fn *ast.FuncDecl, status map[string]bool) {
		ownedRead := map[ast.Expr]bool{} // the X.Status of an X.Status.<owned field>
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && status[sel.Sel.Name] {
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Status" {
					ownedRead[inner] = true
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if base, ok := sel.X.(*ast.Ident); ok && status != nil && notIdentity[base.Name] {
				return true
			}
			if metadata[sel.Sel.Name] || sel.Sel.Name == "Status" && !ownedRead[sel] {
				reads = append(reads, fmt.Sprintf("%s: %s reads .%s", fset.Position(sel.Pos()), fn.Name.Name, sel.Sel.Name))
			}
			return true
		})
	}
	seen := map[string]bool{}
	paths, err := filepath.Glob("../../actor/*.go")
	require.NoError(t, err)
	for _, path := range append(paths, "reconciler.go") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv := ""
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if ident, ok := star.X.(*ast.Ident); ok {
						recv = ident.Name
					}
				}
			}
			switch {
			case path == "reconciler.go" && (fn.Name.Name == "evaluate" || fn.Name.Name == "writeStatuses"):
				check(fn, owned)
			case path == "reconciler.go", recv == "Source" && fn.Name.Name == "Index":
				continue
			default:
				check(fn, nil)
			}
			seen[fn.Name.Name] = true
		}
	}
	for _, fn := range []string{"Evaluate", "evaluate", "writeStatuses"} {
		require.True(t, seen[fn], "%s, the build, was not found to check", fn)
	}
	assert.Empty(t, reads, "the build reads an AgentIdentity field IdentityChanged filters the changes of")
}
