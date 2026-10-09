//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigsyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestStep_StateMachine records every state a promotion's PromotionSteps pass
// through, from a watch: the auto test step goes Pending, Promoting,
// HealthChecking, Verified; the pr-review prod step also waits in
// WaitingForMerge until its PR is merged. Each Verified step lists the
// documented step sequence in status.steps, all Completed.
//
// Covers STEP-SM-01.
func TestStep_StateMachine(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	states := e.RecordStepStates(t, a.ns)
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	a.merge(t, a.openPR(t, bundle, "prod"))
	prod := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	checkStates(t, "test", states.States(bundle, "test"), "Promoting", "HealthChecking", "Verified")
	checkStates(t, "prod", states.States(bundle, "prod"), "Promoting", "WaitingForMerge", "HealthChecking", "Verified")
	checkSteps(t, test, imageSteps("kustomize-set-image", false))
	checkSteps(t, prod, imageSteps("kustomize-set-image", true))
}

// TestStep_AutoPushAndCommitFormat checks how each approval mode writes to
// git (docs/pr-evidence.md). auto pushes exactly one commit straight to the
// base branch and opens no PR. pr-review pushes the same kind of commit to
// kardinal/<bundle>/<env>, opens a PR titled "[kardinal] Promote <bundle> to
// <env>" into the base branch, and leaves the base branch alone until the PR
// is merged. Both commits read "[kardinal] Promote <bundle> to <env>" with
// the Bundle and Pipeline trailers, by kardinal-promoter.
//
// Covers STEP-AUTO-01, STEP-COMMIT-01.
func TestStep_AutoPushAndCommitFormat(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	a.running(t, "test", imageV2, "test after its auto promotion")
	pushed := a.commitsSince(t, a.repo.Branch, base)
	require.Len(t, pushed, 1, "auto pushes one commit to %s", a.repo.Branch)
	checkPromoteCommit(t, pushed[0], a.ns, bundle, "test")
	a.fileHas(t, "test", fixtures.V2, "auto commits to the base branch")

	pr := a.openPR(t, bundle, "prod")
	assert.Equal(t, a.repo.Branch, pr.Base, "the PR targets the base branch")
	assert.Equal(t, fmt.Sprintf("[kardinal] Promote %s to prod", bundle), pr.Title, "PR title")
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	var heads []string
	for _, p := range prs {
		heads = append(heads, p.Head)
	}
	assert.Equal(t, []string{prHead(a.ns, bundle, "prod")}, heads, "only the pr-review environment opens a PR")
	onBranch, err := gitserver.Commits(ctx, e.Git, a.repo, prHead(a.ns, bundle, "prod"), 1)
	require.NoError(t, err)
	require.NotEmpty(t, onBranch, "the PR branch has commits")
	checkPromoteCommit(t, onBranch[0], a.ns, bundle, "prod")
	assert.Empty(t, a.commitsSince(t, a.repo.Branch, pushed[0].SHA), "pr-review does not push to %s", a.repo.Branch)
	a.fileHas(t, "prod", fixtures.V1, "prod before the merge")

	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	a.fileHas(t, "prod", fixtures.V2, "prod after the merge")
	a.running(t, "prod", imageV2, "prod after the merge")
}

// TestStep_DefaultBaseBranch (B99): a Pipeline that omits spec.git.branch
// promotes to main. The API server defaults the field to main, and the
// pr-review step opens its PR into main; once the PR is merged the step
// reaches Verified. Without the default, open-pr sent an empty base and the
// SCM refused the PR, so the step never reached WaitingForMerge.
//
// Covers STEP-BASEBRANCH-01.
func TestStep_DefaultBaseBranch(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	require.Equal(t, "main", a.repo.Branch, "the test repository's default branch")
	p := a.pipeline(map[string]string{"prod": "pr-review"})
	p.Spec.Git.Branch = "" // omitted: the field is omitempty
	a.apply(t, p)
	assert.Equal(t, "main", p.Spec.Git.Branch, "the API server defaults spec.git.branch")
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	assert.Equal(t, "main", pr.Base, "the PR targets main")
	a.fileHas(t, "prod", fixtures.V1, "prod before the merge")

	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	a.fileHas(t, "prod", fixtures.V2, "prod after the merge")
	a.running(t, "prod", imageV2, "prod after the merge")
}

