// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// fakeSTS answers AssumeRole (recording each request's form) and
// GetCallerIdentity (the presigned request EKS sends, which must carry the
// assumed credentials and the cluster header), never real AWS.
type fakeSTS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	assumes []url.Values
	ids     atomic.Int32
	// assume, when set, answers AssumeRole instead (a denial, a redirect).
	assume http.HandlerFunc
}

const (
	baseKey    = "AKIDBASEFAKE"
	assumedKey = "ASIAASSUMEDFAKE"
	testRole   = "arn:aws:iam::111122223333:role/kardinal-health-team-a"
)

func newFakeSTS(t *testing.T) *fakeSTS {
	t.Helper()
	f := &fakeSTS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("Action") {
		case "AssumeRole":
			if f.assume != nil {
				f.assume(w, r)
				return
			}
			assert.Contains(t, r.Header.Get("Authorization"), "Credential="+baseKey, "assumed with the controller's own identity")
			f.mu.Lock()
			f.assumes = append(f.assumes, r.Form)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>`+
				`<Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>session-token</SessionToken>`+
				`<Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::111122223333:assumed-role/kardinal-health-team-a/%s</Arn>`+
				`<AssumedRoleId>AROAFAKE:%s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`,
				assumedKey, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), r.Form.Get("RoleSessionName"), r.Form.Get("RoleSessionName"))
		case "GetCallerIdentity":
			if !strings.HasPrefix(r.Form.Get("X-Amz-Credential"), assumedKey) || r.Form.Get("X-Amz-Security-Token") != "session-token" ||
				r.Header.Get("x-k8s-aws-id") == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.ids.Add(1)
			_, _ = fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>`+
				`<Arn>arn:aws:sts::111122223333:assumed-role/kardinal-health-team-a/kardinal-team-a-web</Arn><UserId>AROAFAKE:x</UserId>`+
				`<Account>111122223333</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>2</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeEKS is an EKS API server: it accepts a request whose bearer token is
// a presigned sts:GetCallerIdentity for cluster (the signed
// x-k8s-aws-id, X-Amz-Expires 60) that STS answers, as EKS does, and
// serves the Deployment prod/web.
func fakeEKS(t *testing.T, cluster string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var authed atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer k8s-aws-v1.")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		require.NoError(t, err)
		u, err := url.Parse(string(raw))
		require.NoError(t, err)
		q := u.Query()
		if q.Get("Action") != "GetCallerIdentity" || q.Get("X-Amz-Expires") != "60" ||
			!strings.Contains(q.Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
		req.Header.Set("x-k8s-aws-id", cluster)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authed.Add(1)
		if r.URL.Path != "/apis/apps/v1/namespaces/prod/deployments/web" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "generation": 1},
			"spec":     map[string]interface{}{"replicas": 1},
			"status": map[string]interface{}{"observedGeneration": 1, "replicas": 1, "updatedReplicas": 1, "readyReplicas": 1,
				"availableReplicas": 1, "conditions": []interface{}{map[string]interface{}{"type": "Available", "status": "True"}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &authed
}

// fakeEKSAPI is the EKS API's DescribeCluster for cluster: called as the
// assumed role (its SigV4 credential and session token), it answers the
// endpoint and CA of server, or with answer when set (a denial, a
// redirect). calls counts the calls.
type fakeEKSAPI struct {
	srv    *httptest.Server
	calls  atomic.Int32
	answer http.HandlerFunc
}

func newFakeEKSAPI(t *testing.T, cluster string, server *httptest.Server) *fakeEKSAPI {
	t.Helper()
	f := &fakeEKSAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.answer != nil {
			f.answer(w, r)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/clusters/"+cluster ||
			!strings.Contains(r.Header.Get("Authorization"), "Credential="+assumedKey+"/") ||
			r.Header.Get("X-Amz-Security-Token") != "session-token" {
			w.Header().Set("X-Amzn-ErrorType", "AccessDeniedException")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"not the assumed role"}`)
			return
		}
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"cluster": map[string]interface{}{
			"name": cluster, "status": "ACTIVE", "endpoint": server.URL,
			"certificateAuthority": map[string]interface{}{"data": base64.StdEncoding.EncodeToString(ca)}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// tenantServer is an https server a tenant runs: it records every request
// and the Authorization header of each.
func tenantServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" Authorization="+r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// eksEnv is a fake STS, a fake EKS cluster prod-eu and its fake EKS API,
// and the RemoteClusters of a controller allowing testRole, all on fakes.
type eksEnv struct {
	sts     *fakeSTS
	cluster *httptest.Server
	authed  *atomic.Int32
	api     *fakeEKSAPI
	now     time.Time
	r       *health.RemoteClusters
}

func newEKSEnv(t *testing.T) *eksEnv {
	t.Helper()
	awsTestEnv(t)
	e := &eksEnv{sts: newFakeSTS(t), now: time.Now()}
	e.cluster, e.authed = fakeEKS(t, "prod-eu")
	e.api = newFakeEKSAPI(t, "prod-eu", e.cluster)
	e.r = &health.RemoteClusters{Dial: loopback, NowFn: func() time.Time { return e.now }, EKS: &health.EKSAuth{
		AllowedRoleARNs: []string{"arn:aws:iam::111122223333:role/kardinal-health-*"},
		Transport:       http.DefaultTransport, STSEndpoint: e.sts.srv.URL, EKSEndpoint: e.api.srv.URL,
		NowFn: func() time.Time { return e.now }}}
	return e
}

var prodEU = &v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}

