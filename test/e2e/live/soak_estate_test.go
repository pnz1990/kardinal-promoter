//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// soakTenant is one team of the day: a fleet's namespace, its own git user
// (every push of its Pipelines uses that user's token, a collaborator on
// the team's repos only), its registry prefix and its Pipelines.
type soakTenant struct {
	name  string // a, b or c
	fleet *scale.Fleet
	ns    string
	// user is the team's Forgejo user; token is its token (never logged).
	user, token string
	// prefix is the team's registry path prefix, e2e/team-<name>/; image
	// its registry image as a Bundle records it; digests that image's
	// copies of framework.SeedTags.
	prefix, image string
	digests       []string
	// load are the Pipelines CI and people create Bundles for; pipelines
	// every Pipeline of the team, with its repo.
	load      []string
	pipelines []string
	repos     map[string]gitserver.Repo
	// evidence is the Pipeline that promotes the team's registry image by
	// digest ("" for none): its receipts get OCI referrers.
	evidence string

	// Team a: the shipper agent's CLI and Pipeline, the verifier agent, and
	// the kubeconfig of the person who approves prod.
	agent         *framework.CLI
	agentPipeline string
	verifier      string
	approver      string
	// Team c: the podinfo release whose test environment rolls back, and
	// the 31-environment fleet.
	podinfo *app
	fleet30 string
}

// soakEnvs are the environments of every team Pipeline but the fleet.
func soakEnvs() []v1alpha1.EnvironmentSpec {
	return []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
}

// Sponsors and people of the day. Every party differs, so the one-party
// rule never hides a vote the soak expects to count.
const (
	soakAgentSponsor    = "sponsor-a@example.com"
	soakVerifierSponsor = "qa-sponsor@example.com"
	soakApprover        = "approver@team-a.example.com"
	soakMallory         = "mallory@team-a.example.com"
	soakQALead          = "qa-lead@team-a.example.com"
	soakRogueUser       = "rogue@agents.example.com"
	soakRogueSponsor    = "rogue-sponsor@example.com"
)

// estate builds the day's namespaces, gates, change requests, agents and
// Pipelines (section 1 of #2403's design).
func (s *soakDay) estate() {
	t, e := s.t, s.e
	began := time.Now()
	s.a, s.b, s.c = s.tenant("a"), s.tenant("b"), s.tenant("c")
	for _, tn := range []*soakTenant{s.a, s.b} {
		for _, tag := range framework.SeedTags {
			tn.digests = append(tn.digests, s.reg.Copy(t, tag, tn.prefix+"app", tag))
		}
	}

	// The org's shared gates, and a namespace that has not consented.
	s.org, s.private = e.Namespace(t), e.Namespace(t)
	labelNamespace(t, e, s.org, v1alpha1.LabelPolicyShareable, "true")
	e.CreateGate(t, framework.Gate(s.org, "org-policy", "prod", `bundle.provenance.author != "blocked@example.com"`, recheck))
	e.CreateGate(t, framework.Gate(s.private, "private-policy", "prod", "true", recheck))

	s.teamA()
	s.teamB()
	s.teamC()
	s.intruderProbe()
	s.note("setupSeconds", int(time.Since(began).Seconds()))
	t.Logf("soak estate: team a %s, team b %s, team c %s, org %s, private %s, intruder %s (set up in %s)",
		s.a.ns, s.b.ns, s.c.ns, s.org, s.private, s.intruder, time.Since(began).Round(time.Second))
}

// tenant creates team name's fleet namespace and git user, and makes the
// user's token the namespace's git Secret.
func (s *soakDay) tenant(name string) *soakTenant {
	t, e := s.t, s.e
	f := scale.NewFleet(t, e, scale.NewRNG(s.seed, "team-"+name))
	tn := &soakTenant{name: name, fleet: f, ns: f.NS, prefix: "e2e/team-" + name + "/", repos: map[string]gitserver.Repo{}}
	tn.image = s.reg.Repository(tn.prefix + "app")
	tn.user = "soak-" + name + "-" + f.NS[len(f.NS)-8:]
	tn.token = e.GitUser(t, tn.user, []string{"write:repository", "write:issue"})
	e.SetSecretValue(t, f.NS, framework.GitSecretName, "token", []byte(tn.token))
	return tn
}

// pipeline creates Pipeline name of team tn over a repo of its own (files,
// or the scale overlays of envs), the team's user a collaborator on it.
// track records it in the team's fleet, for the scale invariants; a
// Pipeline whose overlays are not the scale image's is not tracked.
func (s *soakDay) pipeline(tn *soakTenant, name string, envs []v1alpha1.EnvironmentSpec, track bool,
	files map[string][]byte, edits ...func(*v1alpha1.Pipeline)) *v1alpha1.Pipeline {
	t, e := s.t, s.e
	if files == nil {
		files = scale.RepoFiles(map[string][]string{name: scale.Names(envs)})
	}
	repo := e.Repo(t, tn.ns+"-"+name, files)
	require.NoError(t, e.GitUsers(t).AddCollaborator(context.Background(), repo, tn.user))
	p := tn.fleet.PipelineSpec(name, repo, envs, edits...)
	if p.Spec.HistoryLimit == 0 {
		p.Spec.HistoryLimit = soakHistory
	}
	if track {
		tn.fleet.Apply(t, p, repo)
	} else {
		require.NoError(t, e.Client.Create(context.Background(), p))
	}
	tn.repos[name] = repo
	tn.pipelines = append(tn.pipelines, name)
	return p
}

