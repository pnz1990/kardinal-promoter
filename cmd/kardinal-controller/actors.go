// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// actorWriter stamps what the UI API and the Bundle API write for their
// callers (docs/actors.md). It derives the caller's actor from the
// TokenReview identity and the release's AgentIndex with the same functions
// the chart's actor policy transliterates, and refuses what that policy
// would refuse. A nil actorWriter derives from no index (no agents).
type actorWriter struct {
	source *actor.Source
}

// errActorRefused wraps the refusals forCaller returns (403).
var errActorRefused = errors.New("refused")

// forCaller is the stamp for the caller of ctx, written via ui or api, for a
// write at w. With no authenticated user (the static token, or no UI auth)
// the shared credential is the actor: a bot named after it.
func (a *actorWriter) forCaller(ctx context.Context, via string, w actor.Where) (*v1alpha1.Actor, error) {
	user, ok := uiauth.UserFrom(ctx)
	if !ok || user.Username == "" {
		principal := lifecycle.UICreator
		if via == v1alpha1.ViaAPI {
			principal = actor.BundleAPIPrincipal
		}
		return &v1alpha1.Actor{Kind: v1alpha1.ActorBot, Principal: principal, Via: via}, nil
	}
	var src *actor.Source
	if a != nil {
		src = a.source
	}
	r, err := src.Resolve(ctx, actor.User{Username: user.Username, Groups: user.Groups}, w)
	switch {
	case errors.Is(err, actor.ErrConflict), errors.Is(err, actor.ErrBlocked), errors.Is(err, actor.ErrOutOfScope):
		return nil, fmt.Errorf("%w: %w", errActorRefused, err)
	case err != nil:
		return nil, err
	}
	return actor.OnBehalf(r, via), nil
}

// writeActorError answers a forCaller error: 403 for a refusal, 503 when the
// index cannot be read (fail closed).
func writeActorError(w http.ResponseWriter, log zerolog.Logger, err error) {
	if errors.Is(err, errActorRefused) {
		http.Error(w, "forbidden: "+strings.TrimPrefix(err.Error(), errActorRefused.Error()+": "), http.StatusForbidden)
		return
	}
	log.Error().Err(err).Msg("derive the caller's actor")
	http.Error(w, "cannot read the agent index; try again", http.StatusServiceUnavailable)
}

// auditTarget names the write a handler is serving, for its APIWrite
// AuditEvent (accesslog.Config.Audit).
func auditTarget(r *http.Request, t accesslog.AuditTarget) {
	if t.Route == "" {
		t.Route = r.URL.Path
	}
	accesslog.FromContext(r.Context()).Audit = &t
}

// selfUsername asks the API server who the controller is (a
// SelfSubjectReview): its own records are stamped with it. On failure it
// returns lifecycle.ControllerCreator, which no person can be.
func selfUsername(ctx context.Context, cfg *rest.Config, log zerolog.Logger) string {
	c, err := client.New(cfg, client.Options{})
	if err == nil {
		review := &authenticationv1.SelfSubjectReview{}
		if err = c.Create(ctx, review); err == nil && review.Status.UserInfo.Username != "" {
			return review.Status.UserInfo.Username
		}
	}
	log.Warn().Err(err).Msg("cannot read the controller's own username (SelfSubjectReview); its records name kardinal-controller")
	return lifecycle.ControllerCreator
}

// apiAuditKind labels the APIWrite records in kardinal_audit_events_dropped_total.
const apiAuditKind = "APIWrite"

// apiAuditPerSecond bounds the APIWrite AuditEvents written per second.
const apiAuditPerSecond = 50

// apiAudit writes an APIWrite AuditEvent for each UI API and Bundle API
// write (accesslog.AuditSink). Records are queued and written by Start, so
// a slow API server never delays a response; past the per-second budget or
// a full queue they are dropped and counted. A manager Runnable on every
// replica: each serves its own requests.
type apiAudit struct {
	client client.Client
	log    zerolog.Logger
	now    func() time.Time
	queue  chan v1alpha1.AuditEvent

	mu          sync.Mutex
	windowStart time.Time
	written     int
}

func newAPIAudit(c client.Client, log zerolog.Logger) *apiAudit {
	return &apiAudit{client: c, log: log, now: time.Now, queue: make(chan v1alpha1.AuditEvent, 256)}
}

