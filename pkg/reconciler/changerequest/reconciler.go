// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package changerequest implements the ChangeRequest reconciler
// (docs/design/23-change-requests.md): for each per-promotion instance the
// Graph creates, it opens a ticket in the ticketing system (mode open) or
// tracks one whose key it reads from the promotion (mode link), reads the
// ticket's state every pollInterval, and writes it to the instance's status.
// A PolicyGate reads that status as changeRequests.<template name>. It is
// the only caller of the providers, and it writes only its own status: the
// clock a gate needs (validUntil, timedOut) is written here, not computed in
// the gate.
package changerequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/fairqueue"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/slots"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

const (
	// DefaultPollInterval and MinPollInterval bound spec.pollInterval.
	DefaultPollInterval = time.Minute
	MinPollInterval     = 30 * time.Second
	// DefaultTimeout is spec.timeout when unset.
	DefaultTimeout = 24 * time.Hour
	// staleAfterPolls: a reading is valid for two poll intervals (one missed
	// read of margin); a gate evaluated after status.validUntil reads it as
	// stale.
	staleAfterPolls = 2
	// refusedRetry is the least wait after the provider refused the
	// credential or the egress guard refused the address: no retry can fix
	// either before someone changes something, so no hot loop.
	refusedRetry = 5 * time.Minute
	// CreateGrace is how long after a create was attempted (status.pendingCreate)
	// the reconciler only searches for its ticket before it creates again:
	// a provider's search can lag behind its create (Jira's index), and
	// creating at once would open a second ticket.
	CreateGrace = 2 * time.Minute
	// conflictRetry is the requeue after a status write lost to a newer
	// version of the object.
	conflictRetry = time.Second
	// waitingRecheck is when an instance waiting for a slot asks again if
	// the Limiter's wake-up was lost.
	waitingRecheck = 30 * time.Second
	// DefaultGlobalSlots and DefaultNamespaceSlots are the Limiter caps
	// (--change-request-max-inflight, --change-request-max-inflight-per-namespace).
	DefaultGlobalSlots    = 8
	DefaultNamespaceSlots = 1
	// workers is the reconcile worker count; provider calls take at most
	// the Limiter's global slots of them.
	workers   = 8
	wakeQueue = 1024
)

// Condition types of status.conditions.
const (
	CondReady              = "Ready"
	CondStale              = "Stale"
	CondCredentialsInvalid = "CredentialsInvalid"
	CondRefused            = "Refused"
	CondProviderError      = "ProviderError"
)

// Reasons of the Ready condition when there is no reading.
const (
	ReasonTemplate      = "Template"
	ReasonNotAnInstance = "NotAnInstance"
	ReasonSuspended     = "Suspended"
	ReasonVersionFinal  = "VersionFinal"
	ReasonWaitingForKey = "WaitingForKey"
	ReasonWaitingSlot   = "WaitingForSlot"
	ReasonCreating      = "Creating"
	ReasonRead          = "Read"
	ReasonError         = "ProviderError"
	ReasonIgnoredFilter = "ProviderIgnoredFilter"
)

// The AuditEvent actions of a ChangeRequest (spec 23 section 9).
const (
	AuditActionOpened   = "ChangeRequestOpened"
	AuditActionLinked   = "ChangeRequestLinked"
	AuditActionApproved = "ChangeRequestApproved"
	AuditActionRejected = "ChangeRequestRejected"
	auditKind           = "ChangeRequest"
)

// Reconciler opens or links each ChangeRequest instance's ticket and writes
// its state to the instance's status. It is idempotent and safe to re-run
// after a crash: a create is announced in status.pendingCreate (written with
// the optimistic lock) before it is sent, and every attempt searches for the
// ticket by the promotion's correlation id first, so no crash or lost
// response opens a second ticket.
type Reconciler struct {
	client.Client
	// Providers are the ticketing systems by spec.provider.
	Providers map[string]Provider
	// SecretReader reads the credential Secrets, by name, in the
	// ChangeRequest's namespace. Nil means Client.
	SecretReader client.Reader
	// Limiter shares the provider call slots fairly between namespaces. Nil
	// means no limit (tests).
	Limiter *slots.Limiter
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// CorrelationID is the id of the promotion of Bundle bundle (UID uid) into
// env in namespace ns, written into its ticket.
func CorrelationID(ns, bundle, env, uid string) string {
	return fmt.Sprintf("kardinal:%s/%s/%s/%s", ns, bundle, env, uid)
}

// Reconcile processes one ChangeRequest.
//
//  1. Not found: deleted, leave the Limiter's queue.
//  2. spec.perPromotion: a template; condition Ready=False reason Template,
//     no call.
//  3. Without the instance labels (kardinal.io/bundle, environment,
//     bundle-uid): not tracked, no call.
//  4. status.startedAt (first reconcile) and status.timedOut (spec.timeout
//     after it) are written.
//  5. spec.suspend (the Bundle stopped promoting): no call; the last reading
//     goes stale at validUntil.
//  6. Before status.retryAt, or before the next poll, no call.
//  7. A Limiter slot, then: mode link reads the key (an approval field, a
//     Bundle label or annotation) and reads the ticket; mode open reads its
//     ticket, or searches by the correlation id and, finding none, announces
//     the create in status.pendingCreate and creates it.
//  8. A reading writes state, providerState, approvers, observedAt and
//     validUntil; an error writes a condition and retryAt and keeps the
//     last reading, which goes stale.
//  9. The audit records (opened, linked, approved, rejected) go into the
//     outbox in the same write and are flushed after it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, changeRequestsResource, r.reconcile)
}