// evidencePipeline creates the team's Pipeline of its registry image: its
// overlays name tn.image, which its Bundles pin by digest.
func (s *soakDay) evidencePipeline(tn *soakTenant, edits ...func(*v1alpha1.Pipeline)) {
	name := tn.name + "-evidence"
	files := map[string][]byte{}
	for _, env := range soakEnvs() {
		files[scale.EnvPath(name, env.Name)+"/kustomization.yaml"] = []byte(fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
  - name: %s
    newTag: %s
`, tn.image, scale.SeedTag))
	}
	s.pipeline(tn, name, soakEnvs(), false, files, edits...)
	tn.evidence = name
}

// teamA: a shipper agent, a verifier agent counted twice by prod's verify
// gate, a ServiceNow change request and a person's approval naming its
// ticket on prod.
func (s *soakDay) teamA() {
	t, e := s.t, s.e
	a := s.a
	ctx := context.Background()
	a.verifier = registerAgent(t, e, a.ns, "qa", soakVerifierSponsor, map[string]string{"PROBE_DELAY": "2s"})
	// allowedUsers names a QA lead nobody acts as: without it every
	// person's verdict would count (verify.allowedUsers).
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 2, AllowedAgents: []string{a.verifier},
		AllowedUsers: []string{soakQALead}, Run: &v1alpha1.GateVerifyRun{Template: a.verifier, Count: 2, Namespace: a.ns}}))
	cred := e.AddFakeTicketUser(t, a.ns, randomHex(t, 12))
	e.CreateSecretData(t, a.ns, "snow", true, map[string]string{"token": cred})
	changeTemplate(t, e, a.ns, "prod-change", "snow", framework.TicketAPIURL(t))
	g := framework.Gate(a.ns, "cab", "prod", `changeRequests["prod-change"].state == "approved" && `+
		`approvals.fields.ticketID.exists(id, id == changeRequests["prod-change"].ticket)`, recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Description: "ServiceNow change number"}}}
	e.CreateGate(t, g)

	a.agent = policyAgent(t, e, a.ns, "shipper", soakAgentSponsor, v1alpha1.AutonomyAuto)
	// policyAgent's token lasts an hour; the day outlasts it.
	tr, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, "shipper", &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To(int64((s.d + 3*time.Hour).Seconds()))}}, metav1.CreateOptions{})
	require.NoError(t, err)
	a.agent.Kubeconfig = tokenKubeconfig(t, e, tr.Status.Token)
	a.approver = userKubeconfig(t, e, a.ns, soakApprover)
	s.asApprover = impersonated(t, e, soakApprover)
	// Mallory, a person with the verifier role, and the rogue agent, a
	// registered agent the forged-verdicts episode suspends.
	s.malloryKubeconfig = userKubeconfig(t, e, a.ns, soakMallory)
	bindRole(t, e, a.ns, soakMallory, verifierRole)
	s.rogue = "rogue-" + a.ns
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: s.rogue},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: soakRogueUser}},
			Sponsor: v1alpha1.AgentSponsor{User: soakRogueSponsor}, Scope: v1alpha1.AgentScope{Namespaces: []string{a.ns}},
			Autonomy: v1alpha1.AutonomyApprove}}))
	t.Cleanup(func() {
		var cur v1alpha1.AgentIdentity
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: s.rogue}, &cur); err != nil {
			return
		}
		cur.Spec.Suspended = true
		_ = e.Client.Update(context.Background(), &cur)
		_ = e.Client.Delete(context.Background(), &cur)
	})
	bindRole(t, e, a.ns, soakRogueUser, verifierRole)
	waitIndexed(t, e, soakRogueUser, func(en v1alpha1.AgentIndexEntry) bool { return en.Agent == s.rogue && en.Blocked == "" })
	s.asRogue = impersonated(t, e, soakRogueUser)

	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("a-%03d", i)
		s.pipeline(a, name, soakEnvs(), true, nil)
		a.load = append(a.load, name)
	}
	a.agentPipeline = "a-agent"
	s.pipeline(a, a.agentPipeline, soakEnvs(), true, nil)
	s.evidencePipeline(a)
}

// teamB: PR review on prod (scale.Review merges), a Jira change request on
// prod, and the org's gates through policyNamespaces.
func (s *soakDay) teamB() {
	t, e := s.t, s.e
	b := s.b
	cred := e.AddFakeTicketUser(t, b.ns, randomHex(t, 12))
	e.CreateSecretData(t, b.ns, "jira", true, map[string]string{"token": cred})
	require.NoError(t, e.Client.Create(context.Background(), &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-change", Namespace: b.ns},
		Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, AppliesTo: []string{"prod"}, Provider: "jira", PollInterval: "30s",
			Jira: &v1alpha1.JiraChangeRequest{BaseURL: framework.TicketAPIURL(t), Project: "CHG", IssueType: "Change",
				ApprovedStatuses:  []string{"Approved"},
				ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "jira", Key: "token"}},
				Template:          map[string]string{"summary": "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}"}}},
	}))
	e.CreateGate(t, framework.Gate(b.ns, "cab", "prod", `changeRequests["prod-change"].state == "approved"`, recheck))
	reviewed := func(p *v1alpha1.Pipeline) {
		for i := range p.Spec.Environments {
			if p.Spec.Environments[i].Name == "prod" {
				p.Spec.Environments[i].Approval = "pr-review"
			}
		}
		p.Spec.PolicyNamespaces = []string{s.org}
	}
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("b-%03d", i)
		s.pipeline(b, name, soakEnvs(), true, nil, reviewed)
		b.load = append(b.load, name)
	}
	s.evidencePipeline(b, reviewed)
}

// teamC: the 31-environment fleet in the compact Graph shape, two plain
// Pipelines, and the podinfo release (Argo CD) whose test environment rolls
// back by itself when a rollout fails.
func (s *soakDay) teamC() {
	t, e := s.t, s.e
	c := s.c
	c.fleet30 = "fleet30"
	s.pipeline(c, c.fleet30, scale.Waves(3, 10), true, nil, func(p *v1alpha1.Pipeline) {
		p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	})
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("c-%03d", i)
		s.pipeline(c, name, soakEnvs(), true, nil)
		c.load = append(c.load, name)
	}

	envs := []string{"test", "uat", "prod"}
	repo := e.Repo(t, c.ns+"-podinfo", fixtures.KustomizeRepo(fixtures.App{Namespace: c.ns, Envs: envs}))
	require.NoError(t, e.GitUsers(t).AddCollaborator(context.Background(), repo, c.user))
	c.podinfo = &app{e: e, ns: c.ns, envs: envs, repo: repo}
	for _, env := range envs {
		e.ArgoApp(t, c.podinfo.argoApp(env), repo, fixtures.Path(env), c.ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, c.podinfo.argoApp(env), syncTimeout)
		e.WaitDeploymentImage(t, c.ns, fixtures.Workload(env), imageV1, syncTimeout)
	}
	p := c.podinfo.resourcePipeline(nil)
	test := envSpec(t, p, "test")
	test.OnHealthFailure = "rollback"
	test.Health.Timeout = "5m"
	p.Spec.HistoryLimit = soakHistory
	c.podinfo.apply(t, p)
	c.repos[pipelineName] = repo
	c.pipelines = append(c.pipelines, pipelineName)
}

// intruderProbe points a Pipeline of a fourth namespace at team a's repo
// with team c's token, which may not push there, and creates its version:
// the push must be refused for the whole day (the isolation invariant).
func (s *soakDay) intruderProbe() {
	t, e := s.t, s.e
	s.intruder = e.Namespace(t)
	e.SetSecretValue(t, s.intruder, framework.GitSecretName, "token", []byte(s.c.token))
	target := s.a.load[0]
	repo := s.a.repos[target]
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "steal", Namespace: s.intruder},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch, SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}},
			Environments: []v1alpha1.EnvironmentSpec{{
				Name: "test", Path: scale.EnvPath(target, "test"), Approval: "auto",
				Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
				Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m",
					Resource: &v1alpha1.ResourceRef{Name: scale.HealthDeployment, Namespace: s.intruder}},
			}},
		},
	}
	require.NoError(t, e.Client.Create(context.Background(), p))
	s.intruderBundle = e.CreateBundle(t, s.intruder, "steal", "--image", scale.ImageRepo+":steal-0001")
}

// bindRole binds user to ClusterRole role in ns.
func bindRole(t *testing.T, e *framework.Env, ns, user, role string) {
	t.Helper()
	name := role + "-" + strings.NewReplacer("@", "-", ":", "-", ".", "-").Replace(user)
	_, err := e.Kube.RbacV1().RoleBindings(ns).Create(context.Background(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, ns, name)
}

// labelNamespace sets label key on namespace ns.
func labelNamespace(t require.TestingT, e *framework.Env, ns, key, value string) {
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var n corev1.Namespace
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: ns}, &n); err != nil {
			return err
		}
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[key] = value
		return e.Client.Update(context.Background(), &n)
	}))
}

// tenants are the three teams.
func (s *soakDay) tenants() []*soakTenant { return []*soakTenant{s.a, s.b, s.c} }

// owns reports whether pipeline is one of tn's.
func (tn *soakTenant) owns(pipeline string) bool {
	_, ok := tn.repos[pipeline]
	return ok
}
