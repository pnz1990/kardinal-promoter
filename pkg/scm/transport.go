// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import "net/http"

// transportSetter is implemented by the providers whose API client can take
// another http.RoundTripper.
type transportSetter interface {
	setTransport(rt http.RoundTripper)
}

// WithTransport makes p send its API requests through rt, when p supports
// it, and returns p. The Registry uses it to send the requests of
// ScmProviders, whose apiURL a namespace user writes, through
// egress.NewTransport.
func WithTransport(p SCMProvider, rt http.RoundTripper) SCMProvider {
	if s, ok := p.(transportSetter); ok && rt != nil {
		s.setTransport(rt)
	}
	return p
}

func (p *GitHubProvider) setTransport(rt http.RoundTripper)      { p.client.Transport = rt }
func (p *GitLabProvider) setTransport(rt http.RoundTripper)      { p.client.Transport = rt }
func (p *ForgejoProvider) setTransport(rt http.RoundTripper)     { p.client.Transport = rt }
func (p *BitbucketProvider) setTransport(rt http.RoundTripper)   { p.client.Transport = rt }
func (p *AzureDevOpsProvider) setTransport(rt http.RoundTripper) { p.client.Transport = rt }

var (
	_ transportSetter = (*GitHubProvider)(nil)
	_ transportSetter = (*GitLabProvider)(nil)
	_ transportSetter = (*ForgejoProvider)(nil)
	_ transportSetter = (*BitbucketProvider)(nil)
	_ transportSetter = (*AzureDevOpsProvider)(nil)
)
