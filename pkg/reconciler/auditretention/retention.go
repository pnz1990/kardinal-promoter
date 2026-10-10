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
// Pipeline, all but the MaxPerPipeline newest, except records created
// within the last Interval. It is on by default (--audit-retention: a full
// etcd quota stops the whole cluster, which is worse than losing old audit
// records), and housekeeping like the Pipeline's Bundle historyLimit: no
// promotion decision reads its result. Records that name no Pipeline (no
// kardinal.io/pipeline label) share one count cap per namespace.
//
// Memory and API load are bounded: records are listed metadata-only in pages
// of listPage, age deletions are decided while streaming, the count cap
// lists one Pipeline's records at a time (label selector) and only for a
// Pipeline the stream counted past the cap, a run deletes at most
// maxDeletesPerRun, and main gives the Pruner its own rate-limited client.
package auditretention

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// QPS and Burst are the request rate of the retention client: a run lists
// and deletes at most 5 requests a second, so the 2000 deletions of a run
// on a backlog take about 7 minutes.
const (
	QPS   = 5
	Burst = 10
)

// Pruner deletes AuditEvents past their retention.
type Pruner struct {
	// Client lists and deletes AuditEvents against the API server: main
	// gives it a client of its own with a low QPS, so retention never
	// starves the reconcilers.
	Client client.Client
	// Namespace limits the pruner to one namespace (--watch-namespace);
	// empty is every namespace.
	Namespace string
	// Owns reports whether this controller owns a namespace now
	// (shard.Gate.Owns under --namespace-shard); nil owns every one. The
	// pruner skips, and never deletes in, a namespace it does not own, so
	// shards neither repeat each other's work nor apply their limits to
	// another shard's records.
	Owns func(namespace string) bool
	// MaxAge deletes records created longer ago; 0 keeps any age.
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

// record is what retention keeps of one AuditEvent.
type record struct {
	name, namespace string
	uid             types.UID
	created         time.Time // metadata.creationTimestamp
	createdAt       string    // the kardinal.io/created-at annotation
}

// newer orders records newest first: by metadata.creationTimestamp (set by
// the API server, so a record cannot claim another time), then within its
// one-second resolution by kardinal.io/created-at, then by name.
func newer(a, b record) bool {
	if !a.created.Equal(b.created) {
		return a.created.After(b.created)
	}
	if a.createdAt != b.createdAt {
		return compareCreatedAt(a, b) > 0
	}
	return a.name > b.name
}

func compareCreatedAt(a, b record) int {
	ea := v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: a.name, Annotations: map[string]string{lifecycle.AnnotationCreatedAt: a.createdAt}}}
	eb := v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: b.name, Annotations: map[string]string{lifecycle.AnnotationCreatedAt: b.createdAt}}}
	if a.createdAt == "" {
		ea.Annotations = nil
	}
	if b.createdAt == "" {
		eb.Annotations = nil
	}
	return lifecycle.CompareAuditEvents(&ea, &eb)
}

func recordOf(m *metav1.PartialObjectMetadata) record {
	return record{name: m.Name, namespace: m.Namespace, uid: m.UID, created: m.CreationTimestamp.Time,
		createdAt: m.Annotations[lifecycle.AnnotationCreatedAt]}
}

// run is one Run's state.
type run struct {
	p       *Pruner
	now     time.Time
	deleted int
}