// changeRequestsResource is the resource objectgone matches a NotFound against.
var changeRequestsResource = kardinalv1alpha1.GroupVersion.WithResource("changerequests").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("changerequest", req.Name).Str("namespace", req.Namespace).Logger()
	var cr kardinalv1alpha1.ChangeRequest
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			r.cancelSlot(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get changerequest: %w", err)
	}
	now := r.now()
	if cr.Spec.PerPromotion {
		r.cancelSlot(req.NamespacedName)
		// A template has no ticket: only its Ready condition is kept (with
		// its transition time, so an unchanged template writes nothing).
		st := kardinalv1alpha1.ChangeRequestStatus{ObservedGeneration: cr.Generation}
		if c := meta.FindStatusCondition(cr.Status.Conditions, CondReady); c != nil {
			st.Conditions = []metav1.Condition{*c}
		}
		setCondition(&st, cr.Generation, CondReady, false, ReasonTemplate,
			"spec.perPromotion is set: this ChangeRequest opens no ticket itself; each Bundle's Graph creates an "+
				"instance per environment whose gates read changeRequests."+cr.Name, now)
		return ctrl.Result{}, r.writeIfChanged(ctx, &cr, st)
	}
	bundle, env, uid := cr.Labels[labelBundle], cr.Labels[labelEnvironment], cr.Labels[graph.LabelBundleUID]
	if bundle == "" || env == "" || uid == "" {
		r.cancelSlot(req.NamespacedName)
		st := cr.Status
		setCondition(&st, cr.Generation, CondReady, false, ReasonNotAnInstance,
			"not a per-promotion instance: only the ChangeRequests a promotion Graph makes from a template "+
				"(spec.perPromotion) open or track tickets", now)
		return ctrl.Result{}, r.writeIfChanged(ctx, &cr, st)
	}

	poll, timeout := pollInterval(cr.Spec.PollInterval), parseTimeout(cr.Spec.Timeout)
	st := *cr.Status.DeepCopy()
	st.CorrelationID = CorrelationID(cr.Namespace, bundle, env, uid)
	if st.StartedAt == nil {
		t := metav1.NewTime(now)
		st.StartedAt = &t
	}
	st.TimedOut = timeout > 0 && !now.Before(st.StartedAt.Add(timeout))
	staleNow := st.ValidUntil == nil || !now.Before(st.ValidUntil.Time)
	setCondition(&st, cr.Generation, CondStale, staleNow && st.ObservedAt != nil, "Stale",
		"no reading of the ticket since validUntil", now)

	if cr.Spec.Suspend {
		r.cancelSlot(req.NamespacedName)
		setCondition(&st, cr.Generation, CondReady, false, ReasonSuspended,
			"spec.suspend is set (the Bundle stopped promoting): the ticket is no longer read", now)
		return ctrl.Result{}, r.writeIfChanged(ctx, &cr, st)
	}
	if why := r.versionFinal(ctx, &cr, bundle, uid); why != "" {
		r.cancelSlot(req.NamespacedName)
		setCondition(&st, cr.Generation, CondReady, false, ReasonVersionFinal,
			why+": nothing waits on this change request, so the ticket is no longer read", now)
		return ctrl.Result{}, r.writeIfChanged(ctx, &cr, st)
	}

	// Not due: before retryAt (an error or a 429), or within the poll
	// interval of the last reading of an unchanged spec.
	if due := r.nextCall(&cr, &st, poll); due.After(now) {
		if err := r.writeIfChanged(ctx, &cr, st); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: wait(now, due, st, timeout)}, nil
	}

	p := r.Providers[cr.Spec.Provider]
	if p == nil {
		r.fail(&st, &cr, now, poll, fmt.Errorf("provider %q is not available in this controller", cr.Spec.Provider))
		return r.finish(ctx, log, &cr, st, now, timeout)
	}

	if r.Limiter != nil {
		release, ok := r.Limiter.TryAcquire(req.NamespacedName)
		if !ok {
			if st.ObservedAt == nil {
				setCondition(&st, cr.Generation, CondReady, false, ReasonWaitingSlot,
					"other ChangeRequests of this namespace or the cluster are calling their providers; "+
						"this one calls when a slot is free", now)
			}
			if err := r.writeIfChanged(ctx, &cr, st); err != nil {
				log.Warn().Err(err).Msg("changerequest WaitingForSlot status write failed")
			}
			return ctrl.Result{RequeueAfter: min(waitingRecheck, poll)}, nil
		}
		defer release()
	}

	preq := Request{Spec: &cr.Spec, Secret: secretReader(r.secrets(), cr.Namespace), CorrelationID: st.CorrelationID}
	var (
		reading Reading
		err     error
		action  string // the audit action of a ticket opened or linked now
	)
	switch cr.Spec.Mode {
	case kardinalv1alpha1.ChangeRequestModeLink:
		var key, why string
		key, why, err = r.linkKey(ctx, &cr, bundle, env, uid)
		if err == nil && key == "" {
			// No key (yet, or no longer: the approval was revoked): nothing
			// is tracked, and nothing of an earlier ticket is kept.
			setCondition(&st, cr.Generation, CondReady, false, ReasonWaitingForKey, why, now)
			st.State, st.Ready, st.ProviderState = "", false, ""
			st.Ticket, st.Verified, st.Approvers, st.ObservedAt, st.ValidUntil = nil, nil, nil, nil, nil
			st.ObservedGeneration = cr.Generation
			return r.finishAt(ctx, log, &cr, st, now, timeout, now.Add(poll))
		}
		if err == nil {
			if st.Ticket == nil || st.Ticket.Key != key {
				action = AuditActionLinked
			}
			t := kardinalv1alpha1.ChangeRequestTicket{Key: key}
			if st.Ticket != nil && st.Ticket.Key == key {
				t = *st.Ticket
			}
			reading, err = p.Get(ctx, preq, t)
		}
	default:
		if st.Ticket != nil && st.PendingCreate == nil {
			reading, err = p.Get(ctx, preq, *st.Ticket)
			break
		}
		var found bool
		reading, found, err = p.Find(ctx, preq)
		if err != nil || found {
			action = AuditActionOpened // a create whose response was lost
			break
		}
		if pc := st.PendingCreate; pc != nil && now.Before(pc.Since.Add(CreateGrace)) {
			// A create was attempted and the search does not show it yet.
			setCondition(&st, cr.Generation, CondReady, false, ReasonCreating,
				"a create was sent at "+pc.Since.UTC().Format(time.RFC3339)+"; searching for its ticket before creating again", now)
			return r.finishAt(ctx, log, &cr, st, now, timeout, pc.Since.Add(CreateGrace))
		}
		// Announce the create with the optimistic lock: a reconcile from a
		// stale cache, or another replica, loses the write and creates
		// nothing.
		st.PendingCreate = &kardinalv1alpha1.ChangeRequestPendingCreate{CorrelationID: st.CorrelationID, Since: metav1.NewTime(now)}
		setCondition(&st, cr.Generation, CondReady, false, ReasonCreating, "creating the ticket", now)
		if werr := r.write(ctx, &cr, st); werr != nil {
			switch {
			case apierrors.IsNotFound(werr):
				return ctrl.Result{}, nil // deleted: nothing to open
			case apierrors.IsConflict(werr):
				return ctrl.Result{RequeueAfter: conflictRetry}, nil
			}
			return ctrl.Result{}, werr
		}
		st = *cr.Status.DeepCopy()
		reading, err = p.Create(ctx, preq)
		action = AuditActionOpened
	}
	if err != nil {
		log.Warn().Err(err).Str("provider", cr.Spec.Provider).Msg("change request provider call failed")
		r.fail(&st, &cr, now, poll, err)
		return r.finish(ctx, log, &cr, st, now, timeout)
	}
	r.record(ctx, &st, &cr, reading, action, now, poll)
	return r.finish(ctx, log, &cr, st, now, timeout)
}

