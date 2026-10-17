//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// ssoPage is the page whose Option 3 the sso suite follows.
const ssoPage = "guides/security.md"

// TestSSO_OIDCProxy follows docs/guides/security.md "Option 3: single
// sign-on through an OIDC proxy" on a cluster whose API server trusts Dex
// (hack/e2e/components/sso-pki.sh, sso.sh): it runs the page's helm upgrade
// and RoleBindings, deploys oauth2-proxy with the page's flags, and uses the
// UI as a browser does, through the proxy:
//   - with no session, the UI redirects to Dex's login and the UI API
//     answers 401;
//   - alice (release-managers) signs in at Dex with no kardinal dialog, the
//     UI's assets load, and she approves a Bundle waiting on a gate whose
//     allowedGroups is the page's; the Approval records oidc:alice, her
//     oidc: groups and kardinal.io/recorded-via: ui, the gate's reason says
//     approved by oidc:alice, and prod promotes;
//   - bob (developers, the viewer role) lists the Pipeline and is refused
//     the approval with 403 naming the verb, resource and namespace.
//
// Covers UI-SSO-01.
func TestSSO_OIDCProxy(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	host := framework.MustEnv(t, "KARDINAL_E2E_SSO_HOST")
	issuer := framework.MustEnv(t, "KARDINAL_E2E_SSO_ISSUER")
	clientID := framework.MustEnv(t, "KARDINAL_E2E_SSO_CLIENT_ID")
	ssoDir := framework.MustEnv(t, "KARDINAL_E2E_SSO_DIR")
	secrets := framework.MustEnv(t, "KARDINAL_E2E_SSO_SECRETS")

	// ── Step 3: kardinal accepts the ID token ───────────────────────────
	rel := e.ExistingRelease(t, framework.ControllerDeployment, framework.ControllerNamespace)
	up := e.Shell(t, framework.Fill(t, framework.PageBlock(t, ssoPage, "acceptAPIServerAudience"),
		"oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.1", `"$KARDINAL_E2E_CHART"`,
		"kardinal.example.com", host))
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)

	// ── Step 2: oauth2-proxy with the page's flags ──────────────────────
	proxyNS := e.Namespace(t)
	deployOAuth2Proxy(t, e, proxyNS, ssoDir, secrets, framework.Fill(t, framework.PageCode(t, ssoPage, "text", "--pass-authorization-header"),
		"<issuer>", issuer,
		"<client id>", clientID,
		"<client secret>", "$(CLIENT_SECRET)",
		"<32 random bytes, base64>", "$(COOKIE_SECRET)",
		"https://kardinal.example.com/", "http://"+host+"/"))

	// ── The app, the page's gate and the page's RoleBindings ────────────
	a := newArgoApp(t, e, "test", "prod")
	var gateSpec struct {
		Spec struct {
			Approval v1alpha1.GateApprovalPolicy `json:"approval"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(framework.PageCode(t, ssoPage, "yaml", "allowedGroups")), &gateSpec))
	groups := gateSpec.Spec.Approval.AllowedGroups
	require.Equal(t, []string{"oidc:release-managers"}, groups, "the page's gate")
	g := framework.Gate(a.ns, "release-managers", "prod", "true", recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: groups}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	bind := e.Shell(t, framework.Fill(t, framework.PageBlock(t, ssoPage, "release-managers-approve"), "-n my-app", "-n "+a.ns))
	require.Zero(t, bind.Code, bind.Output)
	framework.Eventually(t, time.Minute, "the API server to grant the page's bindings", func(ctx context.Context) (bool, string) {
		for _, c := range []struct {
			user, group, verb string
		}{{"oidc:alice", "oidc:release-managers", "create"}, {"oidc:bob", "oidc:developers", "list"}} {
			sar, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
				Spec: authzv1.SubjectAccessReviewSpec{User: c.user, Groups: []string{c.group, "system:authenticated"},
					ResourceAttributes: &authzv1.ResourceAttributes{Namespace: a.ns, Verb: c.verb, Group: "kardinal.io", Resource: "approvals"}}},
				metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}
			if !sar.Status.Allowed {
				return false, fmt.Sprintf("%s cannot %s approvals", c.user, c.verb)
			}
		}
		return true, ""
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	routes := map[string]string{
		host + ":80":            hostPort(t, e.PortForward(t, proxyNS, "oauth2-proxy", 80)),
		hostPort(t, issuer+"/"): hostPort(t, e.PortForward(t, "dex", "dex", 5556)),
	}
	caPEM, err := os.ReadFile(filepath.Join(ssoDir, "ca.crt"))
	require.NoError(t, err)
	base := "http://" + host
	pw, err := os.ReadFile(filepath.Join(secrets, "sso-password"))
	require.NoError(t, err)
	password := string(pw)

	// ── No session: a login redirect for the UI, 401 for the API ────────
	anon := newSSOBrowser(t, routes, caPEM)
	anon.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := browse(t, anon, http.MethodGet, base+"/ui/", nil)
	require.Contains(t, []int{http.StatusFound, http.StatusSeeOther}, r.status, "the UI with no session: %s", r)
	loc, err := url.Parse(r.header.Get("Location"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(loc.String(), issuer+"/auth") || strings.HasPrefix(loc.Path, "/ui/oauth2/"),
		"the UI with no session redirects to the login: %s", loc)
	r = browse(t, anon, http.MethodGet, base+uiAPI+"/pipelines", nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the UI API with no session (--api-route): %s", r)

	// ── alice signs in, loads the UI and approves ───────────────────────
	alice := newSSOBrowser(t, routes, caPEM)
	index := signIn(t, alice, base, issuer, "alice", password)
	assets := regexp.MustCompile(`(?:src|href)="(/ui/[^"]+\.(?:js|css))"`).FindAllStringSubmatch(index, -1)
	require.NotEmpty(t, assets, "the UI's index names its assets:\n%s", index)
	for _, m := range assets {
		r := browse(t, alice, http.MethodGet, base+m[1], nil)
		assert.Equal(t, http.StatusOK, r.status, "asset %s through the proxy", m[1])
	}
	aliceUI := framework.UIClient{BaseURL: base, HTTP: alice}.With("Origin", base)
	// The API server's JWT authenticator may still be fetching Dex's keys.
	framework.Eventually(t, 2*time.Minute, "alice's ID token to authenticate", func(context.Context) (bool, string) {
		r := aliceUI.Get(t, uiAPI+"/pipelines")
		return r.Status == http.StatusOK && strings.Contains(r.Body, a.ns), r.String()
	})
	approve := map[string]any{"bundle": bundle, "environment": "prod", "namespace": a.ns, "decision": "approve", "comment": "signed in through Dex"}

	bob := newSSOBrowser(t, routes, caPEM)
	signIn(t, bob, base, issuer, "bob", password)
	bobUI := framework.UIClient{BaseURL: base, HTTP: bob}.With("Origin", base)
	rb := bobUI.Get(t, uiAPI+"/pipelines")
	require.Equal(t, http.StatusOK, rb.Status, "bob, a viewer, lists Pipelines: %s", rb)
	assert.Contains(t, rb.Body, a.ns, "bob sees the namespace he is bound in")
	rb = bobUI.Post(t, uiAPI+"/approvals", approve)
	assert.Equal(t, http.StatusForbidden, rb.Status, "bob may not approve: %s", rb)
	for _, s := range []string{"create", "approvals", a.ns} {
		assert.Contains(t, rb.Body, s, "the 403 names the verb, resource and namespace")
	}

	ra := aliceUI.Post(t, uiAPI+"/approvals", approve)
	require.Equal(t, http.StatusOK, ra.Status, "alice approves: %s", ra)
	assert.Contains(t, ra.Body, "Recorded: oidc:alice approves "+bundle+" for prod")

	var list v1alpha1.ApprovalList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(a.ns)))
	require.Len(t, list.Items, 1, "only alice's Approval")
	ap := list.Items[0]
	assert.Equal(t, "oidc:alice", ap.Spec.User)
	assert.Contains(t, ap.Spec.Groups, "oidc:release-managers", "the OIDC groups, prefixed")
	assert.Equal(t, "ui", ap.Annotations[lifecycle.AnnotationRecordedVia])
	assert.Equal(t, "signed in through Dex", ap.Spec.Comment)

	rbVerified(t, a, bundle, "prod")
	assertEnvAt(t, a, "prod", fixtures.V2)
	framework.Eventually(t, time.Minute, "the gate's reason to name alice", func(ctx context.Context) (bool, string) {
		var gates v1alpha1.PolicyGateList
		if err := e.Client.List(ctx, &gates, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		var reasons []string
		for _, pg := range gates.Items {
			if strings.Contains(pg.Status.Reason, "approved by oidc:alice") {
				return true, ""
			}
			reasons = append(reasons, pg.Name+": "+pg.Status.Reason)
		}
		return false, strings.Join(reasons, "; ")
	})
}

