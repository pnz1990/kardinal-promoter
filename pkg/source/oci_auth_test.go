// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// dockerConfig returns a .dockerconfigjson payload with one entry.
func dockerConfig(key, entry string) []byte {
	return []byte(fmt.Sprintf(`{"auths":{%q:%s}}`, key, entry))
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Host
}

// TestOCIWatcher_PrivateRegistryCredentials covers the credential flows of
// private registries: Basic auth on every request (distribution with
// htpasswd, ECR, Artifactory), Basic credentials at the Bearer token realm
// (Docker Hub, GHCR, Harbor, Quay, GCR), an OAuth2 refresh token (ACR
// identitytoken) and a ready bearer token (registrytoken). A wrong login is an
// error that names secretRef and never contains the password.
func TestOCIWatcher_PrivateRegistryCredentials(t *testing.T) {
	const user, pass = "robot$ci", "s3cret-pass"
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	tests := []struct {
		name      string
		auth      string // registry auth mode
		creds     func(host string) source.Credentials
		wantErr   string
		wantToken int // token requests
		wantAuthz string
	}{
		{name: "basic with username and password", auth: "basic",
			creds: func(string) source.Credentials { return source.Credentials{Username: user, Password: pass} }},
		{name: "basic with dockerconfigjson auth field", auth: "basic",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig(h, fmt.Sprintf(`{"auth":%q}`, auth))}
			}},
		{name: "basic with dockerconfigjson https key", auth: "basic",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig("https://"+h+"/v1/",
					fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass))}
			}},
		{name: "basic wrong password", auth: "basic",
			creds:   func(string) source.Credentials { return source.Credentials{Username: user, Password: "nope"} },
			wantErr: "with the credentials from secretRef"},
		{name: "basic needs username", auth: "basic",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig(h, `{"registrytoken":"x"}`)}
			},
			wantErr: "have no username and password"},
		{name: "bearer with login at the realm", auth: "bearer",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig(h, fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass))}
			},
			wantToken: 1, wantAuthz: "Basic " + auth},
		{name: "bearer wrong login", auth: "bearer",
			creds:     func(string) source.Credentials { return source.Credentials{Username: user, Password: "nope"} },
			wantErr:   "returned HTTP 401 with the credentials from secretRef",
			wantToken: 1},
		{name: "bearer identity token", auth: "bearer",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig(h,
					fmt.Sprintf(`{"username":"00000000-0000-0000-0000-000000000000","identitytoken":%q}`, pass))}
			},
			wantToken: 1},
		{name: "bearer registry token", auth: "bearer",
			creds: func(h string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig(h, `{"registrytoken":"anonymous-test-token"}`)}
			}},
		{name: "dockerconfigjson without the host", auth: "bearer",
			creds: func(string) source.Credentials {
				return source.Credentials{DockerConfigJSON: dockerConfig("other.example.com", `{"auth":"eDp5"}`)}
			},
			wantErr: "has no entry for registry"},
		{name: "dockerconfigjson not JSON", auth: "bearer",
			creds:   func(string) source.Credentials { return source.Credentials{DockerConfigJSON: []byte("{" + pass)} },
			wantErr: ".dockerconfigjson is not valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &testRegistry{name: "team/app", auth: tt.auth, user: user, pass: pass,
				tags: []string{"1.0.0", "1.1.0"}, images: imagesWithDigests("1.0.0", "1.1.0")}
			srv := reg.start(t)
			w := source.NewOCIWatcher(srv.URL+"/team/app", "").WithHTTPClient(srv.Client())
			w.Credentials = tt.creds(hostOf(t, srv.URL))

			res, err := w.Watch(context.Background(), "sha256:1.0.0")
			reg.mu.Lock()
			defer reg.mu.Unlock()
			assert.Equal(t, tt.wantToken, reg.tokenRequests, "token requests")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), pass, "the password is never in an error")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "1.1.0", res.Tag)
			assert.Equal(t, "sha256:1.1.0", res.Digest)
			assert.True(t, res.Changed)
			if tt.wantAuthz != "" {
				assert.Equal(t, []string{tt.wantAuthz}, reg.tokenAuthz)
			}
		})
	}
}