// Run prunes once and returns how many records it deleted.
func (p *Pruner) Run(ctx context.Context) (int, error) {
	if p.MaxAge <= 0 && p.MaxPerPipeline <= 0 {
		return 0, nil
	}
	r := &run{p: p, now: time.Now()}
	if p.Now != nil {
		r.now = p.Now()
	}
	// Pass 1: stream every record; delete the ones past MaxAge as they come
	// and count the rest per Pipeline.
	counts := map[string]int{} // namespace/pipeline -> records kept by pass 1
	opts := []client.ListOption{}
	if p.Namespace != "" {
		opts = append(opts, client.InNamespace(p.Namespace))
	}
	err := r.list(ctx, opts, func(rec record, pipeline string) error {
		if r.tooOld(rec) {
			return r.delete(ctx, rec)
		}
		counts[rec.namespace+"/"+pipeline]++
		return nil
	})
	if err != nil || p.MaxPerPipeline <= 0 {
		return r.deleted, err
	}
	// Pass 2: the Pipelines past the count cap, one at a time, oldest-first
	// deletion of what is past the newest MaxPerPipeline.
	keys := make([]string, 0, len(counts))
	for k, n := range counts {
		if n > p.MaxPerPipeline {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if r.capped() {
			break
		}
		if err := r.prunePipeline(ctx, k); err != nil {
			return r.deleted, err
		}
	}
	return r.deleted, nil
}

// tooOld reports whether rec is past MaxAge (by metadata.creationTimestamp,
// which the API server sets, so a record created in the future cannot be
// before the cutoff either).
func (r *run) tooOld(rec record) bool {
	return r.p.MaxAge > 0 && rec.created.Before(r.now.Add(-r.p.MaxAge))
}

// inGrace reports whether rec was created less than one Interval ago. The
// count cap never deletes such a record: a burst of records, such as a
// writer's audit outbox (status.pendingAuditEvents) flushed after an etcd
// outage, each stays at least one Interval, so an exporter polling that
// often sees every one (#1552).
func (r *run) inGrace(rec record) bool {
	interval := r.p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	return rec.created.After(r.now.Add(-interval))
}

func (r *run) capped() bool { return r.deleted >= maxDeletesPerRun }

// list streams the records matching opts, page by page, metadata only, to
// fn. A continue token that expired (410 Gone) ends the stream: the next
// run starts over.
func (r *run) list(ctx context.Context, opts []client.ListOption, fn func(rec record, pipeline string) error) error {
	for cont := ""; ; {
		if r.capped() || ctx.Err() != nil {
			return nil
		}
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("AuditEventList"))
		err := r.p.Client.List(ctx, list, append(opts, client.Limit(listPage), client.Continue(cont))...)
		if apierrors.IsResourceExpired(err) {
			zerolog.Ctx(ctx).Info().Msg("AuditEvent retention: list continue token expired; the next run starts over")
			return nil
		}
		if err != nil {
			return fmt.Errorf("list auditevents: %w", err)
		}
		for i := range list.Items {
			m := &list.Items[i]
			if !r.owns(m.Namespace) {
				continue
			}
			if err := fn(recordOf(m), m.Labels[labelPipeline]); err != nil {
				return err
			}
			if r.capped() {
				return nil
			}
		}
		if cont = list.Continue; cont == "" {
			return nil
		}
	}
}

// prunePipeline lists one Pipeline's records (namespace/pipeline key) and
// deletes, oldest first, all but the newest MaxPerPipeline.
func (r *run) prunePipeline(ctx context.Context, key string) error {
	ns, pipeline := key, ""
	for i := range key {
		if key[i] == '/' {
			ns, pipeline = key[:i], key[i+1:]
			break
		}
	}
	opts := []client.ListOption{client.InNamespace(ns)}
	if pipeline != "" {
		opts = append(opts, client.MatchingLabels{labelPipeline: pipeline})
	}
	var recs []record
	err := r.list(ctx, opts, func(rec record, p string) error {
		if p == pipeline && !r.tooOld(rec) {
			recs = append(recs, rec)
		}
		return nil
	})
	if err != nil || len(recs) <= r.p.MaxPerPipeline {
		return err
	}
	sort.Slice(recs, func(i, j int) bool { return newer(recs[i], recs[j]) })
	excess := recs[r.p.MaxPerPipeline:]
	for i := len(excess) - 1; i >= 0 && !r.capped(); i-- { // oldest first
		if r.inGrace(excess[i]) {
			// The rest of excess is newer still (sorted by creation).
			break
		}
		if err := r.delete(ctx, excess[i]); err != nil {
			return err
		}
	}
	return nil
}

// owns reports whether the pruner may act in namespace.
func (r *run) owns(namespace string) bool { return r.p.Owns == nil || r.p.Owns(namespace) }

func (r *run) delete(ctx context.Context, rec record) error {
	// Ownership can move to another shard during a run: check again.
	if !r.owns(rec.namespace) {
		return nil
	}
	uid := rec.uid
	ae := &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: rec.name, Namespace: rec.namespace}}
	err := r.p.Client.Delete(ctx, ae, client.Preconditions{UID: &uid})
	switch {
	case err == nil:
		r.deleted++
		observability.AuditEventsPrunedTotal.Inc()
		return nil
	case apierrors.IsNotFound(err), apierrors.IsConflict(err):
		// Gone already, or replaced by a new record of the same name.
		return nil
	}
	return fmt.Errorf("delete auditevent %s/%s: %w", rec.namespace, rec.name, err)
}