// TestStep_KustomizeImages promotes one Bundle through four kustomizations
// and checks the images list kustomize-set-image leaves in each
// (docs/pipeline-reference.md, "How kustomize-set-image matches images"):
//   - bare has no images key: it gets one full-repository entry;
//   - legacy has the entry older kardinal versions wrote (name podinfo,
//     newName the repository) and manifests that use "podinfo": that entry
//     gets the new tag, and a full-repository entry is added;
//   - short has a short-name entry without newName, inert because the
//     manifests use the full name: it gets newName and the tag;
//   - other has a short-name entry whose newName is another image, used by a
//     sidecar: it is left alone, and the sidecar keeps its image.
//
// Every environment then runs the new podinfo.
//
// Covers STEP-KUST-01, STEP-KUST-02.
func TestStep_KustomizeImages(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	envs := []string{"bare", "legacy", "short", "other"}
	images := map[string]string{
		"bare":   "",
		"legacy": fmt.Sprintf("images:\n  - name: podinfo\n    newName: %s\n    newTag: %s\n", fixtures.Image, fixtures.V1),
		"short":  fmt.Sprintf("images:\n  - name: podinfo\n    newTag: %s\n", fixtures.V1),
		"other":  fmt.Sprintf("images:\n  - name: podinfo\n    newName: %s\n    newTag: %q\n", fixtures.Pause, fixtures.PauseV1),
	}
	manifestImage := map[string]string{"bare": imageV1, "legacy": "podinfo", "short": imageV1, "other": imageV1}
	a := newArgoAppFiles(t, e, func(ns string) map[string][]byte {
		files := map[string][]byte{}
		for _, env := range envs {
			dir := fixtures.Path(env)
			files[dir+"/kustomization.yaml"] = []byte(fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: %s
resources:
  - deployment.yaml
  - service.yaml
%s`, ns, images[env]))
			d := fixtures.Deployment(fixtures.Workload(env), manifestImage[env])
			if env == "other" {
				d += sidecar("podinfo")
			}
			files[dir+"/deployment.yaml"] = []byte(d)
			files[dir+"/service.yaml"] = []byte(fixtures.Service(fixtures.Workload(env)))
		}
		return files
	}, envs...)
	sidecarImage := fixtures.Pause + ":" + fixtures.PauseV1
	assert.Equal(t, sidecarImage, a.containerImages(t, "other")["sidecar"], "the sidecar runs the image other's podinfo entry names")

	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	for _, env := range envs {
		e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	}

	full := map[string]interface{}{"name": fixtures.Image, "newTag": fixtures.V2}
	alias := map[string]interface{}{"name": "podinfo", "newName": fixtures.Image, "newTag": fixtures.V2}
	want := map[string][]map[string]interface{}{
		"bare":   {full},
		"legacy": {alias, full},
		"short":  {alias, full},
		"other":  {{"name": "podinfo", "newName": fixtures.Pause, "newTag": fixtures.PauseV1}, full},
	}
	for _, env := range envs {
		assert.Equal(t, want[env], a.kustomizeImages(t, env), "%s: images in kustomization.yaml", env)
		a.running(t, env, imageV2, env+" after the promotion")
	}
	assert.Equal(t, sidecarImage, a.containerImages(t, "other")["sidecar"], "other's sidecar keeps its image")
}

// TestStep_HelmValues promotes a Helm chart that Argo CD renders with an
// extra values file. With update.strategy helm, helm-set-image writes the tag
// at update.helm.imagePathTemplate in update.helm.valuesFile, leaves the
// chart's values.yaml alone, and the new version runs.
//
// Covers STEP-HELM-01.
func TestStep_HelmValues(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test"},
		repo: e.Repo(t, ns, fixtures.HelmRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))}
	e.ArgoAppSpec(t, a.argoApp("test"), map[string]interface{}{
		"project": "default",
		"source": map[string]interface{}{
			"repoURL":        a.repo.CloneURL,
			"targetRevision": a.repo.Branch,
			"path":           fixtures.Path("test"),
			"helm":           map[string]interface{}{"valueFiles": []interface{}{fixtures.HelmValuesFile}},
		},
		"destination": map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": ns},
		"syncPolicy": map[string]interface{}{
			"automated": map[string]interface{}{"prune": true, "selfHeal": true},
		},
	}, false)
	e.WaitArgoApp(t, a.argoApp("test"), syncTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), imageV1, syncTimeout)
	defaults := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/values.yaml")

	p := a.pipeline(nil)
	envSpec(t, p, "test").Update = v1alpha1.UpdateConfig{Strategy: "helm", Helm: &v1alpha1.HelmUpdateConfig{
		ValuesFile: fixtures.HelmValuesFile, ImagePathTemplate: fixtures.HelmImagePath}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)

	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, imageSteps("helm-set-image", false))
	var values struct {
		App struct {
			Image struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"image"`
		} `json:"app"`
	}
	raw := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/"+fixtures.HelmValuesFile)
	require.NoError(t, sigsyaml.Unmarshal([]byte(raw), &values), "parse %s:\n%s", fixtures.HelmValuesFile, raw)
	assert.Equal(t, fixtures.V2, values.App.Image.Tag, "%s pins the new tag at %s", fixtures.HelmValuesFile, fixtures.HelmImagePath)
	assert.Equal(t, fixtures.Image, values.App.Image.Repository, "the repository value is untouched")
	assert.Equal(t, defaults, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/values.yaml"),
		"the chart's values.yaml is untouched")
	a.running(t, "test", imageV2, "test after the promotion")
}

// TestStep_YAMLUpdate promotes plain manifests (a kustomization without an
// images list) with update.strategy yaml: yaml-update writes the image
// reference at spec.template.spec.containers[name=podinfo].image (a list
// element picked by its name) in deployment.yaml and the tag at
// .release.version (a leading "." as in chartVersionPath) in a second file,
// in one commit, and the new version runs. A later edit that cannot be applied (a list element that does
// not exist) fails the step for good, and nothing is pushed: both files keep
// the previous release.
//
// Covers STEP-YAML-01, STEP-YAML-02.
func TestStep_YAMLUpdate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	plain := func(ns string) map[string][]byte {
		files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}})
		k := fixtures.Path("test") + "/kustomization.yaml"
		files[k] = []byte(strings.Split(string(files[k]), "images:")[0])
		files[fixtures.Path("test")+"/release.yaml"] = []byte("# written by kardinal\nrelease:\n  version: " + fixtures.V1 + "\n")
		return files
	}
	a := newArgoAppFiles(t, e, plain, "test")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), imageV1, syncTimeout)

	updates := []v1alpha1.YAMLUpdate{
		{File: "deployment.yaml", Path: "spec.template.spec.containers[name=podinfo].image", Value: "image"},
		{File: "release.yaml", Path: ".release.version", Image: fixtures.Image},
	}
	p := a.pipeline(nil)
	envSpec(t, p, "test").Update = v1alpha1.UpdateConfig{Strategy: "yaml", YAML: &v1alpha1.YAMLUpdateConfig{Updates: updates}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, imageSteps("yaml-update", false))
	dep := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/deployment.yaml")
	assert.Contains(t, dep, "image: "+imageV2, "deployment.yaml has the new image")
	release := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/release.yaml")
	assert.Equal(t, "# written by kardinal\nrelease:\n  version: "+fixtures.V2+"\n", release, "release.yaml has the new tag, comment kept")
	a.running(t, "test", imageV2, "test after the promotion")

	// A path that cannot be applied fails the step, with nothing pushed.
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	broken := append([]v1alpha1.YAMLUpdate{}, updates...)
	broken = append(broken, v1alpha1.YAMLUpdate{File: "deployment.yaml", Path: "spec.template.spec.containers[3].image"})
	envSpec(t, &live, "test").Update.YAML.Updates = broken
	require.NoError(t, e.Client.Update(context.Background(), &live))
	bad := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	ps = e.WaitStepState(t, a.ns, pipelineName, bad, "test", "Failed", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "containers has 1 elements, no [3]")
	assert.Equal(t, dep, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/deployment.yaml"), "nothing pushed")
	assert.Equal(t, release, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/release.yaml"), "nothing pushed")
	a.running(t, "test", imageV2, "test keeps the previous release")
}

// TestStep_GitAuth makes the GitOps repo private, so cloning needs
// credentials, and points the Pipeline's git.secretRef at a Secret holding a
// wrong token. git-clone is refused and the step retries (status.message
// "retrying in 10s (1/5) after error: step git-clone: ..."), with no token
// in the message and nothing pushed. Writing the right token into the Secret
// lets the next retry clone and push over HTTP, and the promotion finishes,
// as docs/troubleshooting.md says for a rotated token. The kind suite's git
// server serves HTTP only, so HTTPS is not exercised.
//
// Covers STEP-GITAUTH-01.
func TestStep_GitAuth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	token := secretToken(t, e, a.ns, framework.GitSecretName)
	wrong := randomHex(t, 20)
	redact := strings.NewReplacer(token, "<git-token>", wrong, "<wrong-token>").Replace

	// Argo CD keeps syncing the private repo with its own credentials.
	argoCreds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: a.ns + "-repo", Namespace: framework.ArgoCDNamespace,
			Labels: map[string]string{"argocd.argoproj.io/secret-type": "repository", "kardinal.io/e2e": "true"}},
		StringData: map[string]string{"type": "git", "url": a.repo.CloneURL, "username": "x-access-token", "password": token},
	}
	if err := e.Client.Create(ctx, argoCreds); err != nil {
		t.Fatalf("create the Argo CD repository Secret: %v", err)
	}
	e.DeleteOnCleanup(t, &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{"name": argoCreds.Name, "namespace": argoCreds.Namespace}}}, false)
	require.NoError(t, gitserver.SetPrivate(ctx, e.Git, a.repo, true), "make the repo private")

	auth := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-auth", Namespace: a.ns},
		Data: map[string][]byte{"token": []byte(wrong)}}
	require.NoError(t, e.Client.Create(ctx, auth))
	p := a.pipeline(nil)
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: auth.Name}
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "a git-clone retry", retrying(t, "git-clone", redact))
	msg := ps.Status.Message
	assert.Regexp(t, `(?i)authentication required|authorization failed|401`, redact(msg), "git-clone is refused")
	assert.GreaterOrEqual(t, ps.Status.RetryCount, 1, "status.retryCount")
	if strings.Contains(msg, wrong) || strings.Contains(msg, token) {
		t.Errorf("the step message contains a token")
	}
	assert.Equal(t, base, a.headSHA(t, a.repo.Branch), "nothing is pushed with a wrong token")

	// The operator rotates the token.
	require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(auth), auth))
	auth.Data = map[string][]byte{"token": []byte(token)}
	require.NoError(t, e.Client.Update(ctx, auth))
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, imageSteps("kustomize-set-image", false))
	pushed := a.commitsSince(t, a.repo.Branch, base)
	require.Len(t, pushed, 1, "the promotion commit is pushed with the rotated token")
	checkPromoteCommit(t, pushed[0], a.ns, bundle, "test")
	a.running(t, "test", imageV2, "test after the token rotation")

	events, err := e.Events(ctx, a.ns, "PromotionStep", ps.Name)
	require.NoError(t, err)
	for _, ev := range events {
		if strings.Contains(ev.Note, wrong) || strings.Contains(ev.Note, token) {
			t.Errorf("event %s (%s) contains a token", ev.Name, ev.Reason)
		}
	}
	for _, s := range ps.Status.Steps {
		if strings.Contains(s.Message, wrong) || strings.Contains(s.Message, token) {
			t.Errorf("status.steps %s message contains a token", s.Name)
		}
	}
}