// nextCall is when the next provider call is due: status.retryAt, else one
// poll interval after the last reading. A spec change, a create in flight or
// no ticket yet make it due at once (retryAt still holds).
func (r *Reconciler) nextCall(cr *kardinalv1alpha1.ChangeRequest, st *kardinalv1alpha1.ChangeRequestStatus,
	poll time.Duration) time.Time {
	if st.RetryAt != nil {
		return st.RetryAt.Time
	}
	if st.ObservedAt == nil || st.Ticket == nil || st.PendingCreate != nil || st.ObservedGeneration != cr.Generation ||
		refreshPending(cr, st) {
		return time.Time{}
	}
	return st.ObservedAt.Add(poll)
}

// refreshPending reports whether a webhook trigger (the kardinal.io/refresh
// annotation) has not been answered with a read yet.
func refreshPending(cr *kardinalv1alpha1.ChangeRequest, st *kardinalv1alpha1.ChangeRequestStatus) bool {
	v := cr.Annotations[kardinalv1alpha1.RefreshAnnotation]
	return v != "" && v != st.ObservedRefresh
}

// record writes a reading to st: the ticket, its state and the clock, and
// the audit records it calls for.
func (r *Reconciler) record(ctx context.Context, st *kardinalv1alpha1.ChangeRequestStatus, cr *kardinalv1alpha1.ChangeRequest,
	reading Reading, action string, now time.Time, poll time.Duration) {
	prevState := st.State
	ticket := reading.Ticket
	st.Ticket = &ticket
	st.State = normalState(reading.State)
	st.ProviderState = cut(reading.ProviderState, 256)
	st.Approvers = reading.Approvers
	if len(st.Approvers) > 32 {
		st.Approvers = st.Approvers[:32]
	}
	at, until := metav1.NewTime(now), metav1.NewTime(now.Add(staleAfterPolls*poll))
	st.ObservedAt, st.ValidUntil = &at, &until
	st.Ready, st.RetryAt, st.PendingCreate = true, nil, nil
	st.ObservedGeneration = cr.Generation
	st.ObservedRefresh = cr.Annotations[kardinalv1alpha1.RefreshAnnotation]
	st.Verified, st.VerifiedReason = nil, ""
	if cr.Spec.Mode == kardinalv1alpha1.ChangeRequestModeLink {
		if action == AuditActionLinked || st.LinkedAt == nil {
			at := metav1.NewTime(now)
			st.LinkedAt = &at
		}
		v, why := r.linkVerified(ctx, cr, st, reading)
		st.Verified, st.VerifiedReason = &v, why
	}
	setCondition(st, cr.Generation, CondReady, true, ReasonRead, fmt.Sprintf("ticket %s is %s", ticket.Key, st.State), now)
	setCondition(st, cr.Generation, CondStale, false, "Read", "read at "+now.UTC().Format(time.RFC3339), now)
	for _, c := range []string{CondCredentialsInvalid, CondRefused, CondProviderError} {
		setCondition(st, cr.Generation, c, false, "Read", "the last call succeeded", now)
	}
	if action != "" {
		r.enqueue(ctx, st, cr, action, "Success", now, auditMessage(action, cr, st))
	}
	if st.State != prevState {
		switch st.State {
		case kardinalv1alpha1.ChangeRequestApproved:
			r.enqueue(ctx, st, cr, AuditActionApproved, "Success", now, auditMessage(AuditActionApproved, cr, st))
		case kardinalv1alpha1.ChangeRequestRejected:
			r.enqueue(ctx, st, cr, AuditActionRejected, "Success", now, auditMessage(AuditActionRejected, cr, st))
		}
	}
}