// checkWeb runs a resource health check of prod/web with d.
func checkWeb(t *testing.T, d *health.AutoDetector) error {
	t.Helper()
	a, err := d.Select(context.Background(), "resource")
	require.NoError(t, err)
	_, err = a.Check(context.Background(), health.CheckOptions{Type: "resource",
		Resource: health.ResourceConfig{Name: "web", Namespace: "prod"}})
	return err
}

// awsTestEnv gives the AWS SDK static base credentials and no other source
// (no profile, metadata or web identity), so nothing reaches real AWS.
func awsTestEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(v, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", baseKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "base-secret")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
}

// eksKubeconfig is a kubeconfig of server and its CA, with user.
func eksKubeconfig(srv *httptest.Server, user string) []byte {
	return eksKubeconfigFor(srv.URL, srv, user)
}

// eksKubeconfigFor is a kubeconfig naming server, with srv's CA and user.
func eksKubeconfigFor(server string, srv *httptest.Server, user string) []byte {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: %q, certificate-authority-data: %s}
users:
- name: u
  user: {%s}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
`, server, ca, user))
}

func eksSecret(data []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "prod-eu", Namespace: "team-a", UID: "u1", ResourceVersion: "1"},
		Data: map[string][]byte{"kubeconfig": data}}
}

// TestEKS_HealthCheckAuthenticates (#2008): with kubeconfigSecretRef.eks the
// health check reaches an EKS cluster with a token the controller signs: it
// assumes the role with its own identity and session tags naming the
// Pipeline, reads the cluster's endpoint and CA with eks:DescribeCluster as
// that role (the Secret holds no kubeconfig at all), presigns
// sts:GetCallerIdentity for the cluster, and EKS (here a fake, with a fake
// STS and EKS API) accepts it. The token is reused for 10 minutes, then
// signed again; the role is not assumed again while its credentials last.
//
// Covers HEALTH-EKS-01.
func TestEKS_HealthCheckAuthenticates(t *testing.T) {
	e := newEKSEnv(t)
	empty := eksSecret(nil)
	d, err := e.r.DetectorWith(context.Background(), empty, "", prodEU, "web")
	require.NoError(t, err)
	require.NoError(t, checkWeb(t, d))
	perCheck := e.authed.Load()
	assert.Positive(t, perCheck, "EKS accepted the token")
	assert.Equal(t, int32(1), e.api.calls.Load(), "the endpoint and CA come from eks:DescribeCluster, as the role")
	e.sts.mu.Lock()
	require.Len(t, e.sts.assumes, 1)
	f := e.sts.assumes[0]
	e.sts.mu.Unlock()
	assert.Equal(t, testRole, f.Get("RoleArn"))
	assert.Regexp(t, `^kardinal-team-a-web-[0-9a-f]{8}$`, f.Get("RoleSessionName"))
	tags := map[string]string{}
	for i := 1; f.Get(fmt.Sprintf("Tags.member.%d.Key", i)) != ""; i++ {
		tags[f.Get(fmt.Sprintf("Tags.member.%d.Key", i))] = f.Get(fmt.Sprintf("Tags.member.%d.Value", i))
	}
	assert.Equal(t, map[string]string{health.EKSTagNamespace: "team-a", health.EKSTagPipeline: "web"}, tags,
		"the session tags name the Pipeline, for the role's trust policy")

	d2, err := e.r.DetectorWith(context.Background(), empty, "", prodEU, "web")
	require.NoError(t, err)
	assert.Same(t, d, d2, "cached")
	require.NoError(t, checkWeb(t, d))
	ids := e.sts.ids.Load()
	e.now = e.now.Add(11 * time.Minute)
	require.NoError(t, checkWeb(t, d))
	assert.Equal(t, 3*perCheck, e.authed.Load(), "every request accepted, before and after the token was signed again")
	assert.GreaterOrEqual(t, e.sts.ids.Load(), ids)
	e.sts.mu.Lock()
	assert.Len(t, e.sts.assumes, 1, "the role's credentials are reused while they last")
	e.sts.mu.Unlock()
	assert.Equal(t, int32(1), e.api.calls.Load(), "the endpoint is read once while it is fresh")
}

// TestEKS_TenantServerGetsNoToken (#2193, QA-3 finding 1): the token goes
// only to the endpoint eks:DescribeCluster returns. A kubeconfig Secret
// naming another server (one the tenant runs) is refused before any
// request, with the reason; one naming the endpoint itself is used; and a
// redirect from the cluster's endpoint to the tenant's server is refused,
// so that server never sees the token.
//
// Covers HEALTH-EKS-02.
func TestEKS_TenantServerGetsNoToken(t *testing.T) {
	t.Run("the Secret names another server", func(t *testing.T) {
		e := newEKSEnv(t)
		tenant, seen := tenantServer(t)
		d, err := e.r.DetectorWith(context.Background(), eksSecret(eksKubeconfig(tenant, "")), "", prodEU, "web")
		if err == nil {
			_ = checkWeb(t, d) // what a health check would send
		}
		assert.Empty(t, seen(), "the tenant's server got no request")
		require.ErrorIs(t, err, health.ErrKubeconfigNotAllowed)
		assert.Contains(t, err.Error(), "the kubeconfig's server "+tenant.URL+" is not the endpoint of EKS cluster prod-eu, "+e.cluster.URL)
		assert.Zero(t, e.authed.Load())
	})
	t.Run("the Secret names the endpoint", func(t *testing.T) {
		e := newEKSEnv(t)
		d, err := e.r.DetectorWith(context.Background(), eksSecret(eksKubeconfigFor(e.cluster.URL+"/", e.cluster, "")), "", prodEU, "web")
		require.NoError(t, err)
		require.NoError(t, checkWeb(t, d))
		assert.Positive(t, e.authed.Load())
	})
	t.Run("the endpoint redirects to the tenant", func(t *testing.T) {
		e := newEKSEnv(t)
		tenant, seen := tenantServer(t)
		redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, tenant.URL+r.URL.Path, http.StatusTemporaryRedirect)
		}))
		t.Cleanup(redirecting.Close)
		e.api = newFakeEKSAPI(t, "prod-eu", redirecting)
		e.r.EKS.EKSEndpoint = e.api.srv.URL
		d, err := e.r.DetectorWith(context.Background(), eksSecret(nil), "", prodEU, "web")
		require.NoError(t, err)
		require.Error(t, checkWeb(t, d))
		for _, req := range seen() {
			assert.NotContains(t, req, "k8s-aws-v1.", "the tenant's server got the token")
		}
	})
}

// TestEKS_DescribeClusterFails: when eks:DescribeCluster fails (the role may
// not call it, its trust policy refuses the controller, the cluster has no
// endpoint yet) the cluster cannot be checked yet: the error is not
// ErrKubeconfigNotAllowed (the step waits until health.timeout) and says,
// briefly, which call failed and why. No token is signed.
//
// Covers HEALTH-EKS-02.
func TestEKS_DescribeClusterFails(t *testing.T) {
	for name, tc := range map[string]struct {
		api, assume http.HandlerFunc
		want        string
	}{
		"no eks:DescribeCluster permission": {api: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Amzn-ErrorType", "AccessDeniedException")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"not authorized to perform: eks:DescribeCluster"}`)
		}, want: "EKS cluster prod-eu: eks:DescribeCluster as " + testRole + ": EKS DescribeCluster: AccessDeniedException"},
		"the trust policy refuses": {assume: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>no</Message></Error></ErrorResponse>`)
		}, want: "STS AssumeRole: AccessDenied"},
		"no endpoint yet": {api: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"cluster":{"name":"prod-eu","status":"CREATING"}}`)
		}, want: "the cluster has no endpoint yet"},
		"an http endpoint": {api: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"cluster":{"name":"prod-eu","endpoint":"http://example.com"}}`)
		}, want: "the cluster's endpoint is not an https URL"},
		"no CA": {api: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"cluster":{"name":"prod-eu","endpoint":"https://example.com"}}`)
		}, want: "the cluster has no certificate authority"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEKSEnv(t)
			e.api.answer, e.sts.assume = tc.api, tc.assume
			_, err := e.r.DetectorWith(context.Background(), eksSecret(nil), "", prodEU, "web")
			require.Error(t, err)
			assert.NotErrorIs(t, err, health.ErrKubeconfigNotAllowed, "the step waits")
			assert.Contains(t, err.Error(), tc.want)
			assert.Zero(t, e.sts.ids.Load(), "no token signed")
		})
	}
}