// TestStep_RetriesTransientGitErrors points the Pipeline's git.url at a host
// that does not resolve yet. git-clone fails with "no such host" and the step
// retries with backoff instead of failing. Once the host exists (an
// ExternalName Service to the git server), a retry clones and pushes, and the
// promotion finishes.
//
// The environment has a gate re-evaluated every 10s, and each evaluation
// reconciles the step. The step still waits out each delay: its second retry
// comes 10s after the first and its third 20s after the second, not at the
// next evaluation (B87: each evaluation ran a retry, so the five retries ran
// in about 40s and the step failed).
//
// Only git errors are injected: an SCM API fault would need the controller's
// SCM endpoint, which every test of the suite shares.
//
// Covers STEP-RETRY-01, STEP-RETRY-02.
func TestStep_RetriesTransientGitErrors(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	e.CreateGate(t, framework.Gate(a.ns, "open", "test", "true", recheck))
	p := a.pipeline(nil)
	resolve := a.unresolvedGit(t, p)
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "a git-clone retry", retrying(t, "git-clone", nil))
	assert.Contains(t, ps.Status.Message, "no such host")
	assert.GreaterOrEqual(t, ps.Status.RetryCount, 1, "status.retryCount")

	// When the step's status first shows each retry, up to the third.
	seen := map[int]time.Time{1: time.Now()}
	framework.Eventually(t, 2*time.Minute, "the third git-clone retry", func(ctx context.Context) (bool, string) {
		got, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("step: ok=%v err=%v", ok, err)
		}
		ps = got
		if ps.Status.State != "Promoting" {
			t.Fatalf("step %s turned %s while it retried: %s", ps.Name, ps.Status.State, ps.Status.Message)
		}
		n := ps.Status.RetryCount
		if _, ok := seen[n]; !ok {
			seen[n] = time.Now()
			t.Logf("retry %d seen after %s: %s", n, seen[n].Sub(seen[1]).Round(time.Second), ps.Status.Message)
		}
		return n >= 3, fmt.Sprintf("state=%q retryCount=%d message=%q", ps.Status.State, n, ps.Status.Message)
	})
	require.NotNil(t, ps.Status.NextRetryAt, "status.nextRetryAt of a step that retries: %s", ps.Status.Message)
	require.Contains(t, seen, 2, "retries 2 and 3 ran between two polls")
	assert.GreaterOrEqual(t, seen[2].Sub(seen[1]), 7*time.Second, "the first retry waits its 10s")
	assert.GreaterOrEqual(t, seen[3].Sub(seen[2]), 16*time.Second, "the second retry waits its 20s")
	g, ok, err := e.GateInstance(context.Background(), a.ns, bundle, "test", "open")
	require.NoError(t, err)
	require.True(t, ok, "gate instance open")
	require.NotNil(t, g.Status.LastEvaluatedAt, "gate open evaluated")
	assert.True(t, g.Status.LastEvaluatedAt.After(seen[2]),
		"the gate was re-evaluated while the step waited for its third retry: last at %s, second retry seen at %s",
		g.Status.LastEvaluatedAt, seen[2].UTC().Format(time.RFC3339))

	resolve()
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, imageSteps("kustomize-set-image", false))
	pushed := a.commitsSince(t, a.repo.Branch, base)
	require.Len(t, pushed, 1, "the retried promotion pushes once")
	checkPromoteCommit(t, pushed[0], a.ns, bundle, "test")
	a.running(t, "test", imageV2, "test after the retries")
}

