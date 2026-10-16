//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// agentIndexName is the AgentIndex of the suite's release.
const agentIndexName = framework.ControllerDeployment

// liveAgent is an agent registered for one test: a ServiceAccount in ns,
// bound to the chart's promoter and approver roles in the namespaces rbac,
// and an AgentIdentity (cluster-scoped, named after the namespace) whose
// scope is the namespace ns and the environments envs (nil: all).
type liveAgent struct {
	name, ns, sa, sponsor string
	// client acts as the ServiceAccount (impersonated); cli runs kardinal
	// with a token of it.
	client client.Client
	cli    *framework.CLI
}

func (a liveAgent) username() string { return "system:serviceaccount:" + a.ns + ":" + a.sa }

// stamp is the actor stamp the agent must write.
func (a liveAgent) stamp() *v1alpha1.Actor {
	return &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: a.username(), Agent: a.name, Sponsor: a.sponsor}
}

func newLiveAgent(t *testing.T, e *framework.Env, ns string, rbac, environments []string) liveAgent {
	t.Helper()
	ctx := context.Background()
	a := liveAgent{name: "e2e-" + ns, ns: ns, sa: "fixer", sponsor: "sponsor-" + ns + "@example.com"}
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: a.sa}}, metav1.CreateOptions{})
	require.NoError(t, err)
	for _, where := range rbac {
		for _, role := range []string{"promoter", "approver"} {
			_, err = e.Kube.RbacV1().RoleBindings(where).Create(ctx, &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-" + role, Namespace: where},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: framework.ControllerDeployment + "-" + role},
				Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: a.sa, Namespace: ns}},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			waitBoundNamed(t, e, where, "agent-"+role)
		}
	}
	id := &v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: a.name},
		Spec: v1alpha1.AgentIdentitySpec{
			Description: "e2e agent of " + t.Name(),
			Subjects:    []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: ns, Name: a.sa}},
			Sponsor:     v1alpha1.AgentSponsor{User: a.sponsor},
			Scope:       v1alpha1.AgentScope{Namespaces: []string{ns}, Environments: environments},
			// auto: its versions promote under the other gates; the default,
			// approve, makes every environment wait for a person
			// (docs/agentic/policy.md).
			Autonomy: v1alpha1.AutonomyAuto,
		},
	}
	require.NoError(t, e.Client.Create(ctx, id))
	t.Cleanup(func() {
		// Suspend, then delete: the agent-identities policy refuses a
		// delete of an AgentIdentity that is not suspended.
		var cur v1alpha1.AgentIdentity
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: a.name}, &cur); err != nil {
			return
		}
		cur.Spec.Suspended = true
		_ = e.Client.Update(context.Background(), &cur)
		_ = e.Client.Delete(context.Background(), &cur)
	})
	waitIndexed(t, e, a.username(), func(en v1alpha1.AgentIndexEntry) bool { return en.Agent == a.name && en.Blocked == "" })

	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: a.username()}
	a.client, err = client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)

	tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, a.sa, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](3600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	a.cli = e.CLI(t)
	a.cli.Kubeconfig = tokenKubeconfig(t, e, tr.Status.Token)
	return a
}

// tokenKubeconfig writes a kubeconfig that authenticates with token alone.
func tokenKubeconfig(t *testing.T, e *framework.Env, token string) string {
	t.Helper()
	tls := e.Config.TLSClientConfig
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kind"] = &clientcmdapi.Cluster{Server: e.Config.Host, CertificateAuthority: tls.CAFile,
		CertificateAuthorityData: tls.CAData, TLSServerName: tls.ServerName, InsecureSkipTLSVerify: tls.Insecure}
	cfg.AuthInfos["agent"] = &clientcmdapi.AuthInfo{Token: token}
	// The context has the harness's name: the CLI runner passes --context.
	cfg.Contexts[e.Context] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "agent"}
	cfg.CurrentContext = e.Context
	path := filepath.Join(t.TempDir(), "agent-kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*cfg, path))
	return path
}

// waitIndexed waits until the release's AgentIndex maps user to an entry ok
// accepts.
func waitIndexed(t *testing.T, e *framework.Env, user string, ok func(v1alpha1.AgentIndexEntry) bool) {
	t.Helper()
	framework.Eventually(t, time.Minute, "the agent index lists "+user, func(ctx context.Context) (bool, string) {
		var idx v1alpha1.AgentIndex
		if err := e.Client.Get(ctx, types.NamespacedName{Name: agentIndexName}, &idx); err != nil {
			return false, err.Error()
		}
		en, found := idx.Status.Users[user]
		return found && ok(en), fmt.Sprintf("%+v", en)
	})
}

