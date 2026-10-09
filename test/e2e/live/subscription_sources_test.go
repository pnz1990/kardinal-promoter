//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestSub_ImageFilters checks the tag filters and strategies on a real
// registry: semverConstraint keeps matching versions without pre-releases,
// allowTags and ignoreTags pick from a list, excludeTagFilter drops tags,
// strategy Lexical takes the dated tag that sorts last, and an invalid
// constraint is phase Error. Every Subscription records the digest of the
// tag it picked; a new tag that passes the constraint creates a Bundle.
//
// Covers SUB-FILTER-01.
func TestSub_ImageFilters(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	// The digest identifies the tag picked: each tag copies one seed image.
	src := map[string]string{"1.0.0": "6.13.0", "1.1.0": "6.14.0", "1.2.0-rc.1": "6.15.0", "2.0.0": "6.15.0",
		"2026-10-01": "6.13.0", "2026-10-08": "6.14.0", "1.1.0-debug": "6.15.0"}
	digest := map[string]string{}
	for tag, from := range src {
		digest[tag] = reg.Copy(t, from, repo, tag)
	}
	spec := func(change func(*v1alpha1.ImageSubscriptionSpec)) v1alpha1.SubscriptionSpec {
		s := imageSub(reg.Ref(repo), "", "30s")
		change(s.Image)
		return s
	}
	newSub(t, e, a.ns, "major-one", spec(func(i *v1alpha1.ImageSubscriptionSpec) { i.SemverConstraint = "^1.0.0" }))
	newSub(t, e, a.ns, "allow", spec(func(i *v1alpha1.ImageSubscriptionSpec) {
		i.AllowTags = []string{"1.0.0", "1.1.0", "2.0.0"}
		i.IgnoreTags = []string{"2.0.0"}
	}))
	newSub(t, e, a.ns, "exclude", spec(func(i *v1alpha1.ImageSubscriptionSpec) {
		i.TagFilter, i.ExcludeTagFilter = `^1\.`, `-rc\.|-debug$`
	}))
	newSub(t, e, a.ns, "dated", spec(func(i *v1alpha1.ImageSubscriptionSpec) {
		i.TagFilter, i.Strategy = `^\d{4}-\d{2}-\d{2}$`, v1alpha1.TagStrategyLexical
	}))
	newSub(t, e, a.ns, "bad", spec(func(i *v1alpha1.ImageSubscriptionSpec) { i.SemverConstraint = "one point oh" }))

	waitBaseline(t, e, a.ns, "major-one", digest["1.1.0"])
	waitBaseline(t, e, a.ns, "allow", digest["1.1.0"])
	waitBaseline(t, e, a.ns, "exclude", digest["1.1.0"])
	waitBaseline(t, e, a.ns, "dated", digest["2026-10-08"])
	assert.Equal(t, "1.1.0", getSub(t, e, a.ns, "major-one").Status.LastSeenTag)
	assert.Equal(t, "2026-10-08", getSub(t, e, a.ns, "dated").Status.LastSeenTag)
	assert.Contains(t, waitSubError(t, e, a.ns, "bad"), `OCIWatcher: invalid semverConstraint "one point oh"`)

	d := reg.Copy(t, "6.15.0", repo, "1.3.0")
	poke(t, e, a.ns, "major-one")
	want := "major-one-1-3-0-" + digestHex(d)[:8]
	waitSub(t, e, a.ns, "major-one", "a Bundle for 1.3.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == want
	})
	assertImageBundle(t, e, a.ns, "major-one", want, reg.Repository(repo), "1.3.0", d)
	poke(t, e, a.ns, "allow")
	s := getSub(t, e, a.ns, "allow")
	waitSub(t, e, a.ns, "allow", "a poll after 1.3.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastCheckedAt != s.Status.LastCheckedAt
	})
	assert.Equal(t, digest["1.1.0"], getSub(t, e, a.ns, "allow").Status.LastSeenDigest, "1.3.0 is not in allowTags")
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// TestSub_GitPrivate checks a private Git repository over HTTP: without
// secretRef the Subscription goes to phase Error asking for one; with a
// Secret holding a token it records the head and creates a Bundle for a new
// commit. The token never appears in the status.
//
// Covers SUB-AUTH-GIT-01.
func TestSub_GitPrivate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	ctx := context.Background()
	require.NoError(t, gitserver.SetPrivate(ctx, e.Git, a.repo, true), "make the repo private")
	token := secretToken(t, e, a.ns, framework.GitSecretName)
	git := committer(t, e)
	head, err := git.BranchSHA(ctx, a.repo)
	require.NoError(t, err)

	gitSpec := func(secret string) v1alpha1.SubscriptionSpec {
		s := v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit,
			Git: &v1alpha1.GitSubscriptionSpec{RepoURL: a.repo.CloneURL, Interval: "30s"}}
		if secret != "" {
			s.Git.SecretRef = &v1alpha1.SubscriptionSecretRef{Name: secret}
		}
		return s
	}
	newSub(t, e, a.ns, "anonymous", gitSpec(""))
	msg := waitSubError(t, e, a.ns, "anonymous")
	assert.Contains(t, msg, fmt.Sprintf("authentication required for %s (HTTP 401); the repository may be private: "+
		"set secretRef to a Secret with a token for it", a.repo.CloneURL))

	createSecret(t, e, a.ns, "repo-token", corev1.SecretTypeOpaque, map[string]string{"token": token})
	newSub(t, e, a.ns, "private", gitSpec("repo-token"))
	waitBaseline(t, e, a.ns, "private", head)
	sha := commitNote(t, git, a.repo, "private change")
	poke(t, e, a.ns, "private")
	waitSub(t, e, a.ns, "private", "a Bundle for "+sha, func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == "private-"+sha[:8]
	})
	b := getBundle(t, e, a.ns, "private-"+sha[:8])
	assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL, CommitSHA: sha}, b.Spec.ConfigRef)
	for _, name := range []string{"anonymous", "private"} {
		assert.NotContains(t, getSub(t, e, a.ns, name).Status.Message, token)
	}
}