// TestEKS_ClusterReadAgain: a cluster's endpoint and CA are read again an
// hour after they were, while the Pipeline keeps checking, so a cluster
// created again under its name is followed.
//
// Covers HEALTH-EKS-02.
func TestEKS_ClusterReadAgain(t *testing.T) {
	e := newEKSEnv(t)
	for i := 0; i < 4; i++ {
		d, err := e.r.DetectorWith(context.Background(), eksSecret(nil), "", prodEU, "web")
		require.NoError(t, err)
		require.NoError(t, checkWeb(t, d))
		e.now = e.now.Add(25 * time.Minute)
	}
	assert.Equal(t, int32(2), e.api.calls.Load(), "read at 0 and again at 75 minutes")
}

// TestEKS_RedirectRefused (#2193, QA-3 finding 5): an AWS API answer that
// redirects is not followed, so STS or the EKS API cannot send the
// controller's signed requests elsewhere (a credential endpoint, a tenant's
// server).
//
// Covers HEALTH-EKS-02.
func TestEKS_RedirectRefused(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		for _, which := range []string{"STS AssumeRole", "EKS DescribeCluster"} {
			t.Run(fmt.Sprintf("%s %d", which, code), func(t *testing.T) {
				e := newEKSEnv(t)
				var elsewhere atomic.Int32
				other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					elsewhere.Add(1)
					w.WriteHeader(http.StatusBadRequest)
				}))
				t.Cleanup(other.Close)
				redirect := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL+"/steal", code) }
				if which == "STS AssumeRole" {
					e.sts.assume = redirect
				} else {
					e.api.answer = redirect
				}
				_, err := e.r.DetectorWith(context.Background(), eksSecret(nil), "", prodEU, "web")
				require.Error(t, err)
				assert.Zero(t, elsewhere.Load(), "the redirect was not followed")
			})
		}
	}
}

