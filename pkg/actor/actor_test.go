// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

func identity(name string, age int, sponsor string, subjects ...v1alpha1.AgentSubject) v1alpha1.AgentIdentity {
	return v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name,
			CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(age) * time.Minute))},
		Spec: v1alpha1.AgentIdentitySpec{
			Subjects: subjects, Sponsor: v1alpha1.AgentSponsor{User: sponsor},
			Scope: v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Environments: []string{"test", "uat"}},
		},
	}
}

func sa(ns, name string) v1alpha1.AgentSubject {
	return v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: name}
}
func user(name string) v1alpha1.AgentSubject { return v1alpha1.AgentSubject{Kind: "User", Name: name} }
func group(name string) v1alpha1.AgentSubject {
	return v1alpha1.AgentSubject{Kind: "Group", Name: name}
}

// registry is the test registry: fixer (an SA and a group), reviewer (a
// user, same sponsor), and a suspended agent.
func registry(t *testing.T) *v1alpha1.AgentIndexStatus {
	t.Helper()
	ids := []v1alpha1.AgentIdentity{
		identity("fixer", 0, "alice", sa("ci", "fixer"), group("oidc:agents-fixer")),
		identity("reviewer", 1, "alice", user("oidc:reviewer")),
		identity("parked", 2, "bob", sa("ci", "parked")),
	}
	ids[2].Spec.Suspended = true
	reg := actor.Evaluate(ids, actor.MaxIndexBytes)
	for n, v := range reg.Verdicts {
		require.True(t, v.Ready, "%s: %s", n, v.Message)
	}
	return &reg.Index
}

// TestDerive runs the admission cases of docs/design/17-actors.md section
// 13 through Derive and Scope; the live admission tests run them through the
// chart's policy.
func TestDerive(t *testing.T) {
	idx := registry(t)
	for _, tc := range []struct {
		name    string
		user    actor.User
		where   actor.Where
		kind    string
		agent   string
		sponsor string
		err     error
	}{
		{name: "human", user: actor.User{Username: "alice"}, kind: "human"},
		{name: "unregistered ServiceAccount is a bot", user: actor.User{Username: "system:serviceaccount:ci:builder"}, kind: "bot"},
		{name: "a person named bundle-api is a person (the static token's bot is built by the API)",
			user: actor.User{Username: "bundle-api"}, kind: "human"},
		{name: "agent ServiceAccount", user: actor.User{Username: "system:serviceaccount:ci:fixer"},
			where: actor.Where{Namespace: "payments-eu", Pipeline: "checkout"}, kind: "agent", agent: "fixer", sponsor: "alice"},
		{name: "agent by group", user: actor.User{Username: "oidc:someone", Groups: []string{"system:authenticated", "oidc:agents-fixer"}},
			where: actor.Where{Namespace: "payments-eu"}, kind: "agent", agent: "fixer", sponsor: "alice"},
		{name: "agent user", user: actor.User{Username: "oidc:reviewer"}, where: actor.Where{Namespace: "payments-us", Environment: "uat"},
			kind: "agent", agent: "reviewer", sponsor: "alice"},
		{name: "two agents", user: actor.User{Username: "oidc:reviewer", Groups: []string{"oidc:agents-fixer"}}, err: actor.ErrConflict},
		{name: "suspended", user: actor.User{Username: "system:serviceaccount:ci:parked"}, err: actor.ErrBlocked},
		{name: "namespace out of scope", user: actor.User{Username: "system:serviceaccount:ci:fixer"},
			where: actor.Where{Namespace: "billing"}, kind: "agent", agent: "fixer", sponsor: "alice", err: actor.ErrOutOfScope},
		{name: "environment out of scope", user: actor.User{Username: "system:serviceaccount:ci:fixer"},
			where: actor.Where{Namespace: "payments-eu", Environment: "prod"}, kind: "agent", agent: "fixer", sponsor: "alice", err: actor.ErrOutOfScope},
		{name: "no index", user: actor.User{Username: "system:serviceaccount:ci:fixer"}, kind: "bot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := idx
			if tc.name == "no index" {
				ix = nil
			}
			r, err := actor.Derive(tc.user, ix)
			if err == nil {
				err = actor.Scope(r.Entry, tc.where)
			}
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				if tc.kind == "" {
					return
				}
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.kind, r.Actor.Kind)
			assert.Equal(t, tc.user.Username, r.Actor.Principal)
			assert.Equal(t, tc.agent, r.Actor.Agent)
			assert.Equal(t, tc.sponsor, r.Actor.Sponsor)
		})
	}
}