// TestStep_SupersededCloseRetriesWithBackoff supersedes a Bundle whose test
// PR is open while the repository is archived. Forgejo closes the PR of an
// archived repository but refuses to delete its head branch (423), so the
// close fails and the superseded step keeps its state and retries it with
// backoff, with the SupersededCloseFailed condition True and
// status.nextRetryAt set (docs/concepts.md, Bundle supersession).
//
// The test annotates the step at every poll, as any write to the step, its
// gates, its PRStatus or its Bundle wakes it. The step still waits out each
// delay: its second retry comes 10s after the first and its third 20s after
// the second (B89: each wake ran a retry, so the five retries ran in seconds
// and the step failed with the branch left, which keeps the closed PR
// mergeable on GitHub). Once the repository is unarchived, a retry deletes
// the branch, and the step fails with "promotion cancelled" and loses the
// condition. The PR is commented on at most once: the comment is best-effort,
// and Forgejo may refuse it on the archived repository.
//
// Covers BUNDLE-SUPERSEDE-05.
func TestStep_SupersededCloseRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	archiver, ok := e.Git.(gitserver.Archiver)
	require.True(t, ok, "the %s git server cannot archive a repo", e.Git.Kind())
	brancher, ok := e.Git.(gitserver.Brancher)
	require.True(t, ok, "the %s git server cannot read branches", e.Git.Kind())
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	older := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, older, "test", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, older, "test")

	require.NoError(t, archiver.SetArchived(ctx, a.repo, true))
	t.Cleanup(func() {
		if err := archiver.SetArchived(context.Background(), a.repo, false); err != nil {
			t.Errorf("unarchive %s: %v", a.repo.Name, err)
		}
	})
	e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitBundlePhase(t, a.ns, older, "Superseded", promoteTimeout)
	ps := e.WaitStep(t, a.ns, pipelineName, older, "test", time.Minute, "a failed close", func(ps *v1alpha1.PromotionStep) (bool, string) {
		return ps.Status.RetryCount >= 1 && strings.Contains(ps.Status.Message, "closing its PR failed"),
			fmt.Sprintf("state=%q retryCount=%d message=%q", ps.Status.State, ps.Status.RetryCount, ps.Status.Message)
	})
	assert.Equal(t, "WaitingForMerge", ps.Status.State, "the step keeps its state while it retries the close")

	// When the step's status first shows each close retry, up to the third.
	seen := map[int]time.Time{ps.Status.RetryCount: time.Now()}
	first := ps.Status.RetryCount
	step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: ps.Name, Namespace: a.ns}}
	framework.Eventually(t, 2*time.Minute, "the third close retry", func(ctx context.Context) (bool, string) {
		annotate(t, e, step)
		got, ok, err := e.Step(ctx, a.ns, pipelineName, older, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("step: ok=%v err=%v", ok, err)
		}
		ps = got
		if ps.Status.State == "Failed" {
			t.Fatalf("step %s turned Failed while it retried the close: %s", ps.Name, ps.Status.Message)
		}
		n := ps.Status.RetryCount
		if _, ok := seen[n]; !ok {
			seen[n] = time.Now()
			t.Logf("close retry %d seen after %s: %s", n, seen[n].Sub(seen[first]).Round(time.Second), ps.Status.Message)
		}
		return n >= 3, fmt.Sprintf("state=%q retryCount=%d message=%q", ps.Status.State, n, ps.Status.Message)
	})
	require.Equal(t, 1, first, "the first failed close is retry 1")
	require.Contains(t, seen, 2, "close retries 2 and 3 ran between two polls")
	assert.GreaterOrEqual(t, seen[2].Sub(seen[1]), 7*time.Second, "the first close retry waits its 10s")
	assert.GreaterOrEqual(t, seen[3].Sub(seen[2]), 16*time.Second, "the second close retry waits its 20s")
	require.NotNil(t, ps.Status.NextRetryAt, "status.nextRetryAt of a step that retries the close: %s", ps.Status.Message)
	ok, cond := framework.CondIs(ps.Status.Conditions, "SupersededCloseFailed", metav1.ConditionTrue, "CloseFailed")
	assert.True(t, ok, "SupersededCloseFailed: %s", cond)
	assert.Contains(t, ps.Status.Message, "deleting its branch "+prHead(a.ns, older, "test")+" failed")
	e.WaitPRState(t, a.repo, pr.Number, "closed", time.Second)
	_, err := brancher.BranchHead(ctx, a.repo, prHead(a.ns, older, "test"))
	require.NoError(t, err, "the branch is kept while the repository is archived")

	require.NoError(t, archiver.SetArchived(ctx, a.repo, false))
	ps = e.WaitStepState(t, a.ns, pipelineName, older, "test", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "promotion cancelled")
	assert.NotContains(t, ps.Status.Message, "by hand")
	assert.Nil(t, meta.FindStatusCondition(ps.Status.Conditions, "SupersededCloseFailed"), "the condition once the close is done")
	assert.Nil(t, ps.Status.NextRetryAt, "status.nextRetryAt of a Failed step")
	_, err = brancher.BranchHead(ctx, a.repo, prHead(a.ns, older, "test"))
	assert.Error(t, err, "the branch is deleted")
	comments := e.PRComments(t, a.repo, pr.Number, "kardinal closed this PR: bundle "+older+" was superseded")
	assert.LessOrEqual(t, len(comments), 1, "the PR is commented on at most once")
}