// linkVerified reports whether a linked ticket is this promotion's, and
// why or why not (docs/change-requests.md, Open or link):
//
//   - it carries the promotion's correlation id; or
//   - it satisfies link.requireField, was created after the Bundle (an
//     older ticket cannot be this version's), and no other ChangeRequest
//     holds it since earlier (one ticket, one promotion).
//
// Anything it cannot establish (no creation time, the Bundle unreadable or
// re-created, the index unreadable) is not verified.
func (r *Reconciler) linkVerified(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest,
	st *kardinalv1alpha1.ChangeRequestStatus, reading Reading) (bool, string) {
	if reading.CorrelationID != "" && reading.CorrelationID == st.CorrelationID {
		return true, "the ticket carries this promotion's correlation id"
	}
	link := cr.Spec.Link
	if link == nil || link.RequireField == nil {
		return false, "the ticket carries neither this promotion's correlation id nor a link.requireField match"
	}
	want := link.RequireField
	if v, ok := reading.Fields[want.Name]; !ok || v != want.Value {
		return false, fmt.Sprintf("field %s is %q, not %q", want.Name, cut(v, 64), cut(want.Value, 64))
	}
	if reading.Created.IsZero() {
		return false, "the provider gave no creation time, so the ticket cannot be shown to be newer than the promotion"
	}
	var b kardinalv1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Labels[labelBundle]}, &b); err != nil ||
		string(b.UID) != cr.Labels[graph.LabelBundleUID] {
		return false, "the Bundle of this promotion cannot be read"
	}
	if reading.Created.Before(b.CreationTimestamp.Time) {
		return false, fmt.Sprintf("the ticket was created %s, before Bundle %s (%s): an older ticket is not this promotion's",
			reading.Created.UTC().Format(time.RFC3339), b.Name, b.CreationTimestamp.UTC().Format(time.RFC3339))
	}
	holder, err := r.earlierHolder(ctx, cr, st)
	if err != nil {
		return false, "the other ChangeRequests holding this ticket cannot be listed"
	}
	if holder != "" {
		return false, fmt.Sprintf("the ticket is already linked to %s", holder)
	}
	return true, fmt.Sprintf("field %s matches, the ticket is newer than the Bundle and linked to no other promotion", want.Name)
}

