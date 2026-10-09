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

// Package auditretention deletes old AuditEvents. AuditEvents have no owner
// (they outlive the Bundles and steps they record), so without retention
// they accumulate in etcd for ever. Pruner is a leader-only manager Runnable
// that, every Interval, deletes the records older than MaxAge and, per
// Pipeline, all but the MaxPerPipeline newest. It is housekeeping, like the
// Pipeline's Bundle historyLimit: no promotion decision reads its result.
package auditretention

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// Defaults of the retention flags (--audit-retention-*).
const (
	DefaultMaxAge         = 90 * 24 * time.Hour
	DefaultMaxPerPipeline = 1000
	DefaultInterval       = 10 * time.Minute
)

const (
	// listPage is how many AuditEvents one list call returns.
	listPage = 500
	// maxDeletesPerRun bounds one run, so a first run on a large backlog
	// does not hold the API server; the rest goes at the next runs.
	maxDeletesPerRun = 2000
	labelPipeline    = "kardinal.io/pipeline"
)

// Pruner deletes AuditEvents past their retention.
type Pruner struct {
	// Client deletes AuditEvents.
	Client client.Client
	// APIReader lists AuditEvents page by page from the API server, so
	// retention starts no AuditEvent informer.
	APIReader client.Reader
	// Namespace limits the pruner to one namespace (--watch-namespace);
	// empty is every namespace.
	Namespace string
	// MaxAge deletes records whose spec.timestamp is older; 0 keeps any age.
	MaxAge time.Duration
	// MaxPerPipeline keeps the newest records of each Pipeline (namespace
	// and kardinal.io/pipeline label); 0 keeps any number.
	MaxPerPipeline int
	// Interval between runs; DefaultInterval when zero.
	Interval time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// NeedLeaderElection makes the Pruner run on the leader only.
func (p *Pruner) NeedLeaderElection() bool { return true }

// Start runs now and then every Interval until ctx is done. A failed run is
// logged and retried at the next tick.
func (p *Pruner) Start(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	log := zerolog.Ctx(ctx).With().Str("runnable", "auditevent-retention").Logger()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deleted, err := p.Run(log.WithContext(ctx))
		if err != nil {
			log.Warn().Err(err).Int("deleted", deleted).Msg("AuditEvent retention run failed")
		} else if deleted > 0 {
			log.Info().Int("deleted", deleted).Msg("AuditEvent retention deleted old records")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// record is what retention keeps of one AuditEvent: enough to order it
// (lifecycle.CompareAuditEvents) and delete it.
type record struct {
	ae v1alpha1.AuditEvent
}

// Run prunes once and returns how many records it deleted.
func (p *Pruner) Run(ctx context.Context) (int, error) {
	if p.MaxAge <= 0 && p.MaxPerPipeline <= 0 {
		return 0, nil
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	groups := map[string][]record{}
	opts := []client.ListOption{client.Limit(listPage)}
	if p.Namespace != "" {
		opts = append(opts, client.InNamespace(p.Namespace))
	}
	for cont := ""; ; {
		var list v1alpha1.AuditEventList
		if err := p.APIReader.List(ctx, &list, append(opts, client.Continue(cont))...); err != nil {
			return 0, fmt.Errorf("list auditevents: %w", err)
		}
		for i := range list.Items {
			ae := &list.Items[i]
			key := ae.Namespace + "/" + ae.Labels[labelPipeline]
			groups[key] = append(groups[key], record{ae: trimmed(ae)})
		}
		if cont = list.Continue; cont == "" {
			break
		}
	}

	var doomed []record
	for _, recs := range groups {
		sort.Slice(recs, func(i, j int) bool { return lifecycle.CompareAuditEvents(&recs[i].ae, &recs[j].ae) > 0 })
		for i, r := range recs {
			tooMany := p.MaxPerPipeline > 0 && i >= p.MaxPerPipeline
			tooOld := p.MaxAge > 0 && r.ae.Spec.Timestamp.Time.Before(now.Add(-p.MaxAge))
			if tooMany || tooOld {
				doomed = append(doomed, r)
			}
		}
	}
	// Oldest first, so a capped run removes the oldest records.
	sort.Slice(doomed, func(i, j int) bool { return lifecycle.CompareAuditEvents(&doomed[i].ae, &doomed[j].ae) < 0 })
	deleted := 0
	for _, r := range doomed {
		if deleted >= maxDeletesPerRun || ctx.Err() != nil {
			break
		}
		uid := r.ae.UID
		err := p.Client.Delete(ctx, &r.ae, client.Preconditions{UID: &uid})
		switch {
		case err == nil:
			deleted++
			observability.AuditEventsPrunedTotal.Inc()
		case apierrors.IsNotFound(err), apierrors.IsConflict(err):
			// Gone already, or replaced by a new record of the same name.
		default:
			return deleted, fmt.Errorf("delete auditevent %s/%s: %w", r.ae.Namespace, r.ae.Name, err)
		}
	}
	return deleted, nil
}

// trimmed is ae with only what ordering and deletion need.
func trimmed(ae *v1alpha1.AuditEvent) v1alpha1.AuditEvent {
	out := v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Name: ae.Name, Namespace: ae.Namespace, UID: ae.UID},
		Spec:       v1alpha1.AuditEventSpec{Timestamp: ae.Spec.Timestamp},
	}
	if v, ok := ae.Annotations[lifecycle.AnnotationCreatedAt]; ok {
		out.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: v}
	}
	return out
}
