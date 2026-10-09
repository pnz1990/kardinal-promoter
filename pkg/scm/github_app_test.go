// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	testAppID          = 4242
	testInstallationID = 777
)

// fakeGitHubApp is a GitHub API that verifies the App JWT the way GitHub
// does (RS256 with the App's public key, iss the App ID, iat in the past,
// exp at most 10 minutes after iat) before it issues an installation token,
// and accepts only its installation tokens on the REST endpoints.
type fakeGitHubApp struct {
	t      *testing.T
	pub    *rsa.PublicKey
	prefix string
	// now is the server's clock, the same one the token source uses.
	now func() time.Time

	mints   atomic.Int32
	mu      sync.Mutex
	issued  map[string]time.Time
	mintErr int // when non-zero, the mint answers this status
	authOf  []string
}

func newFakeGitHubApp(t *testing.T, pub *rsa.PublicKey, prefix string, now func() time.Time) (*fakeGitHubApp, *httptest.Server) {
	f := &fakeGitHubApp{t: t, pub: pub, prefix: prefix, now: now, issued: map[string]time.Time{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHubApp) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, f.prefix)
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	w.Header().Set("Content-Type", "application/json")
	if path == fmt.Sprintf("/app/installations/%d/access_tokens", testInstallationID) && r.Method == http.MethodPost {
		if err := f.verifyJWT(auth); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"message":%q}`, err.Error())
			return
		}
		if f.mintErr != 0 {
			w.WriteHeader(f.mintErr)
			_, _ = w.Write([]byte(`{"message":"Integration not found"}`))
			return
		}
		n := f.mints.Add(1)
		tok := fmt.Sprintf("ghs_installation_%d", n)
		exp := f.now().Add(time.Hour).UTC().Truncate(time.Second)
		f.mu.Lock()
		f.issued[tok] = exp
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, tok, exp.Format(time.RFC3339))
		return
	}
	f.mu.Lock()
	exp, ok := f.issued[auth]
	f.authOf = append(f.authOf, auth)
	f.mu.Unlock()
	if !ok || !f.now().Before(exp) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		return
	}
	switch {
	case r.Method == http.MethodPost && path == "/repos/acme/web/pulls":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.example/acme/web/pull/9"}`))
	case r.Method == http.MethodGet && path == "/repos/acme/web/pulls/9":
		_, _ = w.Write([]byte(`{"state":"open","merged":false}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}
}

func (f *fakeGitHubApp) verifyJWT(jwt string) error {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT")
	}
	dec := base64.RawURLEncoding
	var header struct{ Alg, Typ string }
	raw, err := dec.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "RS256" {
		return fmt.Errorf("bad header %s", raw)
	}
	sig, err := dec.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("bad signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("signature does not verify: %w", err)
	}
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	raw, err = dec.DecodeString(parts[1])
	if err != nil || json.Unmarshal(raw, &claims) != nil {
		return fmt.Errorf("bad claims")
	}
	now := f.now().Unix()
	switch {
	case claims.Iss != fmt.Sprint(testAppID):
		return fmt.Errorf("iss %q", claims.Iss)
	case claims.Iat > now:
		return fmt.Errorf("iat in the future")
	case claims.Exp <= now || claims.Exp-claims.Iat > 600:
		return fmt.Errorf("exp out of range")
	}
	return nil
}

func appKey(t *testing.T, pkcs8 bool) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		require.NoError(t, err)
		return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// clock is a settable clock shared by the fake server and the token source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func appSource(t *testing.T, key []byte, apiURL string, c *clock) *scm.GitHubAppTokenSource {
	t.Helper()
	src, err := scm.NewGitHubAppTokenSource(scm.GitHubAppCredentials{AppID: testAppID, InstallationID: testInstallationID, PrivateKey: key}, apiURL)
	require.NoError(t, err)
	scm.SetAppTokenClockForTest(src, c.now)
	return src
}

// TestGitHubApp_TokenFlow is the contract of GitHub App authentication
// against a GitHub API that verifies the JWT: the App JWT is RS256-signed
// with the private key (PKCS#1 or PKCS#8) with iss the App ID; the
// installation token is cached, replaced 10 minutes before it expires, and
// minted once for concurrent callers; GitHub Enterprise Server's /api/v3
// prefix is kept; PR requests carry the installation token as a Bearer.
// Covers SCM-GHAPP-01.
func TestGitHubApp_TokenFlow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pkcs8  bool
		prefix string
	}{
		{name: "github.com PKCS#1"},
		{name: "github.com PKCS#8", pkcs8: true},
		{name: "enterprise server", prefix: "/api/v3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
			k, pemKey := appKey(t, tc.pkcs8)
			f, srv := newFakeGitHubApp(t, &k.PublicKey, tc.prefix, c.now)
			src := appSource(t, pemKey, srv.URL+tc.prefix, c)
			p := scm.NewGitHubAppProvider(src, srv.URL+tc.prefix, "")

			url, num, err := p.OpenPR(context.Background(), "acme/web", "t", "b", "h", "main")
			require.NoError(t, err)
			assert.Equal(t, 9, num)
			assert.Equal(t, "https://github.example/acme/web/pull/9", url)
			_, _, err = p.GetPRStatus(context.Background(), "acme/web", 9)
			require.NoError(t, err)
			assert.EqualValues(t, 1, f.mints.Load(), "the token is cached")

			c.add(49 * time.Minute) // 11 minutes left: still used
			_, _, err = p.GetPRStatus(context.Background(), "acme/web", 9)
			require.NoError(t, err)
			assert.EqualValues(t, 1, f.mints.Load())

			c.add(2 * time.Minute) // 9 minutes left: replaced before it expires
			_, _, err = p.GetPRStatus(context.Background(), "acme/web", 9)
			require.NoError(t, err)
			assert.EqualValues(t, 2, f.mints.Load())
			f.mu.Lock()
			assert.Equal(t, []string{"ghs_installation_1", "ghs_installation_1", "ghs_installation_1", "ghs_installation_2"}, f.authOf)
			f.mu.Unlock()
		})
	}

	t.Run("concurrent callers share one mint", func(t *testing.T) {
		c := &clock{t: time.Now()}
		k, pemKey := appKey(t, false)
		f, srv := newFakeGitHubApp(t, &k.PublicKey, "", c.now)
		src := appSource(t, pemKey, srv.URL, c)
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tok, err := src.Token(context.Background())
				assert.NoError(t, err)
				assert.Equal(t, "ghs_installation_1", tok)
			}()
		}
		wg.Wait()
		assert.EqualValues(t, 1, f.mints.Load())
	})

	t.Run("a key GitHub does not know is refused", func(t *testing.T) {
		c := &clock{t: time.Now()}
		k, _ := appKey(t, false)
		_, otherKey := appKey(t, false)
		_, srv := newFakeGitHubApp(t, &k.PublicKey, "", c.now)
		p := scm.NewGitHubAppProvider(appSource(t, otherKey, srv.URL, c), srv.URL, "")
		_, _, err := p.OpenPR(context.Background(), "acme/web", "t", "b", "h", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint GitHub App installation token")
		assert.True(t, scm.IsPermanentError(err), "a 401 from the mint is permanent: %v", err)
	})

	t.Run("an installation that is gone", func(t *testing.T) {
		c := &clock{t: time.Now()}
		k, pemKey := appKey(t, false)
		f, srv := newFakeGitHubApp(t, &k.PublicKey, "", c.now)
		f.mintErr = http.StatusNotFound
		err := scm.CheckGitHubApp(context.Background(), scm.NewGitHubAppProvider(appSource(t, pemKey, srv.URL, c), srv.URL, ""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 404")
	})
}

// TestGitHubAppCredentialsFromData reads the three Secret keys, and names
// the key that is missing or wrong without printing a value.
func TestGitHubAppCredentialsFromData(t *testing.T) {
	_, key := appKey(t, false)
	tests := []struct {
		name   string
		data   map[string][]byte
		wantOK bool
		want   string
	}{
		{name: "a token Secret", data: map[string][]byte{"token": []byte("ghp_x")}},
		{name: "complete", wantOK: true, data: map[string][]byte{
			"githubAppID": []byte(" 4242\n"), "githubAppInstallationID": []byte("777"), "githubAppPrivateKey": key}},
		{name: "no installation", wantOK: true, want: "githubAppPrivateKey is set but githubAppInstallationID is missing",
			data: map[string][]byte{"githubAppID": []byte("4242"), "githubAppPrivateKey": key}},
		{name: "bad app id", wantOK: true, want: "githubAppID must be a positive number",
			data: map[string][]byte{"githubAppID": []byte("abc"), "githubAppInstallationID": []byte("777"), "githubAppPrivateKey": key}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok, err := scm.GitHubAppCredentialsFromData(tt.data)
			assert.Equal(t, tt.wantOK, ok)
			if tt.want != "" {
				require.Error(t, err)
				assert.Equal(t, tt.want, err.Error())
				return
			}
			require.NoError(t, err)
			if ok {
				assert.EqualValues(t, testAppID, c.AppID)
				assert.EqualValues(t, testInstallationID, c.InstallationID)
				assert.NotContains(t, c.Fingerprint(), "PRIVATE KEY")
			}
		})
	}
	_, err := scm.NewGitHubAppTokenSource(scm.GitHubAppCredentials{AppID: 1, InstallationID: 1, PrivateKey: []byte("nope")}, "")
	assert.EqualError(t, err, "GitHub App private key is not PEM")
	_, err = scm.NewProviderWithCredentials("gitlab", scm.Credentials{GitHubApp: &scm.GitHubAppCredentials{AppID: 1, InstallationID: 1, PrivateKey: key}}, "", "")
	assert.ErrorContains(t, err, "GitHub App credentials need SCM provider github")
}

// TestSecretWatcher_GitHubApp: a watched Secret with GitHub App keys loads
// the App (and mints a token to check it), a new key is a rotation the
// provider reloads without a restart, a Secret whose App keys are
// incomplete leaves the provider as it was, and back to a token works.
// Covers SCM-GHAPP-02.
func TestSecretWatcher_GitHubApp(t *testing.T) {
	c := &clock{t: time.Now()}
	k1, key1 := appKey(t, false)
	f, srv := newFakeGitHubApp(t, &k1.PublicKey, "", c.now)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app", Namespace: "kardinal-system"},
		Data: map[string][]byte{"githubAppID": []byte("4242"), "githubAppInstallationID": []byte("777"), "githubAppPrivateKey": key1}}
	kc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	dyn, err := scm.NewDynamicProvider("github", "", srv.URL, "")
	require.NoError(t, err)
	var logs strings.Builder
	w := scm.NewSecretWatcher(kc, dyn, "github-app", "kardinal-system", "token", zerolog.New(&logs))

	w.CheckAndReloadForTest(context.Background())
	assert.Contains(t, logs.String(), "SCM credentials loaded — provider uses GitHub App 4242 installation 777")
	assert.Contains(t, logs.String(), "SCM GitHub App installation token minted")
	assert.EqualValues(t, 1, f.mints.Load(), "the check minted the first token")
	_, num, err := dyn.OpenPR(context.Background(), "acme/web", "t", "b", "h", "main")
	require.NoError(t, err)
	assert.Equal(t, 9, num)
	assert.EqualValues(t, 1, f.mints.Load(), "the provider uses the token the check minted")

	w.CheckAndReloadForTest(context.Background())
	assert.EqualValues(t, 1, f.mints.Load(), "an unchanged Secret reloads nothing")

	// Rotate the key: GitHub now knows only the new one.
	k2, key2 := appKey(t, true)
	f.pub = &k2.PublicKey
	secret.Data["githubAppPrivateKey"] = key2
	require.NoError(t, kc.Update(context.Background(), secret))
	logs.Reset()
	w.CheckAndReloadForTest(context.Background())
	assert.Contains(t, logs.String(), "SCM credentials rotated — provider reloaded with GitHub App 4242 installation 777")
	_, _, err = dyn.OpenPR(context.Background(), "acme/web", "t", "b", "h", "main")
	require.NoError(t, err, "the new key mints")
	assert.EqualValues(t, 2, f.mints.Load())

	// Incomplete App keys: kept as it was.
	delete(secret.Data, "githubAppInstallationID")
	require.NoError(t, kc.Update(context.Background(), secret))
	logs.Reset()
	w.CheckAndReloadForTest(context.Background())
	assert.Contains(t, logs.String(), "githubAppInstallationID is missing")
	_, _, err = dyn.OpenPR(context.Background(), "acme/web", "t", "b", "h", "main")
	require.NoError(t, err, "the provider keeps the App")
	assert.NotContains(t, logs.String(), "PRIVATE KEY")
}