// IndexTicket indexes ChangeRequests by the ticket in their status
// (TicketIndexKey): one ticket, one promotion.
const IndexTicket = "kardinal.io/changerequest-ticket"

// TicketIndexKey is a ChangeRequest's ticket as "<provider>/<host>/<key>",
// or "" without one.
func TicketIndexKey(cr *kardinalv1alpha1.ChangeRequest) string {
	if cr.Status.Ticket == nil || cr.Status.Ticket.Key == "" {
		return ""
	}
	host := ""
	switch {
	case cr.Spec.ServiceNow != nil:
		host = cr.Spec.ServiceNow.InstanceURL
	case cr.Spec.Jira != nil:
		host = cr.Spec.Jira.BaseURL
	}
	if u, err := url.Parse(host); err == nil {
		host = strings.ToLower(u.Host)
	}
	return cr.Spec.Provider + "/" + host + "/" + cr.Status.Ticket.Key
}

// TicketIndex is the IndexTicket index function.
func TicketIndex(o client.Object) []string {
	cr, ok := o.(*kardinalv1alpha1.ChangeRequest)
	if !ok {
		return nil
	}
	if k := TicketIndexKey(cr); k != "" {
		return []string{k}
	}
	return nil
}

// earlierHolder names another ChangeRequest that holds the same ticket and
// was there first: one that opened it, or linked it earlier (by linkedAt,
// then name). "" when there is none.
func (r *Reconciler) earlierHolder(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest,
	st *kardinalv1alpha1.ChangeRequestStatus) (string, error) {
	me := cr.DeepCopy()
	me.Status = *st
	key := TicketIndexKey(me)
	var list kardinalv1alpha1.ChangeRequestList
	if err := r.List(ctx, &list, client.MatchingFields{IndexTicket: key}); err != nil {
		return "", fmt.Errorf("list changerequests by ticket: %w", err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if (o.Namespace == cr.Namespace && o.Name == cr.Name) || TicketIndexKey(o) != key {
			continue
		}
		if o.Spec.Mode != kardinalv1alpha1.ChangeRequestModeLink { // it opened the ticket
			return o.Namespace + "/" + o.Name, nil
		}
		if o.Status.LinkedAt == nil || st.LinkedAt == nil {
			continue
		}
		if o.Status.LinkedAt.Before(st.LinkedAt) ||
			(o.Status.LinkedAt.Equal(st.LinkedAt) && o.Namespace+"/"+o.Name < cr.Namespace+"/"+cr.Name) {
			return o.Namespace + "/" + o.Name, nil
		}
	}
	return "", nil
}

// fail writes a failed call to st: a condition and retryAt. The last
// reading stays, and goes stale at validUntil.
func (r *Reconciler) fail(st *kardinalv1alpha1.ChangeRequestStatus, cr *kardinalv1alpha1.ChangeRequest,
	now time.Time, poll time.Duration, err error) {
	retry := poll
	msg := cut(err.Error(), 512)
	var rerr *RetryError
	switch {
	case errors.Is(err, ErrCredentials):
		retry = max(refusedRetry, poll)
		setCondition(st, cr.Generation, CondCredentialsInvalid, true, "Refused", msg, now)
	case errors.Is(err, egress.ErrBlockedAddress):
		retry = max(refusedRetry, poll)
		setCondition(st, cr.Generation, CondRefused, true, "EgressRefused", msg, now)
	case errors.Is(err, ErrIgnoredFilter):
		retry = max(refusedRetry, poll)
		setCondition(st, cr.Generation, CondProviderError, true, ReasonIgnoredFilter, msg, now)
	case errors.As(err, &rerr):
		retry = max(rerr.After, poll)
		setCondition(st, cr.Generation, CondProviderError, true, "Retry", msg, now)
	case errors.Is(err, ErrNotFound):
		st.State = kardinalv1alpha1.ChangeRequestUnknown
		setCondition(st, cr.Generation, CondProviderError, true, "NotFound", msg, now)
	default:
		setCondition(st, cr.Generation, CondProviderError, true, ReasonError, msg, now)
	}
	at := metav1.NewTime(now.Add(retry))
	st.RetryAt = &at
	st.ObservedGeneration = cr.Generation
	st.ObservedRefresh = cr.Annotations[kardinalv1alpha1.RefreshAnnotation]
	if st.ObservedAt == nil {
		setCondition(st, cr.Generation, CondReady, false, ReasonError, msg, now)
	}
}

// finish writes st, flushes the audit outbox and requeues for the next call.
func (r *Reconciler) finish(ctx context.Context, log zerolog.Logger, cr *kardinalv1alpha1.ChangeRequest,
	st kardinalv1alpha1.ChangeRequestStatus, now time.Time, timeout time.Duration) (ctrl.Result, error) {
	next := r.nextCall(cr, &st, pollInterval(cr.Spec.PollInterval))
	if next.IsZero() {
		next = now.Add(pollInterval(cr.Spec.PollInterval))
	}
	return r.finishAt(ctx, log, cr, st, now, timeout, next)
}

// finishAt is finish with the next call at next.
func (r *Reconciler) finishAt(ctx context.Context, log zerolog.Logger, cr *kardinalv1alpha1.ChangeRequest,
	st kardinalv1alpha1.ChangeRequestStatus, now time.Time, timeout time.Duration, next time.Time) (ctrl.Result, error) {
	if err := r.writeIfChanged(ctx, cr, st); err != nil {
		if apierrors.IsConflict(err) {
			// The newer version's reconcile searches before it creates.
			log.Debug().Err(err).Msg("changerequest changed since it was read")
			return ctrl.Result{RequeueAfter: conflictRetry}, nil
		}
		return ctrl.Result{}, err
	}
	r.flushAudit(ctx, log, cr)
	return ctrl.Result{RequeueAfter: wait(now, next, cr.Status, timeout)}, nil
}

// wait is the requeue delay to next, or to the timeout when that comes
// first (status.timedOut is written then), at least a second.
func wait(now, next time.Time, st kardinalv1alpha1.ChangeRequestStatus, timeout time.Duration) time.Duration {
	if timeout > 0 && !st.TimedOut && st.StartedAt != nil {
		if end := st.StartedAt.Add(timeout); end.Before(next) {
			next = end
		}
	}
	return max(next.Sub(now), time.Second)
}

// write patches the status to st with the optimistic lock.
func (r *Reconciler) write(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest, st kardinalv1alpha1.ChangeRequestStatus) error {
	patch := client.MergeFromWithOptions(cr.DeepCopy(), client.MergeFromWithOptimisticLock{})
	cr.Status = st
	if err := r.Status().Patch(ctx, cr, patch); err != nil {
		return fmt.Errorf("status patch: %w", err)
	}
	return nil
}

// writeIfChanged is write when st differs from the stored status. A
// ChangeRequest deleted meanwhile is no error.
func (r *Reconciler) writeIfChanged(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest, st kardinalv1alpha1.ChangeRequestStatus) error {
	if equality.Semantic.DeepEqual(st, cr.Status) {
		return nil
	}
	if err := r.write(ctx, cr, st); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// enqueue adds an audit record of action to st's outbox. Its name follows
// from the correlation id, the action and the ticket, so a re-run writes
// nothing new.
func (r *Reconciler) enqueue(ctx context.Context, st *kardinalv1alpha1.ChangeRequestStatus, cr *kardinalv1alpha1.ChangeRequest,
	action, outcome string, now time.Time, msg string) {
	key := ""
	if st.Ticket != nil {
		key = st.Ticket.Key
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{st.CorrelationID, action, key}, "\x00")))
	prefix := cr.Name
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	prefix = strings.TrimRight(prefix, "-.")
	labels := map[string]string{
		"kardinal.io/pipeline":    cr.Labels[labelPipeline],
		"kardinal.io/bundle":      cr.Labels[labelBundle],
		"kardinal.io/environment": cr.Labels[labelEnvironment],
		"kardinal.io/action":      action,
	}
	for k, v := range labels {
		if v == "" {
			delete(labels, k)
		}
	}
	e := audit.Entry(fmt.Sprintf("%s-cr-%s", prefix, hex.EncodeToString(sum[:])[:12]), labels,
		kardinalv1alpha1.AuditEventSpec{
			BundleName:   cr.Labels[labelBundle],
			PipelineName: cr.Labels[labelPipeline],
			Environment:  cr.Labels[labelEnvironment],
			Action:       action,
			Outcome:      outcome,
			Message:      msg,
		}, metav1.NewTime(now))
	st.PendingAuditEvents = audit.Enqueue(ctx, auditKind, st.PendingAuditEvents, e)
}