// TestStep_ApprovalEditMidPromotion edits an environment's approval while its
// step retries git-clone (git.url points at a host that does not resolve yet),
// then lets the clone through. The step runs the step list it recorded when it
// started, so the edit applies from the next Bundle (docs/pipeline-reference.md,
// approval):
//   - pr-review edited to auto: the change is pushed to kardinal/<bundle>/<env>
//     only, the PR opens, and the step holds the close-pr finalizer while it
//     waits for the merge; after the merge it is Verified with the pr-review
//     steps;
//   - auto edited to pr-review: the change is pushed straight to the base
//     branch, no PR is opened, and the step and the Bundle are Verified with
//     the auto steps.
//
// Either way the Bundle in flight finishes: its Graph, rebuilt in place by the
// edit, turns Ready, and the environment's gate is not evaluated after that
// (B69: the auto step's PRStatus node waited for a merge, so GraphReady stayed
// False and the gate was evaluated at every recheckInterval for good). Only
// auto-to-pr-review catches B69: after pr-review-to-auto the environment is
// auto, whose PRStatus node had no readyWhen before the fix either.
//
// The edit lands while git-clone retries, not open-pr: an SCM API fault would
// need the controller's SCM endpoint, which every test of the suite shares.
//
// Covers STEP-APPROVAL-EDIT-01, STEP-APPROVAL-EDIT-02.
func TestStep_ApprovalEditMidPromotion(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, from, to string }{
		{"pr-review-to-auto", "pr-review", "auto"},
		{"auto-to-pr-review", "auto", "pr-review"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := framework.New(t)
			a := newArgoApp(t, e, "test")
			e.CreateGate(t, framework.Gate(a.ns, "open", "test", "true", recheck))
			p := a.pipeline(map[string]string{"test": c.from})
			resolve := a.unresolvedGit(t, p)
			base := a.headSHA(t, a.repo.Branch)
			a.apply(t, p)
			bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
			e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "a git-clone retry", retrying(t, "git-clone", nil))

			a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) { envSpec(t, p, "test").Approval = c.to })
			resolve()

			prReview := c.from == "pr-review"
			if prReview {
				e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
				pr := a.openPR(t, bundle, "test")
				assert.Contains(t, stepFinalizers(t, e, a.ns, bundle, "test"), closePRFinalizer, "the step with the open PR")
				assert.Empty(t, a.commitsSince(t, a.repo.Branch, base), "nothing is pushed to %s before the merge", a.repo.Branch)
				a.merge(t, pr)
			}
			ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
			checkSteps(t, ps, imageSteps("kustomize-set-image", prReview))
			e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
			a.running(t, "test", imageV2, "test after the promotion")
			if !prReview {
				pushed := a.commitsSince(t, a.repo.Branch, base)
				require.Len(t, pushed, 1, "auto pushes one commit to %s", a.repo.Branch)
				checkPromoteCommit(t, pushed[0], a.ns, bundle, "test")
				prs, err := e.Git.PullRequests(context.Background(), a.repo)
				require.NoError(t, err)
				assert.Empty(t, prs, "no PR is opened")
			}
			a.settled(t, bundle, "test", "open")
		})
	}
}