// deployOAuth2Proxy runs oauth2-proxy in ns with flags (one per
// line, the page's block) and the settings this cluster needs on top:
// plain-HTTP cookies (the test's host is http), the suite's CA for Dex, and
// the groups scope the page names for Dex.
func deployOAuth2Proxy(t *testing.T, e *framework.Env, ns, ssoDir, secrets, flags string) {
	t.Helper()
	ctx := context.Background()
	var args []string
	for _, l := range strings.Split(flags, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			args = append(args, l)
		}
	}
	args = append(args, "--cookie-secure=false",
		"--provider-ca-file=/etc/sso/ca.crt", "--scope=openid email profile groups")
	read := func(dir, name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return b
	}
	_, err := e.Kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "oauth2-proxy"},
		Data: map[string][]byte{"client-secret": read(secrets, "sso-client-secret"),
			"cookie-secret": read(secrets, "sso-cookie-secret"), "ca.crt": read(ssoDir, "ca.crt")},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	labels := map[string]string{"app": "oauth2-proxy"}
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "oauth2-proxy"}, Key: key}}}
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "oauth2-proxy", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "oauth2-proxy",
						Image:           framework.MustEnv(t, "KARDINAL_E2E_OAUTH2_PROXY_IMAGE"),
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            args,
						Env:             []corev1.EnvVar{secretEnv("CLIENT_SECRET", "client-secret"), secretEnv("COOKIE_SECRET", "cookie-secret")},
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: 4180}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/ping", Port: intstr.FromString("http")}}},
						VolumeMounts: []corev1.VolumeMount{{Name: "ca", MountPath: "/etc/sso", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
						SecretName: "oauth2-proxy", Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}}},
				},
			},
		},
	}
	t.Logf("oauth2-proxy %s", strings.Join(args, " "))
	_, err = e.Kube.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.CoreV1().Services(ns).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "oauth2-proxy", Namespace: ns},
		Spec: corev1.ServiceSpec{Selector: labels,
			Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	framework.Eventually(t, 3*time.Minute, "oauth2-proxy ready", func(ctx context.Context) (bool, string) {
		d, err := e.Kube.AppsV1().Deployments(ns).Get(ctx, "oauth2-proxy", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return d.Status.ReadyReplicas == 1, fmt.Sprintf("%d ready", d.Status.ReadyReplicas)
	})
}

// hostPort is the host:port of rawURL (port 443 or 80 when it names none).
func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return u.Host + ":443"
	}
	return u.Host + ":80"
}

