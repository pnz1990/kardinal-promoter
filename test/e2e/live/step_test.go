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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	checkPromoteCommit(t, pushed[0], bundle, "test")
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
	assert.Equal(t, []string{prHead(bundle, "prod")}, heads, "only the pr-review environment opens a PR")
	onBranch, err := gitserver.Commits(ctx, e.Git, a.repo, prHead(bundle, "prod"), 1)
	require.NoError(t, err)
	require.NotEmpty(t, onBranch, "the PR branch has commits")
	checkPromoteCommit(t, onBranch[0], bundle, "prod")
	assert.Empty(t, a.commitsSince(t, a.repo.Branch, pushed[0].SHA), "pr-review does not push to %s", a.repo.Branch)
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
	checkPromoteCommit(t, pushed[0], bundle, "test")
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
// Only git errors are injected: an SCM API fault would need the controller's
// SCM endpoint, which every test of the suite shares.
//
// Covers STEP-RETRY-01.
func TestStep_RetriesTransientGitErrors(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	u, err := url.Parse(a.repo.CloneURL)
	require.NoError(t, err)
	gitHost, gitPort := u.Hostname(), u.Port()
	u.Host = "git-proxy." + a.ns + ".svc.cluster.local:" + gitPort
	p := a.pipeline(nil)
	p.Spec.Git.URL = u.String()
	base := a.headSHA(t, a.repo.Branch)
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "a git-clone retry", retrying(t, "git-clone", nil))
	assert.Contains(t, ps.Status.Message, "no such host")
	assert.GreaterOrEqual(t, ps.Status.RetryCount, 1, "status.retryCount")

	port, err := strconv.Atoi(gitPort)
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "git-proxy", Namespace: a.ns},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: gitHost,
			Ports: []corev1.ServicePort{{Name: "http", Port: int32(port)}}},
	}))
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, imageSteps("kustomize-set-image", false))
	pushed := a.commitsSince(t, a.repo.Branch, base)
	require.Len(t, pushed, 1, "the retried promotion pushes once")
	checkPromoteCommit(t, pushed[0], bundle, "test")
	a.running(t, "test", imageV2, "test after the retries")
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
	e.RunningPod(t, a.ns, "app.kubernetes.io/name=slow-git")

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
	assert.Contains(t, ps.Status.Message, "context deadline exceeded")

	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", 8*time.Minute)
	gaveUp := time.Since(created)
	assert.True(t, strings.HasSuffix(ps.Status.Message, "(gave up after 5 retries)"), "message: %s", ps.Status.Message)
	assert.Contains(t, ps.Status.Message, "context deadline exceeded")
	assert.GreaterOrEqual(t, gaveUp, 270*time.Second, "five retries back off 10s+20s+40s+80s+2m")
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
func promoteMessage(bundle, env string) string {
	return fmt.Sprintf("[kardinal] Promote %s to %s\n\nBundle: %s\nPipeline: %s", bundle, env, bundle, pipelineName)
}

// checkPromoteCommit checks c is the promotion commit of bundle to env.
func checkPromoteCommit(t *testing.T, c gitserver.Commit, bundle, env string) {
	t.Helper()
	assert.Equal(t, promoteMessage(bundle, env), strings.TrimSpace(c.Message), "commit %s message", c.SHA)
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