// Record implements accesslog.AuditSink.
func (a *apiAudit) Record(server, method string, status int, t *accesslog.AuditTarget) {
	if t == nil || t.Namespace == "" || (t.Pipeline == "" && t.Bundle == "" && t.Environment == "") {
		return
	}
	if !a.allow() {
		audit.Dropped.WithLabelValues(apiAuditKind, "rate").Inc()
		return
	}
	outcome := "Success"
	if status >= 400 {
		outcome = "Failure"
	}
	now := a.now()
	msg := t.Message
	if outcome == "Failure" {
		msg = fmt.Sprintf("refused (%d): %s", status, msg)
	}
	labels := map[string]string{"kardinal.io/action": "APIWrite", "kardinal.io/server": server}
	for k, v := range map[string]string{lifecycle.LabelPipeline: t.Pipeline, lifecycle.LabelBundle: t.Bundle,
		"kardinal.io/environment": t.Environment} {
		if v != "" && len(v) <= 63 {
			labels[k] = v
		}
	}
	ev := v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "api-write-", Namespace: t.Namespace, Labels: labels},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp: metav1.NewTime(now), BundleName: t.Bundle, PipelineName: t.Pipeline, Environment: t.Environment,
			Action: "APIWrite", Outcome: outcome, Message: clipMessage(msg), Actor: t.Actor,
			Request: &v1alpha1.AuditRequest{Route: clipRoute(t.Route), Method: method},
		},
	}
	lifecycle.StampCreatedAt(&ev, now)
	select {
	case a.queue <- ev:
	default:
		audit.Dropped.WithLabelValues(apiAuditKind, "queue").Inc()
	}
}

func (a *apiAudit) allow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if now.Sub(a.windowStart) >= time.Second {
		a.windowStart, a.written = now, 0
	}
	if a.written >= apiAuditPerSecond {
		return false
	}
	a.written++
	return true
}

// Start writes queued records until ctx ends, then the ones still queued.
func (a *apiAudit) Start(ctx context.Context) error {
	for {
		select {
		case ev := <-a.queue:
			a.write(ctx, ev)
		case <-ctx.Done():
			for {
				select {
				case ev := <-a.queue:
					a.write(context.Background(), ev)
				default:
					return nil
				}
			}
		}
	}
}

func (a *apiAudit) write(ctx context.Context, ev v1alpha1.AuditEvent) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := a.client.Create(ctx, &ev); err != nil {
		audit.Dropped.WithLabelValues(apiAuditKind, "error").Inc()
		a.log.Warn().Err(err).Str("namespace", ev.Namespace).Str("route", ev.Spec.Request.Route).
			Msg("failed to write the APIWrite AuditEvent")
	}
}

// NeedLeaderElection: every replica records the requests it serves.
func (a *apiAudit) NeedLeaderElection() bool { return false }

func clipMessage(s string) string {
	if len(s) > 1024 {
		return s[:1024]
	}
	return s
}

func clipRoute(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}

// stampFor derives the UI caller's actor for a write at where and names the
// write's APIWrite audit target t (its namespace, Pipeline and environment
// default to where's). When the caller may not act there it answers 403 (503
// when the agent index cannot be read) and returns false; the refusal is
// audited too.
func (s *uiAPIServer) stampFor(w http.ResponseWriter, r *http.Request, where actor.Where, t accesslog.AuditTarget) (*v1alpha1.Actor, bool) {
	t.Namespace = where.Namespace
	if t.Pipeline == "" {
		t.Pipeline = where.Pipeline
	}
	if t.Environment == "" {
		t.Environment = where.Environment
	}
	a, err := s.actors.forCaller(r.Context(), v1alpha1.ViaUI, where)
	if err != nil {
		// A refused caller's actor could not be derived: the record names
		// the principal in its message instead.
		t.Message += " by " + uiRequester(r.Context())
		auditTarget(r, t)
		writeActorError(w, s.log, err)
		return nil, false
	}
	t.Actor = a
	auditTarget(r, t)
	return a, true
}

// auditDone completes the request's audit target once the write succeeded:
// the Bundle it made or changed, and what was done.
func auditDone(r *http.Request, bundle, message string) {
	t := accesslog.FromContext(r.Context()).Audit
	if t == nil {
		return
	}
	if bundle != "" {
		t.Bundle = bundle
	}
	if message != "" {
		t.Message = message
	}
}

// agentIndexReady is a ready check that passes once the AgentIndex name
// exists.
func agentIndexReady(r client.Reader, name string) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()
		var idx v1alpha1.AgentIndex
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &idx); err != nil {
			return fmt.Errorf("agent index %s: %w", name, err)
		}
		return nil
	}
}