// TestSub_GitSSH checks a Git repository over SSH (the git server's built-in
// SSH server, a read-only deploy key): with ssh-privatekey and the server's
// host key in known_hosts the Subscription records the head and creates a
// Bundle for a new commit, also with pathGlob; a known_hosts with another
// key for the host is refused.
//
// Covers SUB-AUTH-SSH-01.
func TestSub_GitSSH(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	ctx := context.Background()
	inCluster, fromHost := os.Getenv("KARDINAL_E2E_GIT_SSH"), os.Getenv("KARDINAL_E2E_GIT_SSH_API")
	require.NotEmpty(t, inCluster, "KARDINAL_E2E_GIT_SSH is not set; components/giteafamily.sh sets it")
	require.NotEmpty(t, fromHost, "KARDINAL_E2E_GIT_SSH_API is not set")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	require.NoError(t, gitserver.AddDeployKey(ctx, e.Git, a.repo, "kardinal-e2e-"+a.ns,
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))))
	hostKey := sshHostKey(t, fromHost)
	known := knownhosts.Line([]string{knownhosts.Normalize(inCluster)}, hostKey) + "\n"
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, err := ssh.NewSignerFromKey(otherPriv)
	require.NoError(t, err)
	wrongKnown := knownhosts.Line([]string{knownhosts.Normalize(inCluster)}, otherSigner.PublicKey()) + "\n"
	keyPEM := string(pem.EncodeToMemory(block))
	createSecret(t, e, a.ns, "ssh", corev1.SecretTypeSSHAuth, map[string]string{"ssh-privatekey": keyPEM, "known_hosts": known})
	createSecret(t, e, a.ns, "ssh-wrong-host", corev1.SecretTypeSSHAuth, map[string]string{"ssh-privatekey": keyPEM, "known_hosts": wrongKnown})

	repoURL := fmt.Sprintf("ssh://git@%s/%s/%s.git", inCluster, a.repo.Owner, a.repo.Name)
	sshSpec := func(secret, glob string) v1alpha1.SubscriptionSpec {
		return v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit, Git: &v1alpha1.GitSubscriptionSpec{
			RepoURL: repoURL, PathGlob: glob, Interval: "30s", SecretRef: &v1alpha1.SubscriptionSecretRef{Name: secret}}}
	}
	git := committer(t, e)
	head, err := git.BranchSHA(ctx, a.repo)
	require.NoError(t, err)
	newSub(t, e, a.ns, "over-ssh", sshSpec("ssh", ""))
	newSub(t, e, a.ns, "over-ssh-glob", sshSpec("ssh", "notes/**"))
	newSub(t, e, a.ns, "wrong-host", sshSpec("ssh-wrong-host", ""))
	waitBaseline(t, e, a.ns, "over-ssh", head)
	waitBaseline(t, e, a.ns, "over-ssh-glob", head)
	assert.Contains(t, waitSubError(t, e, a.ns, "wrong-host"), "host key of "+inCluster+" does not match known_hosts")

	sha := commitNote(t, git, a.repo, "change over ssh")
	for _, name := range []string{"over-ssh", "over-ssh-glob"} {
		poke(t, e, a.ns, name)
		want := name + "-" + sha[:8]
		waitSub(t, e, a.ns, name, "a Bundle for "+sha, func(st v1alpha1.SubscriptionStatus) bool {
			return st.LastBundleCreated == want
		})
		assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: repoURL, CommitSHA: sha}, getBundle(t, e, a.ns, want).Spec.ConfigRef)
	}
	assert.Empty(t, subBundles(t, e, a.ns, "wrong-host"))
}

