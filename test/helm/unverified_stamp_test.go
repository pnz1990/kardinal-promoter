// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStampKind_Unverified (#2451): the policies that check a stamp's kind
// without the AgentIndex (who froze or lifted a freeze, who paused, resumed
// or released a hold, who rejected a version) admit a person's stamp of
// kind unverified, which kardinal writes while the index is not Ready, as
// they admit human and agent; a ServiceAccount's stays bot or agent.
// Covers ACTOR-INDEX-06.
func TestStampKind_Unverified(t *testing.T) {
	t.Parallel()
	stamp := func(kind, principal string) map[string]interface{} {
		return map[string]interface{}{"kind": kind, "principal": principal}
	}
	sa := "system:serviceaccount:ci:fixer"
	freeze := func(by string, actor map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "kardinal-freeze-agents"},
			"spec": map[string]interface{}{"type": "blackout", "start": "2026-10-14T09:00:00Z",
				"freeze": map[string]interface{}{"actors": "agents", "createdBy": by, "actor": actor}}}
	}
	pause := func(by string, actor map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "checkout", "namespace": "payments"},
			"spec": map[string]interface{}{"paused": true,
				"lastChange": map[string]interface{}{"action": "Pause", "by": by, "actor": actor}}}
	}
	unpaused := map[string]interface{}{"metadata": map[string]interface{}{"name": "checkout", "namespace": "payments"},
		"spec": map[string]interface{}{}}
	reject := func(by string, actor map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "checkout-v1", "namespace": "payments"},
			"spec": map[string]interface{}{"pipeline": "checkout",
				"rejected": map[string]interface{}{"by": by, "reason": "bad", "actor": actor}}}
	}
	unrejected := map[string]interface{}{"metadata": map[string]interface{}{"name": "checkout-v1", "namespace": "payments"},
		"spec": map[string]interface{}{"pipeline": "checkout"}}
	for _, tc := range []struct {
		policy string
		obj    func(string, map[string]interface{}) map[string]interface{}
		prior  map[string]interface{}
	}{
		{"freezes", freeze, nil},
		{"hold-writes", pause, unpaused},
		{"bundle-rejection", reject, unrejected},
	} {
		vap := identityPolicy(t, tc.policy).DeepCopy()
		for i, v := range vap.Spec.Variables {
			if v.Name == "mayHold" {
				// The CEL authorizer is the API server's (cel-go has none);
				// a pause adds no hold, so its answer does not matter here.
				vap.Spec.Variables[i].Expression = "true"
			}
		}
		for name, c := range map[string]struct {
			user, kind string
			want       bool
		}{
			"a person, unverified":         {"alice", "unverified", true},
			"a person, human":              {"alice", "human", true},
			"a person, bot":                {"alice", "bot", false},
			"a ServiceAccount, unverified": {sa, "unverified", false},
			"a ServiceAccount, bot":        {sa, "bot", true},
		} {
			assert.Equal(t, c.want, admits(t, vap, tc.obj(c.user, stamp(c.kind, c.user)), tc.prior, c.user), "%s: %s", tc.policy, name)
		}
	}
}
