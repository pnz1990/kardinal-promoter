// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Credentials are the secrets a watcher authenticates with. The Subscription
// reconciler reads them from the Secret spec.<type>.secretRef names; the zero
// value means anonymous access. Credentials are never logged and never
// included in an error or status message.
type Credentials struct {
	// DockerConfigJSON is a kubernetes.io/dockerconfigjson payload
	// (.dockerconfigjson). The entry for the registry host is used.
	DockerConfigJSON []byte
	// Username and Password are basic credentials (keys username and
	// password): a registry login, Helm repository basic auth, or a Git
	// HTTPS user and password.
	Username string
	Password string
	// Token is a Git HTTPS token (key token), sent as the password.
	Token string
	// SSHPrivateKey is a PEM private key for Git over SSH (key ssh-privatekey).
	SSHPrivateKey []byte
	// SSHKnownHosts is the known_hosts content the SSH host key is checked
	// against (key known_hosts). Required for SSH.
	SSHKnownHosts []byte
}

// IsZero reports whether c holds no credential.
func (c Credentials) IsZero() bool {
	return len(c.DockerConfigJSON) == 0 && c.Username == "" && c.Password == "" && c.Token == "" &&
		len(c.SSHPrivateKey) == 0 && len(c.SSHKnownHosts) == 0
}

// registryAuth is the credential for one registry host.
type registryAuth struct {
	username, password string
	// identityToken is an OAuth2 refresh token (docker config identitytoken,
	// ACR): exchanged at the token realm with grant_type=refresh_token.
	identityToken string
	// registryToken is a bearer token sent as is (docker config registrytoken).
	registryToken string
}

func (a registryAuth) isZero() bool {
	return a.username == "" && a.password == "" && a.identityToken == "" && a.registryToken == ""
}

func (a registryAuth) hasBasic() bool { return a.username != "" || a.password != "" }

// dockerConfig is the subset of a docker config.json the watcher reads.
type dockerConfig struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

type dockerAuthEntry struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Auth          string `json:"auth"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// registryAuthFor returns the credential for registry host (host[:port]).
// A docker config entry for the host wins over username and password; no
// matching entry and no username is anonymous access, not an error, unless
// the docker config does not parse.
func (c Credentials) registryAuthFor(host string) (registryAuth, error) {
	if len(c.DockerConfigJSON) > 0 {
		var cfg dockerConfig
		if err := json.Unmarshal(c.DockerConfigJSON, &cfg); err != nil {
			// The payload is not echoed: it holds the credentials.
			return registryAuth{}, fmt.Errorf("the credentials Secret's .dockerconfigjson is not valid JSON")
		}
		want := normalizeRegistryHost(host)
		for key, e := range cfg.Auths {
			if normalizeRegistryHost(key) != want {
				continue
			}
			a := registryAuth{username: e.Username, password: e.Password,
				identityToken: e.IdentityToken, registryToken: e.RegistryToken}
			if e.Auth != "" && a.username == "" && a.password == "" {
				raw, err := base64.StdEncoding.DecodeString(e.Auth)
				if err != nil {
					return registryAuth{}, fmt.Errorf("the credentials Secret's .dockerconfigjson entry for %s has an auth value that is not base64", host)
				}
				a.username, a.password, _ = strings.Cut(string(raw), ":")
			}
			return a, nil
		}
		if c.Username == "" && c.Password == "" {
			return registryAuth{}, fmt.Errorf("the credentials Secret's .dockerconfigjson has no entry for registry %s", host)
		}
	}
	return registryAuth{username: c.Username, password: c.Password}, nil
}

// normalizeRegistryHost turns a docker config auths key ("https://index.docker.io/v1/",
// "ghcr.io", "http://registry:5000") or a registry host into a comparable
// host[:port]. Every Docker Hub name maps to registry-1.docker.io.
func normalizeRegistryHost(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	if _, rest, ok := strings.Cut(k, "://"); ok {
		k = rest
	}
	k, _, _ = strings.Cut(k, "/")
	switch k {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return dockerHubRegistry
	}
	return k
}