// sshHostKey connects to addr and returns the host key it presents.
func sshHostKey(t *testing.T, addr string) ssh.PublicKey {
	t.Helper()
	var key ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "git", Timeout: 10 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { key = k; return nil },
		Auth:            []ssh.AuthMethod{ssh.Password("x")}}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err == nil {
		_ = c.Close()
	}
	require.NotNil(t, key, "no SSH host key from %s: %v", addr, err)
	return key
}

// helmIndex is an index.yaml with chart podinfo at versions (digest
// "d<version>").
func helmIndex(versions ...string) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nentries:\n  podinfo:\n")
	for _, v := range versions {
		fmt.Fprintf(&b, "    - name: podinfo\n      version: %s\n      digest: %s\n      urls: [podinfo-%s.tgz]\n",
			v, chartDigestHex(v), v)
	}
	return []byte(b.String())
}

func chartDigestHex(version string) string {
	sum := sha256.Sum256([]byte("podinfo-" + version))
	return hex.EncodeToString(sum[:])
}

// TestSub_HelmHTTP checks a Helm Subscription on an HTTP chart repository
// (a static index.yaml served from a private repo by the git server): without
// credentials the repository is not found; with a username/password Secret
// the first poll records the highest version, a new version in the index
// creates a chart Bundle that carries the chart, version and index digest,
// and semverConstraint keeps a Subscription on its major version.
//
// Covers SUB-HELM-01.
func TestSub_HelmHTTP(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()
	a := pausedApp(t, e, ns, "test")
	repo := a.repo
	_, err := committer(t, e).CommitFiles(ctx, repo, "chart index", map[string][]byte{
		"charts/index.yaml": helmIndex("6.13.0", "6.14.0", "7.0.0-rc.1")})
	require.NoError(t, err)
	require.NoError(t, gitserver.SetPrivate(ctx, e.Git, repo, true))
	raw, err := gitserver.RawURL(e.Git, repo, repo.Branch)
	require.NoError(t, err)
	repoURL := raw + "/charts"
	createSecret(t, e, ns, "charts", corev1.SecretTypeBasicAuth, map[string]string{
		"username": "x-access-token", "password": secretToken(t, e, ns, framework.GitSecretName)})

	helm := func(secret, constraint string) v1alpha1.SubscriptionSpec {
		s := v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeHelm, Helm: &v1alpha1.HelmSubscriptionSpec{
			RepoURL: repoURL, Chart: "podinfo", Interval: "30s",
			TagSelection: v1alpha1.TagSelection{SemverConstraint: constraint}}}
		if secret != "" {
			s.Helm.SecretRef = &v1alpha1.SubscriptionSecretRef{Name: secret}
		}
		return s
	}
	newSub(t, e, a.ns, "anonymous", helm("", ""))
	assert.Contains(t, waitSubError(t, e, a.ns, "anonymous"),
		"(HTTP 404); check repoURL, and set secretRef if the repository is private")
	newSub(t, e, a.ns, "latest", helm("charts", ""))
	newSub(t, e, a.ns, "six", helm("charts", "^6"))
	waitBaseline(t, e, a.ns, "latest", "sha256:"+chartDigestHex("7.0.0-rc.1"))
	waitBaseline(t, e, a.ns, "six", "sha256:"+chartDigestHex("6.14.0"))
	assert.Equal(t, "6.14.0", getSub(t, e, a.ns, "six").Status.LastSeenTag)

	_, err = gitserver.UpdateFile(ctx, e.Git, repo, "charts/index.yaml", "release 6.15.0",
		helmIndex("6.13.0", "6.14.0", "6.15.0", "7.0.0-rc.1"))
	require.NoError(t, err)
	poke(t, e, a.ns, "six")
	digest := "sha256:" + chartDigestHex("6.15.0")
	want := "six-6-15-0-" + digest[len("sha256:"):][:8]
	waitSub(t, e, a.ns, "six", "a Bundle for 6.15.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == want
	})
	b := getBundle(t, e, a.ns, want)
	assert.Equal(t, "chart", b.Spec.Type)
	assert.Equal(t, &v1alpha1.ChartRef{RepoURL: repoURL, Name: "podinfo", Version: "6.15.0", Digest: digest}, b.Spec.Chart)
	assert.Equal(t, "six", b.Labels["kardinal.io/subscription"])
	poke(t, e, a.ns, "latest")
	s := getSub(t, e, a.ns, "latest")
	waitSub(t, e, a.ns, "latest", "a poll after 6.15.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastCheckedAt != s.Status.LastCheckedAt
	})
	assert.Empty(t, subBundles(t, e, a.ns, "latest"), "7.0.0-rc.1 is still the highest version")
}