func TestEvaluate(t *testing.T) {
	t.Run("the older AgentIdentity keeps a shared subject", func(t *testing.T) {
		reg := actor.Evaluate([]v1alpha1.AgentIdentity{
			identity("late", 5, "bob", sa("ci", "fixer"), sa("ci", "late")),
			identity("fixer", 0, "alice", sa("ci", "fixer")),
		}, actor.MaxIndexBytes)
		assert.True(t, reg.Verdicts["fixer"].Ready)
		assert.Equal(t, actor.ReasonSubjectConflict, reg.Verdicts["late"].Reason)
		assert.Equal(t, "fixer", reg.Index.Users["system:serviceaccount:ci:fixer"].Agent)
		assert.Empty(t, reg.Index.Users["system:serviceaccount:ci:fixer"].Blocked)
		// The loser's other subjects are blocked, not people.
		assert.Contains(t, reg.Index.Users["system:serviceaccount:ci:late"].Blocked, "not ready")
	})
	t.Run("an agent cannot sponsor an agent", func(t *testing.T) {
		reg := actor.Evaluate([]v1alpha1.AgentIdentity{
			identity("a", 0, "alice", user("oidc:a")),
			identity("b", 1, "oidc:a", user("oidc:b")),
		}, actor.MaxIndexBytes)
		assert.Equal(t, actor.ReasonInvalidSponsor, reg.Verdicts["b"].Reason)
		assert.NotEmpty(t, reg.Index.Users["oidc:b"].Blocked)
	})
	t.Run("suspended is ready but blocked", func(t *testing.T) {
		id := identity("a", 0, "alice", group("g"))
		id.Spec.Suspended = true
		reg := actor.Evaluate([]v1alpha1.AgentIdentity{id}, actor.MaxIndexBytes)
		assert.True(t, reg.Verdicts["a"].Ready)
		assert.Equal(t, "suspended", reg.Index.Groups["g"].Blocked)
	})
	t.Run("defaults", func(t *testing.T) {
		reg := actor.Evaluate([]v1alpha1.AgentIdentity{identity("a", 0, "alice", user("u"))}, actor.MaxIndexBytes)
		e := reg.Index.Users["u"]
		assert.Equal(t, []string{"*"}, e.Pipelines)
		assert.Equal(t, "approve", e.Autonomy)
	})
	t.Run("index full", func(t *testing.T) {
		var ids []v1alpha1.AgentIdentity
		for i := range 50 {
			ids = append(ids, identity(fmt.Sprintf("a%02d", i), i, "alice", user(fmt.Sprintf("u%02d", i))))
		}
		reg := actor.Evaluate(ids, 2000)
		assert.True(t, reg.Full)
		assert.Equal(t, actor.ReasonIndexFull, reg.Verdicts["a00"].Reason, "a full index takes no AgentIdentity")
		assert.False(t, reg.Verdicts["a49"].Ready)
		assert.Empty(t, reg.Index.Users, "written empty")
		assert.False(t, actor.Evaluate(ids, actor.MaxIndexBytes).Full)
	})
	t.Run("blocked entries count against the bound", func(t *testing.T) {
		// 700 AgentIdentities of 16 subjects each, all conflicting with an
		// older one: every entry is blocked, and the index must still fit.
		ids := []v1alpha1.AgentIdentity{}
		for i := range 700 {
			var subs []v1alpha1.AgentSubject
			for j := range 16 {
				subs = append(subs, sa(fmt.Sprintf("team-%04d", i), fmt.Sprintf("agent-subject-%02d", j)))
			}
			ids = append(ids, identity(fmt.Sprintf("agent-%04d", i), i, "alice", subs...))
		}
		reg := actor.Evaluate(ids, actor.MaxIndexBytes)
		assert.True(t, reg.Full, "11,200 subjects do not fit")
		raw, err := json.Marshal(reg.Index)
		require.NoError(t, err)
		assert.Less(t, len(raw), actor.MaxIndexBytes+64*1024, "a full index stays small enough to write")
		for _, v := range reg.Verdicts {
			require.Equal(t, actor.ReasonIndexFull, v.Reason)
		}
	})
	t.Run("deterministic", func(t *testing.T) {
		a := []v1alpha1.AgentIdentity{identity("x", 0, "alice", user("u")), identity("y", 0, "bob", user("u"))}
		b := []v1alpha1.AgentIdentity{a[1], a[0]}
		assert.Equal(t, actor.Evaluate(a, actor.MaxIndexBytes), actor.Evaluate(b, actor.MaxIndexBytes))
		// Same creation time: the name decides.
		assert.True(t, actor.Evaluate(a, actor.MaxIndexBytes).Verdicts["x"].Ready)
	})
}

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		a    v1alpha1.Actor
		want string
	}{
		{v1alpha1.Actor{Kind: "agent", Principal: "p", Agent: "fixer", Sponsor: "alice"}, "agent fixer for alice"},
		{v1alpha1.Actor{Kind: "agent", Principal: "p", Agent: "fixer", Sponsor: "alice", Via: "ui"}, "agent fixer for alice via the UI"},
		{v1alpha1.Actor{Kind: "human", Principal: "alice"}, "alice"},
		{v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "api"}, "alice via the API"},
		{v1alpha1.Actor{Kind: "bot", Principal: "system:serviceaccount:ci:b"}, "bot system:serviceaccount:ci:b"},
		{v1alpha1.Actor{Kind: "controller", Principal: "c", Via: "subscription"}, "kardinal (subscription)"},
		{actor.OrUnknown(nil, "alice"), "alice"},
		{actor.OrUnknown(nil, ""), "unknown"},
	} {
		assert.Equal(t, tc.want, actor.Describe(tc.a))
	}
	a := v1alpha1.Actor{Kind: "agent", Asserted: &v1alpha1.AssertedActor{Model: "m", Session: "s"}}
	assert.Equal(t, "model m, session s (asserted)", actor.DescribeAsserted(a))
	assert.Empty(t, actor.DescribeAsserted(v1alpha1.Actor{}))
}

func TestStamp(t *testing.T) {
	r := actor.Resolved{Actor: v1alpha1.Actor{Kind: "human", Principal: "alice"}}
	assert.Nil(t, actor.Stamp(r, &v1alpha1.AssertedActor{}).Asserted, "an empty assertion is not recorded")
	s := actor.Stamp(r, &v1alpha1.AssertedActor{Model: "m"})
	assert.Equal(t, "m", s.Asserted.Model)
	assert.Equal(t, "ui", actor.OnBehalf(r, "ui").Via)
	assert.Empty(t, r.Actor.Via, "OnBehalf does not change its argument")
}

func TestMatches(t *testing.T) {
	assert.True(t, actor.Matches([]string{"*"}, "x"))
	assert.True(t, actor.Matches([]string{"pay-*"}, "pay-eu"))
	assert.True(t, actor.Matches([]string{"pay-*"}, "pay-"))
	assert.False(t, actor.Matches([]string{"pay-*"}, "pa"))
	assert.False(t, actor.Matches([]string{"prod"}, "prod-2"))
	assert.False(t, actor.Matches(nil, "x"))
}
