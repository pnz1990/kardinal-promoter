// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentbudget

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// laggingReader answers the AgentIdentity from a snapshot it refreshes only
// when told: a cache that lags the API server.
type laggingReader struct {
	client.Client
	snap *v1alpha1.AgentIdentity
}

func (l *laggingReader) refresh(t *testing.T) {
	t.Helper()
	var id v1alpha1.AgentIdentity
	require.NoError(t, l.Client.Get(context.Background(), types.NamespacedName{Name: "fixer"}, &id))
	l.snap = &id
}

func (l *laggingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if id, ok := obj.(*v1alpha1.AgentIdentity); ok && l.snap != nil {
		l.snap.DeepCopyInto(id)
		return nil
	}
	return l.Client.Get(ctx, key, obj, opts...)
}

// TestPropertyAtMostTheLimitUnderWay is section 19's property test: six
// versions of one agent ask for prod, with maxConcurrent 1. For the first
// half of each run two reconcilers grant at once, as two leaders do across a
// handover, the second reading the AgentIdentity through a cache that lags.
// Each step starts when it finds its grant in the ledger and finishes a few
// ticks later. Whatever the schedule, no two steps are ever under way, and
// every version ships. Removing the optimistic lock from the ledger's write
// makes it fail: the lagging leader's write then replaces a grant a step has
// already started on.
// Covers BUDGET-LEDGER-01.
func TestPropertyAtMostTheLimitUnderWay(t *testing.T) {
	const versions = 6
	for seed := int64(0); seed < 40; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			r := rule("prod", "prod")
			r.MaxConcurrent = 1
			objs := []client.Object{identity(r), pipeline()}
			for i := range versions {
				b := bundle(fmt.Sprintf("v%d", i), fmt.Sprintf("u-%d", i))
				objs = append(objs, b, step(b, "prod", "Pending", t0.Add(time.Duration(i)*time.Second)))
			}
			w := newWorld(t, nil, objs...)
			now := t0
			clock := func() time.Time { return now }
			leader := &Reconciler{Client: w.c, APIReader: w.c, Ledger: ledgerName, NowFn: clock}
			lag := &laggingReader{Client: w.c}
			lag.refresh(t)
			second := &Reconciler{Client: lag, APIReader: w.c, Ledger: ledgerName, NowFn: clock}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "fixer"}}
			ctx := context.Background()

			left := map[string]int{} // steps under way: ticks to go
			finished := map[string]bool{}
			for tick := 0; tick < 400 && len(finished) < versions; tick++ {
				now = now.Add(time.Second)
				switch rng.Intn(4) {
				case 0:
					_, err := leader.Reconcile(ctx, req)
					require.NoError(t, err)
				case 1:
					if tick < 200 {
						_, err := second.Reconcile(ctx, req)
						require.NoError(t, err)
					}
				case 2:
					if rng.Intn(3) == 0 {
						lag.refresh(t)
					}
				}
				// The steps: start on a grant, finish a few ticks later.
				u := w.usage()
				for i := range versions {
					name := fmt.Sprintf("v%d-prod", i)
					if finished[name] {
						continue
					}
					if n, ok := left[name]; ok {
						if n == 0 {
							w.setState(name, "Verified")
							delete(left, name)
							finished[name] = true
						} else {
							left[name] = n - 1
						}
						continue
					}
					if u != nil && hasGrant(u, fmt.Sprintf("u-%d", i)) {
						w.setState(name, "Promoting")
						left[name] = 1 + rng.Intn(4)
					}
				}
				require.LessOrEqual(t, len(left), 1, "tick %d: steps under way at once: %v", tick, left)
			}
			require.Len(t, finished, versions, "every version ships")
		})
	}
}

func hasGrant(u *v1alpha1.AgentUsage, uid string) bool {
	for _, g := range u.Grants {
		if g.BundleUID == uid {
			return true
		}
	}
	return false
}