// TestEKS_TokenRefreshed: a token is reused for 10 minutes and signed again
// after, with a new signing time.
func TestEKS_TokenRefreshed(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	now := time.Now()
	a := &health.EKSAuth{AllowedRoleARNs: []string{testRole}, Transport: http.DefaultTransport, STSEndpoint: sts.srv.URL,
		NowFn: func() time.Time { return now }}
	src, err := a.TokenSource(&v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}, "team-a", "web")
	require.NoError(t, err)
	first, err := src.Token(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first, "k8s-aws-v1."))
	now = now.Add(9 * time.Minute)
	again, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first, again, "reused under 10 minutes")
	now = now.Add(2 * time.Minute)
	time.Sleep(1100 * time.Millisecond) // the signature's X-Amz-Date has a one-second resolution
	later, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, first, later, "signed again after 10 minutes")
}

// TestEKS_Refused: the controller signs nothing unless the operator allows
// the role (an empty list, the default, allows none, so a Pipeline cannot
// have the controller assume another team's role), and a kubeconfig with
// credentials of its own is refused with eks. Both fail the step
// (ErrKubeconfigNotAllowed) before any AWS call.
func TestEKS_Refused(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	eks, _ := fakeEKS(t, "prod-eu")
	api := newFakeEKSAPI(t, "prod-eu", eks)
	creds := &v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}
	for name, tc := range map[string]struct {
		r    *health.RemoteClusters
		kc   []byte
		want string
	}{
		"feature off": {&health.RemoteClusters{Dial: loopback}, eksKubeconfig(eks, ""), "signs no EKS tokens"},
		"empty allowlist": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL, EKSEndpoint: api.srv.URL,
			Transport: http.DefaultTransport}},
			eksKubeconfig(eks, ""), "is not one of the roles the controller allows for namespace team-a"},
		"another team's role": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL, EKSEndpoint: api.srv.URL,
			Transport:       http.DefaultTransport,
			AllowedRoleARNs: []string{"arn:aws:iam::111122223333:role/kardinal-health-team-b"}}},
			eksKubeconfig(eks, ""), "is not one of"},
		"a token in the kubeconfig": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL, EKSEndpoint: api.srv.URL,
			Transport:       http.DefaultTransport,
			AllowedRoleARNs: []string{testRole}}}, eksKubeconfig(eks, "token: long-lived"), "remove users[].user's credentials"},
		"an http server": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL, EKSEndpoint: api.srv.URL,
			Transport:       http.DefaultTransport,
			AllowedRoleARNs: []string{testRole}}}, eksKubeconfigFor("http://example.com", eks, ""), "server must be an https URL"},
		"another namespace's role": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL, EKSEndpoint: api.srv.URL,
			Transport:                  http.DefaultTransport,
			AllowedRoleARNsByNamespace: map[string][]string{"team-b": {testRole}}}},
			eksKubeconfig(eks, ""), "is not one of the roles the controller allows for namespace team-a"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.r.DetectorWith(context.Background(), eksSecret(tc.kc), "", creds, "web")
			require.ErrorIs(t, err, health.ErrKubeconfigNotAllowed)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	sts.mu.Lock()
	assert.Empty(t, sts.assumes, "no AWS call for a refused configuration")
	sts.mu.Unlock()
	assert.Zero(t, api.calls.Load(), "no eks:DescribeCluster for a refused configuration")
}