// newSSOBrowser is a client that behaves as a browser on this cluster's
// network: it keeps cookies, follows redirects, trusts the suite's CA, and
// dials each host:port of routes at its local port-forward instead (the
// UI's host name and Dex's ClusterIP), and nothing else.
func newSSOBrowser(t *testing.T, routes map[string]string, caPEM []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM), "the suite's CA")
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				to, ok := routes[addr]
				if !ok {
					return nil, fmt.Errorf("the browser has no route to %s", addr)
				}
				return d.DialContext(ctx, network, to)
			},
		},
	}
}

// page is one browser response.
type page struct {
	status int
	header http.Header
	url    *url.URL
	body   string
}

func (p page) String() string {
	return fmt.Sprintf("HTTP %d at %s: %.1500s", p.status, p.url, strings.TrimSpace(p.body))
}

// browse sends method rawURL with form (POST) as a browser does.
func browse(t *testing.T, c *http.Client, method, rawURL string, form url.Values) page {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, rawURL, body)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.Do(req)
	require.NoError(t, err, "%s %s", method, rawURL)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return page{status: resp.StatusCode, header: resp.Header, url: resp.Request.URL, body: string(b)}
}

var (
	dexForm = regexp.MustCompile(`<form[^>]*action="([^"]*)"`)
	dexReq  = regexp.MustCompile(`name="req" value="([^"]+)"`)
)

// signIn opens the UI in c with no session, which ends on Dex's login form,
// signs in as user (Dex's password user <user>@sso.test, with the suite's
// generated password), grants the
// proxy access, and returns the UI's index page that the redirects end on:
// the proxy shows no page of its own and kardinal no sign-in dialog.
func signIn(t *testing.T, c *http.Client, base, issuer, user, password string) string {
	t.Helper()
	login := browse(t, c, http.MethodGet, base+"/ui/", nil)
	require.Equal(t, http.StatusOK, login.status, "the login page: %s", login)
	require.True(t, strings.HasPrefix(login.url.String(), issuer+"/"), "the UI with no session ends at Dex: %s", login)
	require.Contains(t, login.body, `name="password"`, "Dex's login form: %s", login)
	m := dexForm.FindStringSubmatch(login.body)
	require.NotNil(t, m, "Dex's login form has an action: %s", login)
	action, err := login.url.Parse(html.UnescapeString(m[1]))
	require.NoError(t, err)
	ui := browse(t, c, http.MethodPost, action.String(), url.Values{"login": {user + "@sso.test"}, "password": {password}})
	// oauth2-proxy asks for consent (prompt=consent, its --approval-prompt
	// default), so Dex shows its Grant Access page even with
	// skipApprovalScreen: the user grants it, as in a browser.
	if strings.HasSuffix(ui.url.Path, "/approval") {
		req := dexReq.FindStringSubmatch(ui.body)
		require.NotNil(t, req, "Dex's Grant Access form: %s", ui)
		ui = browse(t, c, http.MethodPost, ui.url.String(), url.Values{"req": {req[1]}, "approval": {"approve"}})
	}
	require.Equal(t, http.StatusOK, ui.status, "%s signs in: %s", user, ui)
	require.Equal(t, base+"/ui/", ui.url.String(), "%s lands on the UI: %s", user, ui)
	require.Contains(t, ui.body, `<div id="root">`, "the UI's index page")
	return ui.body
}