// TestOCIWatcher_TagFiltersAndStrategies covers the tag filters (allow,
// include, exclude, ignore, semver constraint) and the selection strategies.
// The same tag list always selects the same tag.
func TestOCIWatcher_TagFiltersAndStrategies(t *testing.T) {
	tags := []string{"1.2.0", "1.3.0", "1.3.1-rc.1", "v1.4.0", "2.0.0", "2026-10-01", "2026-10-08", "latest"}
	images := imagesWithDigests(tags...)
	img := images["2026-10-01"]
	img.created = "2026-10-01T00:00:00Z"
	images["2026-10-01"] = img
	img = images["2026-10-08"]
	img.created = "2026-10-08T00:00:00Z"
	images["2026-10-08"] = img
	tests := []struct {
		name     string
		filters  source.TagFilters
		strategy string
		limit    int
		want     string
		wantErr  string
	}{
		{name: "constraint below 2", filters: source.TagFilters{SemverConstraint: "<2.0.0"}, want: "v1.4.0"},
		{name: "caret constraint", filters: source.TagFilters{SemverConstraint: "^1.2"}, want: "v1.4.0"},
		{name: "tilde excludes prereleases", filters: source.TagFilters{SemverConstraint: "~1.3"}, want: "1.3.0"},
		{name: "prerelease constraint", filters: source.TagFilters{SemverConstraint: ">=1.3.1-0 <1.3.2"}, want: "1.3.1-rc.1"},
		{name: "ignore", filters: source.TagFilters{SemverConstraint: "<2", Ignore: []string{"v1.4.0"}}, want: "1.3.0"},
		{name: "allow", filters: source.TagFilters{Allow: []string{"1.2.0", "1.3.0", "missing"}}, want: "1.3.0"},
		{name: "exclude", filters: source.TagFilters{Include: `^\d`, Exclude: `^2|-rc`}, want: "1.3.0"},
		{name: "include dated, auto is newest build", filters: source.TagFilters{Include: `^\d{4}-`}, want: "2026-10-08"},
		{name: "lexical", filters: source.TagFilters{Include: `^\d{4}-`}, strategy: "Lexical", want: "2026-10-08"},
		{name: "semver strategy ignores the rest", strategy: "SemVer", want: "2.0.0"},
		{name: "newest build over limit", filters: source.TagFilters{Include: `^\d{4}-`}, strategy: "NewestBuild", limit: 1,
			wantErr: "limited to discoveryLimit 1 tags"},
		{name: "auto over limit", strategy: "Auto", limit: 3, wantErr: "is limited to 3 tags"},
		{name: "nothing passes", filters: source.TagFilters{SemverConstraint: ">=3"}, wantErr: `passes the tag filters (semverConstraint ">=3"; 8 tags listed)`},
		{name: "bad constraint", filters: source.TagFilters{SemverConstraint: "not a constraint"}, wantErr: "invalid semverConstraint"},
		{name: "bad exclude", filters: source.TagFilters{Exclude: "("}, wantErr: "invalid excludeTagFilter regex"},
		{name: "unknown strategy", strategy: "Random", wantErr: `unknown strategy "Random"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &testRegistry{name: "team/app", tags: tags, images: images}
			srv := reg.start(t)
			for range 2 { // deterministic
				w := source.NewOCIWatcher(srv.URL+"/team/app", "").WithHTTPClient(srv.Client())
				w.Filters, w.Strategy, w.DiscoveryLimit = tt.filters, tt.strategy, tt.limit
				res, err := w.Watch(context.Background(), "")
				if tt.wantErr != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.wantErr)
					continue
				}
				require.NoError(t, err)
				assert.Equal(t, tt.want, res.Tag)
				assert.False(t, res.Changed, "the first poll is the baseline")
			}
		})
	}
}