// loopback lets the tests reach httptest servers, which the egress guard
// refuses.
var loopback = (&net.Dialer{}).DialContext

// TestEKS_AllowedByNamespace (#2193, QA-3 finding 2): a role in
// AllowedRoleARNsByNamespace may be named by that namespace's Pipelines
// only; AllowedRoleARNs apply to every namespace.
//
// Covers HEALTH-EKS-03.
func TestEKS_AllowedByNamespace(t *testing.T) {
	a := &health.EKSAuth{AllowedRoleARNs: []string{"arn:aws:iam::111122223333:role/kardinal-health-shared"},
		AllowedRoleARNsByNamespace: map[string][]string{
			"team-a": {"arn:aws:iam::111122223333:role/kardinal-health-team-a"},
			"team-b": {"arn:aws:iam::111122223333:role/kardinal-health-team-b-*"},
		}}
	for _, tc := range []struct {
		namespace, role string
		want            bool
	}{
		{"team-a", "arn:aws:iam::111122223333:role/kardinal-health-team-a", true},
		{"team-b", "arn:aws:iam::111122223333:role/kardinal-health-team-a", false},
		{"team-b", "arn:aws:iam::111122223333:role/kardinal-health-team-b-eu", true},
		{"team-a", "arn:aws:iam::111122223333:role/kardinal-health-team-b-eu", false},
		{"team-c", "arn:aws:iam::111122223333:role/kardinal-health-team-a", false},
		{"team-c", "arn:aws:iam::111122223333:role/kardinal-health-shared", true},
		{"team-a", "arn:aws:iam::111122223333:role/kardinal-health-shared", true},
	} {
		assert.Equal(t, tc.want, a.Allowed(tc.namespace, tc.role), "%s naming %s", tc.namespace, tc.role)
	}
	_, err := a.TokenSource(&v1alpha1.EKSCredentials{ClusterName: "c", Region: "eu-west-1",
		RoleARN: "arn:aws:iam::111122223333:role/kardinal-health-team-a"}, "team-b", "web")
	require.ErrorIs(t, err, health.ErrKubeconfigNotAllowed)
	assert.Contains(t, err.Error(), "allows for namespace team-b")
}

