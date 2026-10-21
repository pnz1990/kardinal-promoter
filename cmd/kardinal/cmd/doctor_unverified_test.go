// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestDoctor_UnverifiedHeld (#2451): after the agent index recovers, doctor
// lists the versions a kardinal-agents-* gate holds because they were
// stamped unverified, each with the kardinal override command that lets it
// through, and names the other gates that hold one too. It leaves out a
// version whose agent gate passed (a rollback to what was Verified there),
// an agent's version, a done one, and one whose writer the Ready index now
// names as an agent. While no index is Ready it is an INFO; with nothing
// held it passes, a WARN when only the current namespace could be listed.
//
// Not parallel: it parses each fix with NewRootCmd, which binds the global
// flags to package variables (TestParallelTestsChangeNoPackageVariable).
//
// Covers CLI-DOCTOR-05.
func TestDoctor_UnverifiedHeld(t *testing.T) {
	evaluated := metav1.Now()
	gate := func(ns, bundle, env, tmpl string, ready bool) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: tmpl + "-" + env + "--" + bundle, Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": env, "kardinal.io/pipeline": "checkout",
				"kardinal.io/gate-template": tmpl}},
			Spec:   v1alpha1.PolicyGateSpec{Expression: "false"},
			Status: v1alpha1.PolicyGateStatus{Ready: ready, LastEvaluatedAt: &evaluated}}
	}
	bundle := func(ns, name, phase string, a *v1alpha1.Actor) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.BundleSpec{Pipeline: "checkout", Type: "image", Actor: a}, Status: v1alpha1.BundleStatus{Phase: phase}}
	}
	unverified := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: p} }
	index := func(ready bool) *v1alpha1.AgentIndex {
		st := metav1.ConditionTrue
		if !ready {
			st = metav1.ConditionFalse
		}
		return &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"},
			Status: v1alpha1.AgentIndexStatus{
				Users:      map[string]v1alpha1.AgentIndexEntry{"oidc:fixer-bot": {Agent: "fixer", Sponsor: "alice", Autonomy: "auto"}},
				Conditions: []metav1.Condition{{Type: "Ready", Status: st, Reason: "IndexFull"}}}}
	}
	estate := func(ready bool) []sigs_client.Object {
		return []sigs_client.Object{
			index(ready),
			// Held only by its agent gate.
			bundle("team-a", "checkout-v2", "Promoting", unverified("alice")),
			gate("team-a", "checkout-v2", "prod", "kardinal-agents-auto", false),
			// Held by its agent gate and a change window gate.
			bundle("team-b", "checkout-v3", "Promoting", unverified("bob")),
			gate("team-b", "checkout-v3", "uat", "kardinal-agents-deny", false),
			gate("team-b", "checkout-v3", "uat", "no-weekends", false),
			// A rollback to what was Verified there: its agent gate passed.
			bundle("team-a", "checkout-rb", "Promoting", unverified("alice")),
			gate("team-a", "checkout-rb", "prod", "kardinal-agents-auto", true),
			// An agent's version, a done one, and one whose writer is now
			// named as an agent: not listed.
			bundle("team-a", "checkout-agent", "Promoting", &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "sa", Agent: "fixer", Sponsor: "alice"}),
			gate("team-a", "checkout-agent", "prod", "kardinal-agents-auto", false),
			bundle("team-a", "checkout-v1", "Superseded", unverified("alice")),
			gate("team-a", "checkout-v1", "prod", "kardinal-agents-auto", false),
			bundle("team-a", "checkout-bot", "Promoting", unverified("oidc:fixer-bot")),
			gate("team-a", "checkout-bot", "prod", "kardinal-agents-auto", false),
		}
	}
	forbidCluster := func(c *fake.ClientBuilder) *fake.ClientBuilder {
		return c.WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, cl sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
			if _, ok := list.(*v1alpha1.PolicyGateList); ok {
				lo := (&sigs_client.ListOptions{}).ApplyOptions(opts)
				if lo.Namespace == "" {
					return errors.New("forbidden")
				}
			}
			return cl.List(ctx, list, opts...)
		}})
	}
	t.Run("lists each held version with its fix", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(estate(true)...).Build()
		r := checkUnverifiedHeld(context.Background(), c, "team-a")
		assert.Equal(t, doctorWarn, r.icon)
		assert.Equal(t, "2 version(s) shipped while the agent index was not Ready are held: team-a/checkout prod: checkout-v2 by alice; "+
			"team-b/checkout uat: checkout-v3 by bob (also held by no-weekends) (all namespaces)", r.detail)
		assert.Equal(t, "Override each one, or ship it again:\n"+
			`kardinal override checkout prod --gate kardinal-agents-auto --reason "shipped while the agent index was not Ready" -n team-a`+"\n"+
			`kardinal override checkout uat --gate kardinal-agents-deny --reason "shipped while the agent index was not Ready" -n team-b`+
			"   # also held by no-weekends", r.hint)
		// Each fix is a kardinal override its parser takes, with no hidden
		// (older) flag name.
		for _, fix := range strings.Split(r.hint, "\n")[1:] {
			args := shellFields(t, strings.SplitN(fix, "   #", 2)[0])
			require.Equal(t, "kardinal", args[0])
			cmd, rest, err := NewRootCmd().Find(args[1:])
			require.NoError(t, err, fix)
			require.Equal(t, "override", cmd.Name(), fix)
			require.NoError(t, cmd.ParseFlags(rest), fix)
			assert.Len(t, cmd.Flags().Args(), 2, "pipeline and environment: %s", fix)
			reason, err := cmd.Flags().GetString("reason")
			require.NoError(t, err)
			assert.Equal(t, unverifiedOverrideReason, reason)
			cmd.Flags().Visit(func(f *pflag.Flag) { assert.False(t, f.Hidden, "%s: --%s is hidden", fix, f.Name) })
		}
		var out bytes.Buffer
		require.NoError(t, printDoctorResults(&out, []doctorResult{r}))
		assert.Contains(t, out.String(), "\n"+strings.Repeat(" ", len("WARN")+2+doctorColWidth+2)+"kardinal override checkout uat",
			"every line of the hint is indented")
	})
	t.Run("the index not Ready yet is an INFO", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(estate(false)...).Build()
		r := checkUnverifiedHeld(context.Background(), c, "team-a")
		assert.Equal(t, doctorInfo, r.icon)
		assert.True(t, r.noted)
		assert.Contains(t, r.detail, "3 version(s) stamped unverified are held, and the agent index is not Ready yet")
	})
	t.Run("nothing held passes", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(index(true),
			bundle("team-a", "checkout-rb", "Promoting", unverified("alice")),
			gate("team-a", "checkout-rb", "prod", "kardinal-agents-auto", true)).Build()
		r := checkUnverifiedHeld(context.Background(), c, "team-a")
		assert.Equal(t, doctorPass, r.icon)
		assert.Equal(t, "no version is held because of an unverified stamp (all namespaces)", r.detail)
	})
	t.Run("only the current namespace", func(t *testing.T) {
		c := forbidCluster(fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(estate(true)...)).Build()
		r := checkUnverifiedHeld(context.Background(), c, "team-a")
		assert.Equal(t, doctorWarn, r.icon)
		assert.Contains(t, r.detail, "1 version(s) shipped while the agent index was not Ready are held: team-a/checkout prod: checkout-v2 by alice (namespace team-a)")
		c = forbidCluster(fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(index(true))).Build()
		r = checkUnverifiedHeld(context.Background(), c, "team-a")
		assert.Equal(t, doctorWarn, r.icon)
		assert.Contains(t, r.detail, "checked namespace team-a only")
	})
}

// shellFields splits s into words as a shell does for the commands doctor
// prints: spaces separate, and a double-quoted Go string is one word.
func shellFields(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for s = strings.TrimLeft(s, " "); s != ""; s = strings.TrimLeft(s, " ") {
		if s[0] == '"' {
			q, err := strconv.QuotedPrefix(s)
			require.NoError(t, err, s)
			u, err := strconv.Unquote(q)
			require.NoError(t, err)
			out, s = append(out, u), s[len(q):]
			continue
		}
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			i = len(s)
		}
		out, s = append(out, s[:i]), s[i:]
	}
	return out
}