func actorBundle(ns string, stamp *v1alpha1.Actor) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{GenerateName: pipelineName + "-", Namespace: ns},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, Actor: stamp},
	}
}

// TestAudit_ActorAdmission checks the chart's actor policies on a real API
// server, against the AgentIndex the controller builds: a person's stamp
// must name them (a forged principal is refused) and kardinal create bundle
// stamps them human; a registered agent's ServiceAccount must stamp, with its
// sponsor (missing or forged stamps are refused, with the stamp to write),
// and kardinal stamps it agent with its sponsor and its asserted model; an
// unregistered ServiceAccount is a bot and cannot pass as a person; the
// agent is refused outside its scope (another namespace, an Approval for an
// environment it may not approve in) by the CLI before it writes and by the
// policy; only the controller sets via (the Bundle API's static token is
// stamped bot bundle-api via api) or writes an AuditEvent that names an
// actor; an AgentIdentity is deleted only once suspended, and a suspension
// takes effect within 10 seconds.
//
// Covers ACTOR-ADMIT-01, ACTOR-ADMIT-02, ACTOR-ADMIT-03, ACTOR-ADMIT-04, ACTOR-ADMIT-05, ACTOR-REG-01.
func TestAudit_ActorAdmission(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns, other := e.Namespace(t), e.Namespace(t)
	pausedApp(t, e, ns, "test")
	pausedApp(t, e, other, "test")
	// RBAC lets the agent write in both namespaces; its scope is ns only.
	agent := newLiveAgent(t, e, ns, []string{ns, other}, []string{"test"})
	me := whoAmI(t, e)

	// A person.
	err := e.Client.Create(ctx, actorBundle(ns, &v1alpha1.Actor{Kind: "human", Principal: "bob@example.com"}))
	require.Error(t, err, "a stamp in someone else's name is refused")
	assert.Contains(t, err.Error(), "actor must be {kind: human, principal: "+me+"}")
	b := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	var got v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: b}, &got))
	assert.Equal(t, &v1alpha1.Actor{Kind: "human", Principal: me}, got.Spec.Actor, "kardinal stamps a person human")
	require.NoError(t, e.Client.Create(ctx, actorBundle(ns, nil)), "a person may leave the stamp out")

	// The agent.
	err = agent.client.Create(ctx, actorBundle(ns, nil))
	require.Error(t, err, "an agent must stamp")
	assert.Contains(t, err.Error(), fmt.Sprintf("you are agent %s (sponsor %s)", agent.name, agent.sponsor))
	forged := agent.stamp()
	forged.Sponsor = "mallory@example.com"
	err = agent.client.Create(ctx, actorBundle(ns, forged))
	require.Error(t, err, "a forged sponsor is refused")
	assert.Contains(t, err.Error(), "sponsor: "+agent.sponsor)
	err = agent.client.Create(ctx, actorBundle(ns, &v1alpha1.Actor{Kind: "human", Principal: agent.username()}))
	require.Error(t, err, "an agent cannot pass as a person")
	require.NoError(t, agent.client.Create(ctx, actorBundle(ns, agent.stamp())))
	out := agent.cli.Exec(framework.CLIOptions{Env: []string{"KARDINAL_ACTOR_MODEL=e2e-model", "KARDINAL_ACTOR_SESSION=s-1"}},
		agent.cli.Args(ns, "create", "bundle", pipelineName, "--image", imageV2)...)
	require.Zero(t, out.Code, out.Output())
	name := bundleCreatedName(t, out.Stdout)
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	want := agent.stamp()
	want.Asserted = &v1alpha1.AssertedActor{Model: "e2e-model", Session: "s-1"}
	assert.Equal(t, want, got.Spec.Actor, "kardinal stamps the agent with its sponsor and what it asserts")

	// Out of scope.
	res := agent.cli.Run(other, "create", "bundle", pipelineName, "--image", imageV2)
	require.NotZero(t, res.Code, "the CLI refuses an agent outside its scope")
	assert.Contains(t, res.Output(), fmt.Sprintf("agent %s may not act in namespace %s", agent.name, other))
	err = agent.client.Create(ctx, actorBundle(other, agent.stamp()))
	require.Error(t, err, "so does the policy")
	assert.Contains(t, err.Error(), "may not act here (namespace "+other)
	assert.Len(t, bundles(t, e, other), 0)
	approval := func(env string) *v1alpha1.Approval {
		return &v1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "a-", Namespace: ns,
				Labels: map[string]string{"kardinal.io/bundle": name, "kardinal.io/environment": env}},
			Spec: v1alpha1.ApprovalSpec{Bundle: name, BundleUID: string(got.UID), Environment: env, User: agent.username(),
				Decision: "approve", Actor: agent.stamp()},
		}
	}
	err = agent.client.Create(ctx, approval("prod"))
	require.Error(t, err, "an approval in an environment outside the scope is refused")
	assert.Contains(t, err.Error(), "environment prod")
	require.NoError(t, agent.client.Create(ctx, approval("test")))

	// An unregistered ServiceAccount is a bot.
	bot := impersonating(t, e, ns, "system:serviceaccount:"+ns+":builder", nil, []string{"bundles"}, "create")
	err = bot.Create(ctx, actorBundle(ns, &v1alpha1.Actor{Kind: "human", Principal: "system:serviceaccount:" + ns + ":builder"}))
	require.Error(t, err, "a ServiceAccount cannot pass as a person")
	assert.Contains(t, err.Error(), "actor must be {kind: bot")
	require.NoError(t, bot.Create(ctx, actorBundle(ns, &v1alpha1.Actor{Kind: "bot", Principal: "system:serviceaccount:" + ns + ":builder"})))

	// Only the controller sets via, or names an actor in an AuditEvent.
	err = e.Client.Create(ctx, actorBundle(ns, &v1alpha1.Actor{Kind: "human", Principal: me, Via: "ui"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "with no via")
	apiName, _ := created(t, framework.PostBundle(t, framework.ControllerURL(t), "Bearer "+framework.BundleToken(t),
		ciBody(t, ns, fixtures.V2, nil)))
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: apiName}, &got))
	assert.Equal(t, &v1alpha1.Actor{Kind: "bot", Principal: "bundle-api", Via: "api"}, got.Spec.Actor,
		"the controller stamps the static token's Bundles")
	err = e.Client.Create(ctx, &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{GenerateName: "forged-", Namespace: ns},
		Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.Now(), BundleName: "x", Action: "APIWrite", Outcome: "Success",
			Actor: &v1alpha1.Actor{Kind: "human", Principal: "alice"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only kardinal writes an AuditEvent that names an actor")

	// Remove the agent: suspend first.
	var id v1alpha1.AgentIdentity
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: agent.name}, &id))
	err = e.Client.Delete(ctx, &id)
	require.Error(t, err, "an AgentIdentity that is not suspended cannot be deleted")
	assert.Contains(t, err.Error(), "suspend agent "+agent.name+" before deleting it")
	id.Spec.Suspended = true
	require.NoError(t, e.Client.Update(ctx, &id))
	start := time.Now()
	framework.Eventually(t, 10*time.Second, "the suspended agent is refused", func(ctx context.Context) (bool, string) {
		err := agent.client.Create(ctx, actorBundle(ns, agent.stamp()))
		return err != nil && strings.Contains(err.Error(), "may not act: suspended"), fmt.Sprint(err)
	})
	t.Logf("suspension took effect in %s", time.Since(start).Round(time.Millisecond))
	require.NoError(t, e.Client.Delete(ctx, &id))
}