// TestStep_NoChangesPRReview promotes, through a pr-review environment, the
// image the environment already runs. git-commit finds nothing to commit, so
// git-push, open-pr and wait-for-merge do nothing (docs/pipeline-reference.md,
// Promotion Steps): the step goes Promoting, HealthChecking, Verified, with
// every pr-review step Completed, no PR and no commit. The Bundle is Verified,
// its Graph turns Ready, and the environment's gate is not evaluated after
// that (B69: the PRStatus node waited for a merge of a PR that was never
// opened, so GraphReady stayed False and the gate was evaluated for good).
//
// Covers STEP-NOCHANGE-01.
func TestStep_NoChangesPRReview(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	states := e.RecordStepStates(t, a.ns)
	e.CreateGate(t, framework.Gate(a.ns, "open", "test", "true", recheck))
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV1)

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkStates(t, "test", states.States(bundle, "test"), "Promoting", "HealthChecking", "Verified")
	checkSteps(t, ps, imageSteps("kustomize-set-image", true))
	assert.Empty(t, ps.Status.PRURL, "status.prURL")
	assert.Equal(t, "true", ps.Status.Outputs["noChanges"], "status.outputs.noChanges")
	assert.Equal(t, base, a.headSHA(t, a.repo.Branch), "nothing is pushed to %s", a.repo.Branch)
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs, "no PR is opened")
	a.running(t, "test", imageV1, "test after the promotion")
	a.settled(t, bundle, "test", "open")
}

// settled checks that bundle finishes: it is Verified with GraphReady True,
// and for 30 seconds the instance of gate for env is not evaluated after the
// test saw both (docs/policy-gates.md, gates of finished Bundles). The gate's
// recheckInterval is shorter than that. The reference is the time the test saw
// both, not GraphReady's lastTransitionTime: GraphReady can turn True before
// the phase is Verified, and the gate is still evaluated in between. An
// evaluation that started before the Bundle settled may still land, and
// lastEvaluatedAt has second precision, so 2 seconds of slack are allowed.
func (a *app) settled(t *testing.T, bundle, env, gate string) {
	t.Helper()
	a.e.WaitBundle(t, a.ns, bundle, 2*time.Minute, "Verified with GraphReady True", func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "GraphReady", metav1.ConditionTrue, "")
		return ok && b.Status.Phase == "Verified", fmt.Sprintf("phase=%s %s", b.Status.Phase, seen)
	})
	ready := time.Now()
	framework.Consistently(t, 30*time.Second, "gate "+gate+" of "+bundle+" not evaluated after it settled", func(ctx context.Context) (bool, string) {
		g, ok, err := a.e.GateInstance(ctx, a.ns, bundle, env, gate)
		switch {
		case err != nil:
			return false, err.Error()
		case !ok || g.Status.LastEvaluatedAt == nil:
			return false, fmt.Sprintf("gate %s of %s not evaluated", gate, bundle)
		}
		at := g.Status.LastEvaluatedAt.Time
		return !at.After(ready.Add(2 * time.Second)),
			fmt.Sprintf("gate %s evaluated at %s, settled since %s", g.Name, at.Format(time.RFC3339), ready.Format(time.RFC3339))
	})
}

// unresolvedGit points p's git.url at git-proxy.<ns>.svc.cluster.local, a host
// that does not resolve yet, so git-clone fails with "no such host" and
// retries. The returned func creates the host: an ExternalName Service to the
// git server.
func (a *app) unresolvedGit(t *testing.T, p *v1alpha1.Pipeline) func() {
	t.Helper()
	u, err := url.Parse(a.repo.CloneURL)
	require.NoError(t, err)
	gitHost, gitPort := u.Hostname(), u.Port()
	port, err := strconv.Atoi(gitPort)
	require.NoError(t, err)
	u.Host = "git-proxy." + a.ns + ".svc.cluster.local:" + gitPort
	p.Spec.Git.URL = u.String()
	return func() {
		t.Helper()
		require.NoError(t, a.e.Client.Create(context.Background(), &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "git-proxy", Namespace: a.ns},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: gitHost,
				Ports: []corev1.ServicePort{{Name: "http", Port: int32(port)}}},
		}))
	}
}