// TestParseEKSRoleARNsByNamespace: --health-eks-role-arns-by-namespace is
// namespace=pattern entries, several per namespace; anything else is
// refused at start-up.
func TestParseEKSRoleARNsByNamespace(t *testing.T) {
	got, err := health.ParseEKSRoleARNsByNamespace([]string{"team-a=arn:aws:iam::111122223333:role/a",
		" team-a = arn:aws:iam::111122223333:role/a-* ", "team-b=arn:aws:iam::*:role/b"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"team-a": {"arn:aws:iam::111122223333:role/a", "arn:aws:iam::111122223333:role/a-*"},
		"team-b": {"arn:aws:iam::*:role/b"}}, got)
	none, err := health.ParseEKSRoleARNsByNamespace(nil)
	require.NoError(t, err)
	assert.Nil(t, none)
	for _, bad := range []string{"arn:aws:iam::111122223333:role/a", "=arn:aws:iam::111122223333:role/a", "team-a=",
		"Team-A=arn:aws:iam::111122223333:role/a", "team-a=role/a", "team-a=arn:aws:iam::111122223333:role/[a",
		"*=arn:aws:iam::111122223333:role/a"} {
		_, err := health.ParseEKSRoleARNsByNamespace([]string{bad})
		assert.Error(t, err, bad)
	}
}

// TestEKS_SessionNames (#2193, QA-3 finding 4): every Pipeline has its own
// role session name, in CloudTrail: kardinal-a-b/c and kardinal-a/b-c
// differ, and a long namespace and name still fit STS's 64 characters.
//
// Covers HEALTH-EKS-03.
func TestEKS_SessionNames(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	a := &health.EKSAuth{AllowedRoleARNs: []string{testRole}, Transport: http.DefaultTransport, STSEndpoint: sts.srv.URL}
	long := strings.Repeat("n", 63)
	pairs := [][2]string{{"a-b", "c"}, {"a", "b-c"}, {long, strings.Repeat("p", 253)}, {long, strings.Repeat("p", 252) + "q"}}
	for _, p := range pairs {
		src, err := a.TokenSource(&v1alpha1.EKSCredentials{ClusterName: "c", Region: "eu-west-1", RoleARN: testRole}, p[0], p[1])
		require.NoError(t, err)
		_, err = src.Token(context.Background())
		require.NoError(t, err)
	}
	sts.mu.Lock()
	defer sts.mu.Unlock()
	require.Len(t, sts.assumes, len(pairs))
	names := map[string]bool{}
	for _, f := range sts.assumes {
		n := f.Get("RoleSessionName")
		assert.LessOrEqual(t, len(n), 64, n)
		assert.Regexp(t, `^kardinal-[\w+=,.@-]*-[0-9a-f]{8}$`, n)
		names[n] = true
	}
	assert.Len(t, names, len(pairs), "one session name per Pipeline: %v", names)
}

// TestEKS_TokenSourcesEvicted (#2193, QA-3 finding 3): a token source (its
// credentials and clients) unused for an hour is dropped, one in use is
// kept, and at most 128 are kept.
//
// Covers HEALTH-EKS-03.
func TestEKS_TokenSourcesEvicted(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	now := time.Now()
	a := &health.EKSAuth{AllowedRoleARNs: []string{testRole}, Transport: http.DefaultTransport, STSEndpoint: sts.srv.URL,
		NowFn: func() time.Time { return now }}
	creds := &v1alpha1.EKSCredentials{ClusterName: "c", Region: "eu-west-1", RoleARN: testRole}
	inUse, err := a.TokenSource(creds, "team-a", "in-use")
	require.NoError(t, err)
	_, err = a.TokenSource(creds, "team-a", "idle")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		now = now.Add(25 * time.Minute)
		_, err = inUse.Token(context.Background()) // the cluster's client asks for a token
		require.NoError(t, err)
	}
	_, err = a.TokenSource(creds, "team-a", "new")
	require.NoError(t, err)
	assert.Equal(t, 2, a.Len(), "the idle source is dropped, the one in use and the new one kept")
	again, err := a.TokenSource(creds, "team-a", "in-use")
	require.NoError(t, err)
	assert.Same(t, inUse, again)

	for i := 0; i < 200; i++ {
		now = now.Add(time.Second)
		_, err = a.TokenSource(creds, "team-a", fmt.Sprintf("p%d", i))
		require.NoError(t, err)
	}
	assert.Equal(t, 128, a.Len(), "at most 128 kept")
}