// auditMessage is the message of an audit record of action.
func auditMessage(action string, cr *kardinalv1alpha1.ChangeRequest, st *kardinalv1alpha1.ChangeRequestStatus) string {
	key, url := "", ""
	if st.Ticket != nil {
		key, url = st.Ticket.Key, st.Ticket.URL
	}
	var what string
	switch action {
	case AuditActionOpened:
		what = "opened"
	case AuditActionLinked:
		what = "linked"
		if st.Verified != nil && !*st.Verified {
			what = "linked (not verified: " + st.VerifiedReason + ")"
		}
	case AuditActionApproved:
		what = "approved"
	case AuditActionRejected:
		what = "rejected"
	}
	msg := fmt.Sprintf("change request %s %s %s ticket %s", templateOf(cr), what, cr.Spec.Provider, key)
	if url != "" {
		msg += " (" + url + ")"
	}
	if st.ProviderState != "" {
		msg += "; provider state: " + st.ProviderState
	}
	return msg
}

// flushAudit creates the outbox's AuditEvents and removes the written
// entries. An entry that fails stays for the next reconcile.
func (r *Reconciler) flushAudit(ctx context.Context, log zerolog.Logger, cr *kardinalv1alpha1.ChangeRequest) {
	if len(cr.Status.PendingAuditEvents) == 0 {
		return
	}
	remaining, err := audit.Flush(ctx, r.Client, auditKind, cr.Namespace, cr.Status.PendingAuditEvents)
	if err != nil {
		log.Warn().Err(err).Msg("change request AuditEvents not written yet")
	}
	if len(remaining) == len(cr.Status.PendingAuditEvents) {
		return
	}
	st := *cr.Status.DeepCopy()
	st.PendingAuditEvents = remaining
	if err := r.write(ctx, cr, st); err != nil {
		log.Debug().Err(err).Msg("audit outbox not trimmed yet; the next reconcile writes nothing twice")
	}
}