// bundleCreatedName is the Bundle kardinal create bundle reports.
func bundleCreatedName(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, pipelineName+"-") {
			return strings.Trim(f, ".:,()")
		}
	}
	t.Fatalf("no Bundle name in %q", out)
	return ""
}

// TestAudit_AgentShipsWithActor: an agent ships a version with its own
// token; the version promotes through test, and its prod PR says "Created
// by: agent <name> for <sponsor>"; the promotion's AuditEvents name the
// agent; kardinal history shows it in ACTOR and kardinal why in the
// version line; kardinal audit export writes the events as JSON lines and
// as OCSF API Activity with the agent in actor.app_name.
//
// Covers ACTOR-SHOW-01, ACTOR-SHOW-02, AUDIT-EXPORT-01.
func TestAudit_AgentShipsWithActor(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	agent := newLiveAgent(t, e, a.ns, []string{a.ns}, nil)
	sentence := fmt.Sprintf("agent %s for %s", agent.name, agent.sponsor)

	out := agent.cli.Must(a.ns, "ship", pipelineName, "--image", imageV2)
	bundle := bundleCreatedName(t, out)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	assert.Contains(t, pr.Body, "\n\nCreated by: "+sentence+"\n")

	framework.Eventually(t, time.Minute, "the promotion's AuditEvents name the agent", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
			return false, err.Error()
		}
		n := 0
		for _, ev := range list.Items {
			if ev.Spec.Action == "PromotionSucceeded" || ev.Spec.Action == "PromotionStarted" {
				if ev.Spec.Actor == nil || *ev.Spec.Actor != *agent.stamp() {
					return false, fmt.Sprintf("%s: %+v", ev.Spec.Action, ev.Spec.Actor)
				}
				n++
			}
		}
		return n >= 3, fmt.Sprintf("%d events", n) // started+succeeded in test, started in prod
	})

	hist := e.MustKardinal(t, a.ns, "history", pipelineName)
	assert.Contains(t, hist, "ACTOR")
	assert.Contains(t, hist, sentence)
	// why reads the Pipeline's status, which writes a running step's state
	// up to 2s after the step (#1719): wait for prod's entry.
	framework.Eventually(t, 30*time.Second, "why names the agent", func(context.Context) (bool, string) {
		why, err := e.Kardinal(t, a.ns, "why", pipelineName, "prod")
		return err == nil && strings.Contains(why, ", by "+sentence), why
	})

	for _, format := range []string{"json", "ocsf"} {
		lines := e.MustKardinal(t, a.ns, "audit", "export", "--format", format, "--pipeline", pipelineName, "--since", "1h")
		found := false
		sc := bufio.NewScanner(strings.NewReader(lines))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var rec map[string]interface{}
			require.NoError(t, json.Unmarshal(sc.Bytes(), &rec), sc.Text())
			if format == "json" {
				spec := rec["spec"].(map[string]interface{})
				if act, ok := spec["actor"].(map[string]interface{}); ok && act["agent"] == agent.name && act["sponsor"] == agent.sponsor {
					found = true
				}
				continue
			}
			assert.Equal(t, 6003.0, rec["class_uid"])
			if act := rec["actor"].(map[string]interface{}); act["app_name"] == agent.name {
				found = true
				unm := rec["unmapped"].(map[string]interface{})["kardinal"].(map[string]interface{})
				assert.Equal(t, agent.sponsor, unm["actor"].(map[string]interface{})["sponsor"])
			}
		}
		assert.True(t, found, "%s export names the agent:\n%s", format, lines)
	}
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestAudit_APIWriteEvents (#1539): a write through the Bundle API is an
// APIWrite AuditEvent in the target's namespace that names the caller (the
// static token's holder, bot bundle-api via api), the route and the method,
// with the Bundle it created, and no token.
//
// Covers AUDIT-API-01.
func TestAudit_APIWriteEvents(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	name, _ := created(t, framework.PostBundle(t, framework.ControllerURL(t), "Bearer "+framework.BundleToken(t),
		ciBody(t, a.ns, fixtures.V2, nil)))
	var ev v1alpha1.AuditEvent
	framework.Eventually(t, time.Minute, "the APIWrite AuditEvent", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/action": "APIWrite"}); err != nil {
			return false, err.Error()
		}
		for _, x := range list.Items {
			if x.Spec.BundleName == name {
				ev = x
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d APIWrite events", len(list.Items))
	})
	assert.Equal(t, "Success", ev.Spec.Outcome)
	assert.Equal(t, pipelineName, ev.Spec.PipelineName)
	assert.Equal(t, &v1alpha1.Actor{Kind: "bot", Principal: "bundle-api", Via: "api"}, ev.Spec.Actor)
	assert.Equal(t, &v1alpha1.AuditRequest{Route: "/api/v1/bundles", Method: http.MethodPost}, ev.Spec.Request)
	assert.Equal(t, "bundle-api", ev.Labels["kardinal.io/server"])
	assert.Equal(t, "created bundle "+name, ev.Spec.Message)
	assert.NotContains(t, fmt.Sprintf("%+v", ev.Spec), framework.BundleToken(t))
}