// TestSub_HelmOCI checks a Helm Subscription on OCI charts (pushed the way
// helm push does) in the public and in the private registry: the first poll
// records the manifest digest of the highest version, a newly pushed version
// creates a chart Bundle with that digest; the private registry is read with
// a dockerconfigjson Secret.
//
// Covers SUB-HELM-02.
func TestSub_HelmOCI(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	pub, priv := framework.NewRegistry(t), framework.NewPrivateRegistry(t)
	user, password := priv.Login()
	createSecret(t, e, a.ns, "pull", corev1.SecretTypeDockerConfigJson, map[string]string{
		corev1.DockerConfigJsonKey: fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q}}}`, priv.Host(), user, password)})
	for _, tc := range []struct {
		name   string
		reg    *framework.Registry
		secret string
	}{{"public", pub, ""}, {"private", priv, "pull"}} {
		chartRepo := a.ns + "/charts"
		first := tc.reg.PushChart(t, chartRepo, "podinfo", "6.14.0")
		repoURL := "oci+http://" + strings.TrimPrefix(tc.reg.Ref(chartRepo), "http://")
		spec := v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeHelm, Helm: &v1alpha1.HelmSubscriptionSpec{
			RepoURL: repoURL, Chart: "podinfo", Interval: "30s"}}
		if tc.secret != "" {
			spec.Helm.SecretRef = &v1alpha1.SubscriptionSecretRef{Name: tc.secret}
		}
		newSub(t, e, a.ns, tc.name, spec)
		waitBaseline(t, e, a.ns, tc.name, first)

		next := tc.reg.PushChart(t, chartRepo, "podinfo", "6.15.0")
		poke(t, e, a.ns, tc.name)
		want := tc.name + "-6-15-0-" + digestHex(next)[:8]
		waitSub(t, e, a.ns, tc.name, "a Bundle for 6.15.0", func(st v1alpha1.SubscriptionStatus) bool {
			return st.LastBundleCreated == want
		})
		b := getBundle(t, e, a.ns, want)
		assert.Equal(t, "chart", b.Spec.Type)
		assert.Equal(t, &v1alpha1.ChartRef{RepoURL: repoURL, Name: "podinfo", Version: "6.15.0", Digest: next}, b.Spec.Chart)
	}
}

// TestSub_HelmChartPromotion promotes a chart version end to end: a Helm
// Subscription on an OCI chart creates a chart Bundle for a new version, the
// Bundle's Graph runs the environment with update.strategy helm, and
// helm-set-image writes the version at update.helm.chartVersionPath in
// update.helm.chartVersionFile (a Flux HelmRelease-style file next to the
// chart). The step ends Verified and the file in git has the version.
//
// Covers SUB-HELM-03.
func TestSub_HelmChartPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	files := fixtures.HelmRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}})
	release := fixtures.Path("test") + "/release.yaml"
	files[release] = []byte("kind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: podinfo\n      version: 6.14.0\n")
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: e.Repo(t, ns, files)}
	e.ArgoAppSpec(t, a.argoApp("test"), map[string]interface{}{
		"project": "default",
		"source": map[string]interface{}{
			"repoURL": a.repo.CloneURL, "targetRevision": a.repo.Branch, "path": fixtures.Path("test"),
			"helm": map[string]interface{}{"valueFiles": []interface{}{fixtures.HelmValuesFile}},
		},
		"destination": map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": ns},
		"syncPolicy":  map[string]interface{}{"automated": map[string]interface{}{"prune": true, "selfHeal": true}},
	}, false)
	e.WaitArgoApp(t, a.argoApp("test"), syncTimeout)
	p := a.pipeline(nil)
	envSpec(t, p, "test").Update = v1alpha1.UpdateConfig{Strategy: "helm", Helm: &v1alpha1.HelmUpdateConfig{
		ChartVersionFile: "release.yaml", ChartVersionPath: ".spec.chart.spec.version"}}
	a.apply(t, p)

	reg := framework.NewRegistry(t)
	chartRepo := ns + "/charts"
	first := reg.PushChart(t, chartRepo, "podinfo", "6.14.0")
	newSub(t, e, ns, "chart", v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeHelm, Helm: &v1alpha1.HelmSubscriptionSpec{
		RepoURL: "oci+http://" + strings.TrimPrefix(reg.Ref(chartRepo), "http://"), Chart: "podinfo", Interval: "30s"}})
	waitBaseline(t, e, ns, "chart", first)
	next := reg.PushChart(t, chartRepo, "podinfo", "6.15.0")
	poke(t, e, ns, "chart")
	bundle := "chart-6-15-0-" + digestHex(next)[:8]
	waitSub(t, e, ns, "chart", "a Bundle for 6.15.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == bundle
	})

	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, "helm-set-image", ps.Spec.StepType)
	assert.Equal(t, "kind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: podinfo\n      version: 6.15.0\n",
		e.ReadFile(t, a.repo, a.repo.Branch, release), "the chart version is promoted")
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/"+fixtures.HelmValuesFile),
		fmt.Sprintf("tag: %q", fixtures.V1), "the image values are untouched")
	framework.Eventually(t, 2*time.Minute, "Bundle "+bundle+" Verified", func(ctx context.Context) (bool, string) {
		b := getBundle(t, e, ns, bundle)
		return b.Status.Phase == "Verified", b.Status.Phase
	})
}

// webhookSub creates an image Subscription on the moving tag main that polls
// every hour, with the webhook token in Secret hook, and waits for its
// baseline. Only a webhook makes it poll again within the test.
func webhookSub(t *testing.T, e *framework.Env, ns, name, ref, token, baseline string) {
	t.Helper()
	if _, err := e.Kube.CoreV1().Secrets(ns).Get(context.Background(), "hook-"+name, metav1.GetOptions{}); err != nil {
		createSecret(t, e, ns, "hook-"+name, corev1.SecretTypeOpaque, map[string]string{"token": token})
	}
	spec := imageSub(ref, "^main$", "1h")
	spec.Webhook = &v1alpha1.SubscriptionWebhook{SecretRef: v1alpha1.SubscriptionSecretRef{Name: "hook-" + name}}
	newSub(t, e, ns, name, spec)
	waitBaseline(t, e, ns, name, baseline)
}

// postSubWebhook posts body to the controller's receiver for ns/name.
func postSubWebhook(t *testing.T, ns, name, provider, pathToken string, headers map[string]string, body string) framework.HTTPResult {
	t.Helper()
	u := framework.ControllerURL(t) + "/webhook/subscriptions/" + ns + "/" + name + "/" + provider
	if pathToken != "" {
		u += "/" + pathToken
	}
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/json"
	res := framework.HTTP(t, http.MethodPost, u, headers, []byte(body))
	t.Logf("POST .../%s/%s/%s: HTTP %d %s", ns, name, provider, res.Status, strings.TrimSpace(res.Body))
	return res
}

func hmacHex(token, body string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestSub_WebhookRegistries pushes a new image and posts the push event of
// each registry the receiver understands, in its own format and with its
// own authentication: Docker Hub and Quay (token in the URL), Harbor (its
// Authorization header), Artifactory (X-JFrog-Event-Auth), GHCR (a GitHub
// package event signed with X-Hub-Signature-256) and generic
// (X-Kardinal-Signature-256). Each Subscription polls only every hour, so
// the Bundle for the new digest within a minute comes from the webhook: the
// receiver sets kardinal.io/refresh and the reconciler answers it in
// status.lastRefreshRequest.
//
// Covers SUB-WEBHOOK-01.
func TestSub_WebhookRegistries(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	token := randomHex(t, 16)
	ghcrBody := `{"action":"published","package":{"name":"podinfo","package_type":"container","package_version":{"container_metadata":{"tag":{"name":"main"}}}}}`
	generic := `{"image":"podinfo","tag":"main"}`
	providers := []struct {
		provider, pathToken string
		headers             map[string]string
		body                string
	}{
		{"dockerhub", token, nil, `{"push_data":{"tag":"main","pusher":"ci"},"repository":{"repo_name":"org/podinfo","name":"podinfo"}}`},
		{"quay", token, nil, `{"repository":"org/podinfo","updated_tags":["main"],"docker_url":"quay.io/org/podinfo"}`},
		{"harbor", "", map[string]string{"Authorization": token},
			`{"type":"PUSH_ARTIFACT","occur_at":1,"operator":"ci","event_data":{"resources":[{"tag":"main"}],"repository":{"name":"podinfo"}}}`},
		{"artifactory", "", map[string]string{"X-JFrog-Event-Auth": token},
			`{"domain":"docker","event_type":"pushed","data":{"repo_key":"docker-local","image_name":"podinfo","tag":"main"}}`},
		{"ghcr", "", map[string]string{"X-GitHub-Event": "package", "X-Hub-Signature-256": "sha256=" + hmacHex(token, ghcrBody)}, ghcrBody},
		{"generic", "", map[string]string{"X-Kardinal-Signature-256": "sha256=" + hmacHex(token, generic)}, generic},
	}
	for _, p := range providers {
		repo := a.ns + "/" + p.provider
		d13 := reg.Copy(t, "6.13.0", repo, "main")
		webhookSub(t, e, a.ns, p.provider, reg.Ref(repo), token, d13)
	}
	for _, p := range providers {
		repo := a.ns + "/" + p.provider
		d14 := reg.Copy(t, "6.14.0", repo, "main")
		res := postSubWebhook(t, a.ns, p.provider, p.provider, p.pathToken, p.headers, p.body)
		require.Equal(t, http.StatusAccepted, res.Status, "%s: %s", p.provider, res.Body)
		want := p.provider + "-main-" + digestHex(d14)[:8]
		s := waitSub(t, e, a.ns, p.provider, "a Bundle from the "+p.provider+" webhook", func(st v1alpha1.SubscriptionStatus) bool {
			return st.LastBundleCreated == want
		})
		refresh := getSub(t, e, a.ns, p.provider).Annotations[v1alpha1.RefreshAnnotation]
		require.NotEmpty(t, refresh, "%s: the receiver set %s", p.provider, v1alpha1.RefreshAnnotation)
		assert.Equal(t, refresh, s.Status.LastRefreshRequest, "%s: the reconciler answered the refresh", p.provider)
		assertImageBundle(t, e, a.ns, p.provider, want, reg.Repository(repo), "main", d14)
	}
}

// TestSub_WebhookAuth checks the receiver's refusals and idempotency: a
// wrong URL token, an unsigned or wrongly signed GitHub delivery, a
// Subscription without spec.webhook and one that does not exist all get the
// same 401 and change nothing; a GitHub ping and a Harbor delete are answered
// without a refresh; a flood of deliveries of one push is rate limited (429)
// past the per-Subscription burst and creates one Bundle.
//
// Covers SUB-WEBHOOK-02.
func TestSub_WebhookAuth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	token := randomHex(t, 16)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "main")
	webhookSub(t, e, a.ns, "hooked", reg.Ref(repo), token, d13)
	newSub(t, e, a.ns, "unhooked", imageSub(reg.Ref(repo), "^main$", "1h"))
	waitBaseline(t, e, a.ns, "unhooked", d13)

	push := `{"push_data":{"tag":"main"},"repository":{"repo_name":"org/podinfo"}}`
	ping := `{"zen":"Keep it logically awesome."}`
	for _, tc := range []struct {
		name, sub, provider, pathToken string
		headers                        map[string]string
		body                           string
	}{
		{"wrong token", "hooked", "dockerhub", randomHex(t, 16), nil, push},
		{"no token", "hooked", "dockerhub", "", nil, push},
		{"unsigned github", "hooked", "ghcr", token, map[string]string{"X-GitHub-Event": "package"}, push},
		{"bad signature", "hooked", "ghcr", "", map[string]string{"X-GitHub-Event": "package",
			"X-Hub-Signature-256": "sha256=" + hmacHex(token, push+" ")}, push},
		{"no spec.webhook", "unhooked", "dockerhub", token, nil, push},
		{"no such Subscription", "missing", "dockerhub", token, nil, push},
	} {
		res := postSubWebhook(t, a.ns, tc.sub, tc.provider, tc.pathToken, tc.headers, tc.body)
		assert.Equal(t, http.StatusUnauthorized, res.Status, tc.name)
		assert.Equal(t, `{"status":"unauthorized"}`, strings.TrimSpace(res.Body), tc.name)
	}
	res := postSubWebhook(t, a.ns, "hooked", "github", "", map[string]string{"X-GitHub-Event": "ping",
		"X-Hub-Signature-256": "sha256=" + hmacHex(token, ping)}, ping)
	assert.Equal(t, http.StatusOK, res.Status)
	assert.Equal(t, `{"status":"ignored: ping"}`, strings.TrimSpace(res.Body))
	res = postSubWebhook(t, a.ns, "hooked", "harbor", token, nil, `{"type":"DELETE_ARTIFACT"}`)
	assert.Equal(t, http.StatusOK, res.Status)
	for _, name := range []string{"hooked", "unhooked"} {
		assert.Empty(t, getSub(t, e, a.ns, name).Annotations[v1alpha1.RefreshAnnotation], "%s: nothing was refreshed", name)
	}

	// A flood at one Subscription: the receiver answers 429 past its
	// per-Subscription burst (10), the deliveries it accepts coalesce into
	// one refresh.
	d14 := reg.Copy(t, "6.14.0", repo, "main")
	codes := map[int]int{}
	for range 25 {
		codes[postSubWebhook(t, a.ns, "hooked", "dockerhub", token, nil, push).Status]++
	}
	assert.Positive(t, codes[http.StatusAccepted], "%v", codes)
	assert.Positive(t, codes[http.StatusTooManyRequests], "a flood is rate limited: %v", codes)
	assert.Equal(t, 25, codes[http.StatusAccepted]+codes[http.StatusTooManyRequests], "%v", codes)
	want := "hooked-main-" + digestHex(d14)[:8]
	waitSub(t, e, a.ns, "hooked", "a Bundle from the webhook", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == want
	})
	framework.Eventually(t, 30*time.Second, "a redelivery after the poll is accepted", func(context.Context) (bool, string) {
		res := postSubWebhook(t, a.ns, "hooked", "dockerhub", token, nil, push)
		return res.Status == http.StatusAccepted, fmt.Sprint(res.Status)
	})
	framework.Consistently(t, 15*time.Second, "one Bundle for the push", func(ctx context.Context) (bool, string) {
		n := len(subBundles(t, e, a.ns, "hooked"))
		return n == 1, fmt.Sprintf("%d Bundles", n)
	})
	assert.Empty(t, subBundles(t, e, a.ns, "unhooked"))
}