// TestStep_Timeout points the Pipeline's git.url at a server that answers
// after 30 seconds, with stepTimeoutSeconds 2. git-clone is cancelled after
// 2 seconds ("context deadline exceeded"), well before the server answers,
// and the timeout is handled like any step error (docs/pipeline-reference.md):
// the step retries with backoff (10s, 20s, 40s, 80s, 2m), then turns Failed
// with "(gave up after 5 retries)", the Bundle fails, and nothing is pushed.
//
// Covers STEP-TIMEOUT-01.
func TestStep_Timeout(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	deployment, service := fixtures.SlowGitServer("slow-git")
	for _, doc := range []string{deployment, service} {
		obj := &unstructured.Unstructured{}
		require.NoError(t, sigsyaml.Unmarshal([]byte(doc), &obj.Object))
		obj.SetNamespace(a.ns)
		if _, err := e.Apply(ctx, obj); err != nil {
			t.Fatalf("apply %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
	e.WaitDeploymentImage(t, a.ns, "slow-git", fixtures.Image+":"+fixtures.V1, 2*time.Minute)
	waitServiceEndpoint(t, e, a.ns, "slow-git", time.Minute)

	p := a.pipeline(nil)
	p.Spec.Git.URL = fmt.Sprintf("http://slow-git.%s.svc.cluster.local:9898%s%s/%s.git",
		a.ns, fixtures.SlowGitPath, a.repo.Owner, a.repo.Name)
	envSpec(t, p, "test").StepTimeoutSeconds = 2
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "a git-clone retry", retrying(t, "git-clone", nil))
	created := ps.CreationTimestamp.Time
	firstRetry := time.Since(created)
	assert.Less(t, firstRetry, 20*time.Second, "the first attempt is cut at 2s, not after the server's 30s")
	if !strings.Contains(ps.Status.Message, "context deadline exceeded") {
		// kube-proxy can route the Service a few seconds after its endpoint
		// is ready; until then a connection is refused. Later attempts hang.
		assert.Contains(t, ps.Status.Message, "connection refused")
		t.Logf("the first attempt was refused before the Service was routed: %s", ps.Status.Message)
	}

	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", 8*time.Minute)
	gaveUp := time.Since(created)
	assert.True(t, strings.HasSuffix(ps.Status.Message, "(gave up after 5 retries)"), "message: %s", ps.Status.Message)
	assert.Contains(t, ps.Status.Message, "context deadline exceeded")
	assert.GreaterOrEqual(t, gaveUp, 270*time.Second, "five retries back off 10s+20s+40s+80s+2m")
	assert.Less(t, gaveUp, 6*time.Minute, "each attempt is cut at 2s: six 30s attempts would take at least 7m30s")
	clone, ok := stepEntry(ps, "git-clone")
	assert.True(t, ok && clone.State == v1alpha1.StepExecutionFailed, "git-clone entry: %+v", clone)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Equal(t, base, a.headSHA(t, a.repo.Branch), "nothing is pushed")
	t.Logf("first retry after %s, Failed after %s", firstRetry.Round(time.Second), gaveUp.Round(time.Second))
}

// TestStep_OrphansCleanedUp deletes a Bundle with `kardinal delete bundle`
// while prod waits for its PR to merge: the Bundle's Graph, PromotionSteps,
// PRStatuses and gate instances are deleted with it. A PromotionStep whose
// Bundle does not exist (created by hand here) deletes itself without
// touching git.
//
// Covers STEP-ORPHAN-01.
func TestStep_OrphansCleanedUp(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	a.openPR(t, bundle, "prod")
	graph := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, graph, "the Bundle has a Graph")
	require.NotZero(t, a.children(t, bundle), "the Graph created children")

	out := e.MustKardinal(t, a.ns, "delete", "bundle", bundle)
	assert.Contains(t, out, bundle)
	framework.Eventually(t, 2*time.Minute, "the Graph and its children are gone", func(ctx context.Context) (bool, string) {
		_, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, graph, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Graph %s: err=%v", graph, err)
		}
		n := a.children(t, bundle)
		return n == 0, fmt.Sprintf("%d children left", n)
	})
	head := a.headSHA(t, a.repo.Branch)
	orphan := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-test", Namespace: a.ns, Labels: map[string]string{
			"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": "gone", "kardinal.io/environment": "test"}},
		Spec: v1alpha1.PromotionStepSpec{PipelineName: pipelineName, BundleName: "gone", Environment: "test",
			StepType: "kustomize-set-image"},
	}
	require.NoError(t, e.Client.Create(ctx, orphan))
	framework.Eventually(t, time.Minute, "the orphaned PromotionStep deletes itself", func(ctx context.Context) (bool, string) {
		var ps v1alpha1.PromotionStep
		err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: orphan.Name}, &ps)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		return false, fmt.Sprintf("err=%v state=%q message=%q", err, ps.Status.State, ps.Status.Message)
	})
	assert.Equal(t, head, a.headSHA(t, a.repo.Branch), "the orphan pushes nothing")
}

// waitServiceEndpoint waits until the EndpointSlices of Service svc in ns list
// a ready endpoint.
func waitServiceEndpoint(t *testing.T, e *framework.Env, ns, svc string, timeout time.Duration) {
	t.Helper()
	framework.Eventually(t, timeout, fmt.Sprintf("Service %s/%s has a ready endpoint", ns, svc), func(ctx context.Context) (bool, string) {
		list, err := e.Kube.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=" + svc})
		if err != nil {
			return false, err.Error()
		}
		for _, sl := range list.Items {
			for _, ep := range sl.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf("%d EndpointSlices, no ready endpoint", len(list.Items))
	})
}

// children counts the objects a Bundle's Graph creates: PromotionSteps,
// PRStatuses and PolicyGate instances labelled with the Bundle.
func (a *app) children(t *testing.T, bundle string) int {
	t.Helper()
	ctx := context.Background()
	sel := client.MatchingLabels{"kardinal.io/bundle": bundle}
	var steps v1alpha1.PromotionStepList
	var prs v1alpha1.PRStatusList
	var gates v1alpha1.PolicyGateList
	for _, l := range []client.ObjectList{&steps, &prs, &gates} {
		if err := a.e.Client.List(ctx, l, client.InNamespace(a.ns), sel); err != nil {
			t.Fatalf("list children of %s: %v", bundle, err)
		}
	}
	return len(steps.Items) + len(prs.Items) + len(gates.Items)
}

// imageSteps is the documented status.steps sequence of an image Bundle whose
// environment updates manifests with update (docs/pipeline-reference.md,
// Promotion Steps).
func imageSteps(update string, prReview bool) []string {
	s := []string{"git-clone", update, "git-commit", "git-push"}
	if prReview {
		s = append(s, "open-pr", "wait-for-merge")
	}
	return append(s, "health-check")
}

// checkSteps checks that ps ran exactly want, in order, and all completed.
func checkSteps(t *testing.T, ps *v1alpha1.PromotionStep, want []string) {
	t.Helper()
	var names []string
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
		if s.State != v1alpha1.StepExecutionCompleted {
			t.Errorf("%s: step %s is %s (%s), want Completed", ps.Spec.Environment, s.Name, s.State, s.Message)
		}
	}
	assert.Equal(t, want, names, "%s: status.steps", ps.Spec.Environment)
}