// linkKey is the ticket key mode link reads, or "" and why there is none
// yet.
func (r *Reconciler) linkKey(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest, bundle, env, uid string) (string, string, error) {
	link := cr.Spec.Link
	if link == nil {
		return "", "", errors.New("mode link needs spec.link")
	}
	switch link.From {
	case kardinalv1alpha1.ChangeRequestLinkBundleLabel, kardinalv1alpha1.ChangeRequestLinkBundleAnnotation:
		var b kardinalv1alpha1.Bundle
		if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: bundle}, &b); err != nil {
			if apierrors.IsNotFound(err) {
				return "", "Bundle " + bundle + " not found", nil
			}
			return "", "", fmt.Errorf("read bundle %s: %w", bundle, err)
		}
		if string(b.UID) != uid {
			return "", "Bundle " + bundle + " was re-created: this instance is of the earlier one", nil
		}
		src, kind := b.Labels, "label"
		if link.From == kardinalv1alpha1.ChangeRequestLinkBundleAnnotation {
			src, kind = b.Annotations, "annotation"
		}
		if v := strings.TrimSpace(src[link.Name]); v != "" {
			return v, "", nil
		}
		return "", fmt.Sprintf("waiting for the ticket key in Bundle %s's %s %s", bundle, kind, link.Name), nil
	case kardinalv1alpha1.ChangeRequestLinkApprovalField:
		var gates kardinalv1alpha1.PolicyGateList
		if err := r.List(ctx, &gates, client.InNamespace(cr.Namespace),
			client.MatchingLabels{labelBundle: bundle, labelEnvironment: env}); err != nil {
			return "", "", fmt.Errorf("list policygates: %w", err)
		}
		var keys []string
		for _, g := range gates.Items {
			for _, a := range g.Status.Approvals {
				if v := strings.TrimSpace(a.Fields[link.Name]); a.Counted && a.Decision == "approve" && v != "" &&
					!slices.Contains(keys, v) {
					keys = append(keys, v)
				}
			}
		}
		sort.Strings(keys)
		switch len(keys) {
		case 0:
			return "", fmt.Sprintf("waiting for a counted approval of %s with the field %s", env, link.Name), nil
		case 1:
			return keys[0], "", nil
		}
		return "", "", fmt.Errorf("the counted approvals of %s name %d tickets in field %s (%s): approve with one",
			env, len(keys), link.Name, strings.Join(keys, ", "))
	}
	return "", "", fmt.Errorf("unknown link.from %q", link.From)
}

// SetupWithManager registers the reconciler. Only spec or annotation
// changes and new audit outbox entries trigger a reconcile; the poll is
// RequeueAfter. The Limiter's wake-ups come in through a channel source.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &kardinalv1alpha1.ChangeRequest{}, IndexTicket,
		TicketIndex); err != nil {
		return fmt.Errorf("index changerequests by ticket: %w", err)
	}
	n := workers
	if r.Limiter != nil && r.Limiter.Global+4 > n {
		n = r.Limiter.Global + 4
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.ChangeRequest{}, builder.WithPredicates(predicate.Or(eventfilter.SpecOrAnnotationChanged, auditPending))).
		// A version that becomes Superseded or Rejected stops its instances
		// at once (versionFinal), not at their next poll.
		Watches(&kardinalv1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.bundleInstances),
			builder.WithPredicates(becameFinal)).
		WithOptions(controller.Options{MaxConcurrentReconciles: n, NewQueue: fairqueue.NewFor(mgr)})
	if r.Limiter != nil {
		wake := make(chan event.GenericEvent, wakeQueue)
		r.Limiter.Wake = func(key types.NamespacedName) {
			ev := event.GenericEvent{Object: &kardinalv1alpha1.ChangeRequest{
				ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}}
			select {
			case wake <- ev:
			default:
				go func() { wake <- ev }()
			}
		}
		b = b.WatchesRawSource(source.Channel(wake, &handler.EnqueueRequestForObject{}))
	}
	return shard.Active().Complete(b, tracing.WrapReconciler("changerequest", r), &kardinalv1alpha1.ChangeRequestList{})
}

