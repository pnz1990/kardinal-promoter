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
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Receipt retention defaults: every hour, at most maxDeletesPerRun.
const (
	DefaultRetentionInterval = time.Hour
	maxDeletesPerRun         = 2000
	retentionPage            = 500
)

// Pruner deletes PromotionReceipts older than MaxAge
// (--receipt-retention-max-age). Receipts have no owner, so nothing else
// does. Off by default: a receipt is evidence. The sinks outside the
// cluster keep their copy.
type Pruner struct {
	// Client is uncached and rate limited: a backlog never takes API
	// capacity from the reconcilers.
	Client ctrl.Client
	// Namespace limits the pruner to one namespace; empty is all.
	Namespace string
	// Owns reports whether this controller owns namespace (the shard
	// gate); nil owns every namespace.
	Owns     func(namespace string) bool
	MaxAge   time.Duration
	Interval time.Duration
	// Now overrides the clock in tests.
	Now func() time.Time
}

// NeedLeaderElection: only the leader prunes.
func (p *Pruner) NeedLeaderElection() bool { return true }

// Start runs now and then every Interval until ctx is done.
func (p *Pruner) Start(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}
	log := zerolog.Ctx(ctx).With().Str("runnable", "receipt-retention").Logger()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deleted, err := p.Run(log.WithContext(ctx))
		if err != nil {
			log.Warn().Err(err).Int("deleted", deleted).Msg("receipt retention run failed")
		} else if deleted > 0 {
			log.Info().Int("deleted", deleted).Msg("receipt retention deleted old receipts")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Run deletes the receipts created longer than MaxAge ago, at most
// maxDeletesPerRun, and returns how many it deleted. MaxAge 0 deletes none.
func (p *Pruner) Run(ctx context.Context) (int, error) {
	if p.MaxAge <= 0 {
		return 0, nil
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	cutoff := now.Add(-p.MaxAge)
	deleted := 0
	opts := []ctrl.ListOption{ctrl.Limit(retentionPage)}
	if p.Namespace != "" {
		opts = append(opts, ctrl.InNamespace(p.Namespace))
	}
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("PromotionReceiptList"))
	for {
		if err := p.Client.List(ctx, &list, opts...); err != nil {
			return deleted, fmt.Errorf("list promotion receipts: %w", err)
		}
		for i := range list.Items {
			m := &list.Items[i]
			if !m.CreationTimestamp.Time.Before(cutoff) || (p.Owns != nil && !p.Owns(m.Namespace)) {
				continue
			}
			uid := m.UID
			rc := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace}}
			err := p.Client.Delete(ctx, rc, ctrl.Preconditions{UID: &uid})
			switch {
			case err == nil:
				deleted++
			case apierrors.IsNotFound(err), apierrors.IsConflict(err):
			default:
				return deleted, fmt.Errorf("delete promotion receipt %s/%s: %w", m.Namespace, m.Name, err)
			}
			if deleted >= maxDeletesPerRun {
				return deleted, nil
			}
		}
		if list.Continue == "" {
			return deleted, nil
		}
		opts = append(opts[:1:1], ctrl.Continue(list.Continue))
		if p.Namespace != "" {
			opts = append(opts, ctrl.InNamespace(p.Namespace))
		}
	}
}