// checkStates checks a recorded state sequence: the step starts without a
// state or Pending, then passes through exactly want.
func checkStates(t *testing.T, env string, got []string, want ...string) {
	t.Helper()
	rest := got
	for len(rest) > 0 && (rest[0] == "(none)" || rest[0] == "Pending") {
		rest = rest[1:]
	}
	if len(rest) == len(got) || !slices.Equal(rest, want) {
		t.Errorf("%s step states: %s; want (none) or Pending, then %s", env, framework.JoinStates(got), framework.JoinStates(want))
	}
}

// retrying is a WaitStep check for the first retry of step: status.message
// "retrying in 10s (1/5) after error: step <step>: ...". The test fails at
// once if the PromotionStep finishes first. redact, when set, cleans the
// message for the failure output.
func retrying(t *testing.T, step string, redact func(string) string) func(*v1alpha1.PromotionStep) (bool, string) {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	return func(ps *v1alpha1.PromotionStep) (bool, string) {
		msg := redact(ps.Status.Message)
		if ps.Status.State == "Failed" || ps.Status.State == "Verified" {
			t.Fatalf("step %s turned %s before retrying: %s", ps.Name, ps.Status.State, msg)
		}
		return strings.Contains(ps.Status.Message, "retrying in 10s (1/5) after error: step "+step+": "),
			fmt.Sprintf("state=%q retryCount=%d message=%q", ps.Status.State, ps.Status.RetryCount, msg)
	}
}

// promoteMessage is the commit message of a promotion (docs/pr-evidence.md).
func promoteMessage(ns, bundle, env string) string {
	return fmt.Sprintf("[kardinal] Promote %s to %s\n\nBundle: %s\nPipeline: %s\nNamespace: %s", bundle, env, bundle, pipelineName, ns)
}

// checkPromoteCommit checks c is the promotion commit of bundle to env.
func checkPromoteCommit(t *testing.T, c gitserver.Commit, ns, bundle, env string) {
	t.Helper()
	assert.Equal(t, promoteMessage(ns, bundle, env), strings.TrimSpace(c.Message), "commit %s message", c.SHA)
	assert.Equal(t, "kardinal-promoter", c.AuthorName, "commit %s author", c.SHA)
	assert.Equal(t, "kardinal@kardinal.io", c.AuthorEmail, "commit %s author email", c.SHA)
}

// headSHA returns the newest commit of branch.
func (a *app) headSHA(t *testing.T, branch string) string {
	t.Helper()
	c, err := gitserver.Commits(context.Background(), a.e.Git, a.repo, branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, c, "%s has commits", branch)
	return c[0].SHA
}

// commitsSince returns the commits of branch newer than base, newest first.
func (a *app) commitsSince(t *testing.T, branch, base string) []gitserver.Commit {
	t.Helper()
	all, err := gitserver.Commits(context.Background(), a.e.Git, a.repo, branch, 20)
	require.NoError(t, err)
	for i, c := range all {
		if c.SHA == base {
			return all[:i]
		}
	}
	t.Fatalf("%s has no commit %s among its newest 20", branch, base)
	return nil
}

// kustomizeImages returns the images list of env's kustomization.yaml on the
// default branch.
func (a *app) kustomizeImages(t *testing.T, env string) []map[string]interface{} {
	t.Helper()
	raw := a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml")
	var k struct {
		Images []map[string]interface{} `json:"images"`
	}
	if err := sigsyaml.Unmarshal([]byte(raw), &k); err != nil {
		t.Fatalf("parse %s kustomization.yaml: %v\n%s", env, err, raw)
	}
	return k.Images
}

// containerImages maps container name to image in env's Deployment.
func (a *app) containerImages(t *testing.T, env string) map[string]string {
	t.Helper()
	d, err := a.e.Kube.AppsV1().Deployments(a.ns).Get(context.Background(), fixtures.Workload(env), metav1.GetOptions{})
	require.NoError(t, err)
	out := map[string]string{}
	for _, c := range d.Spec.Template.Spec.Containers {
		out[c.Name] = c.Image
	}
	return out
}

// sidecar is a containers list item to append to fixtures.Deployment.
func sidecar(image string) string {
	return fmt.Sprintf(`        - name: sidecar
          image: %s
          resources:
            requests:
              cpu: 1m
              memory: 4Mi
`, image)
}

// secretToken reads key token of Secret name in ns. Never log it.
func secretToken(t *testing.T, e *framework.Env, ns, name string) string {
	t.Helper()
	s, err := e.Kube.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read Secret %s/%s: %v", ns, name, err)
	}
	tok := strings.TrimSpace(string(s.Data["token"]))
	if tok == "" {
		t.Fatalf("Secret %s/%s has no token", ns, name)
	}
	return tok
}

// randomHex returns n random bytes, hex-encoded.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}