// versionFinal says why the instance's version is final, or "" while it
// may still promote: its Bundle is Superseded or Rejected (both final), or
// gone, or re-created under the same name (another UID). Its spec.suspend
// reads the Bundle phase, but the Graph holds the steps the instance node
// depends on once the Bundle is Superseded or Rejected, so kro never
// re-applies it with suspend (#2138, as MetricCheck's versionFinal). A
// Bundle that cannot be read for another reason is not final: the instance
// reads its ticket as before.
func (r *Reconciler) versionFinal(ctx context.Context, cr *kardinalv1alpha1.ChangeRequest, bundle, uid string) string {
	var b kardinalv1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: bundle}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return "Bundle " + bundle + " is gone"
		}
		return ""
	}
	switch {
	case string(b.UID) != uid:
		return "Bundle " + bundle + " was re-created"
	case b.Status.Phase == "Superseded" || b.Status.Phase == "Rejected":
		return "its version is " + b.Status.Phase
	}
	return ""
}

// becameFinal passes a Bundle update that moved it to Superseded or
// Rejected, or back out of either: a final instance asks for no requeue, so
// a version that is promoting again must enqueue its instances to resume.
var becameFinal = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		final := func(o client.Object) bool {
			b, ok := o.(*kardinalv1alpha1.Bundle)
			return ok && (b.Status.Phase == "Superseded" || b.Status.Phase == "Rejected")
		}
		return final(e.ObjectNew) != final(e.ObjectOld)
	},
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// bundleInstances maps a Bundle to its ChangeRequest instances.
func (r *Reconciler) bundleInstances(ctx context.Context, o client.Object) []reconcile.Request {
	var list kardinalv1alpha1.ChangeRequestList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()), client.MatchingLabels{labelBundle: o.GetName()}); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("bundle", o.GetName()).Msg("list the ChangeRequest instances of a finished version")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// auditPending passes updates that add audit outbox entries.
var auditPending = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, okOld := e.ObjectOld.(*kardinalv1alpha1.ChangeRequest)
		n, okNew := e.ObjectNew.(*kardinalv1alpha1.ChangeRequest)
		return okOld && okNew && audit.Pending(o.Status.PendingAuditEvents, n.Status.PendingAuditEvents)
	},
}

const (
	labelPipeline    = "kardinal.io/pipeline"
	labelBundle      = "kardinal.io/bundle"
	labelEnvironment = "kardinal.io/environment"
)

// templateOf is the template an instance was made from (its label), or its
// own name.
func templateOf(cr *kardinalv1alpha1.ChangeRequest) string {
	if t := cr.Labels[graph.LabelChangeRequestTemplate]; t != "" {
		return t
	}
	return cr.Name
}

// normalState is s when it is a state status.state admits, else unknown.
func normalState(s string) string {
	switch s {
	case kardinalv1alpha1.ChangeRequestPending, kardinalv1alpha1.ChangeRequestApproved, kardinalv1alpha1.ChangeRequestRejected,
		kardinalv1alpha1.ChangeRequestCancelled, kardinalv1alpha1.ChangeRequestClosed:
		return s
	}
	return kardinalv1alpha1.ChangeRequestUnknown
}

// setCondition sets condition typ; a condition that does not change keeps
// its transition time.
func setCondition(st *kardinalv1alpha1.ChangeRequestStatus, gen int64, typ string, status bool, reason, msg string, now time.Time) {
	s := metav1.ConditionFalse
	if status {
		s = metav1.ConditionTrue
	}
	if !status && meta.FindStatusCondition(st.Conditions, typ) == nil && typ != CondReady {
		return // a problem never seen needs no False condition
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: typ, Status: s, Reason: reason,
		Message: cut(msg, 1024), ObservedGeneration: gen, LastTransitionTime: metav1.NewTime(now)})
}

// pollInterval is spec.pollInterval, DefaultPollInterval when unset or
// invalid, at least MinPollInterval.
func pollInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if s == "" || err != nil || d <= 0 {
		return DefaultPollInterval
	}
	return max(d, MinPollInterval)
}

// parseTimeout is spec.timeout, DefaultTimeout when unset or invalid, 0 for
// "0" (none).
func parseTimeout(s string) time.Duration {
	if s == "0" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if s == "" || err != nil || d <= 0 {
		return DefaultTimeout
	}
	return d
}

// cut is s cut to n bytes at a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func (r *Reconciler) secrets() client.Reader {
	if r.SecretReader != nil {
		return r.SecretReader
	}
	return r.Client
}

func (r *Reconciler) cancelSlot(key types.NamespacedName) {
	if r.Limiter != nil {
		r.Limiter.Cancel(key)
	}
}

// now returns NowFn() when set, else the current UTC time.
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}
