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

package promotionreceipt

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPruner: receipt retention deletes the receipts older than its max
// age in the namespaces it owns, and nothing with a max age of 0 (the
// default: receipts are evidence).
//
// Covers RECEIPT-RETENTION-01.
func TestPruner(t *testing.T) {
	at := func(name, ns string, age time.Duration) *v1alpha1.PromotionReceipt {
		return &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			CreationTimestamp: metav1.NewTime(now.Add(-age))}}
	}
	mk := func() client.Client {
		return fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(
			at("old", "team-a", 100*24*time.Hour), at("new", "team-a", time.Hour),
			at("old-other-shard", "team-b", 100*24*time.Hour)).Build()
	}
	names := func(c client.Client) []string {
		var l v1alpha1.PromotionReceiptList
		require.NoError(t, c.List(context.Background(), &l))
		var out []string
		for _, r := range l.Items {
			out = append(out, r.Name)
		}
		return out
	}
	c := mk()
	n, err := (&Pruner{Client: c, Now: func() time.Time { return now }}).Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "no max age: every receipt is kept")

	p := &Pruner{Client: c, MaxAge: 90 * 24 * time.Hour, Now: func() time.Time { return now },
		Owns: func(ns string) bool { return ns == "team-a" }}
	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.ElementsMatch(t, []string{"new", "old-other-shard"}, names(c))
	assert.True(t, p.NeedLeaderElection())
}
