// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// changeRequestSection is the golden Change Requests section of
// changeRequestBody: a verified ServiceNow change with its approvers, a
// linked Jira issue that is not verified, and a template whose ticket is not
// open yet.
const changeRequestSection = `

### Change Requests

| Change request | Ticket | Provider | State | Verified | Approvers | Read at |
|---|---|---|---|---|---|---|
| prod-change | [CHG0031337](https://acme.service-now.com/nav_to.do?uri=change_request.do%3Fsys_id=abc) | servicenow | approved (Scheduled) | yes | Jane Smith, Raj Patel | 2026-10-14T09:05Z |
| jira-change | [OPS-42](https://acme.atlassian.net/browse/OPS-42) | jira | approved (Approved), timed out | no: the issue was created before the version | — | 2026-10-14T09:06Z |
| cab-review | — | servicenow | no ticket yet | no | — | — |
`

func changeRequestBody() scm.PRBody {
	at := func(m int) *metav1.Time {
		t := metav1.NewTime(time.Date(2026, 10, 14, 9, m, 30, 0, time.UTC))
		return &t
	}
	return scm.PRBody{PipelineName: "app", Environment: "prod", BundleName: "app-v1",
		Bundle: v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "1.5.0"}}},
		ChangeRequests: []scm.ChangeRequest{
			{Name: "prod-change", Provider: "servicenow", Ticket: "CHG0031337",
				URL:   "https://acme.service-now.com/nav_to.do?uri=change_request.do%3Fsys_id=abc",
				State: "approved", ProviderState: "Scheduled", Verified: true, Approvers: []string{"Jane Smith", "Raj Patel"}, ObservedAt: at(5)},
			{Name: "jira-change", Provider: "jira", Ticket: "OPS-42", URL: "https://acme.atlassian.net/browse/OPS-42",
				State: "approved", ProviderState: "Approved", TimedOut: true,
				VerifiedReason: "the issue was created before the version", ObservedAt: at(6)},
			{Name: "cab-review", Provider: "servicenow"},
		}}
}

// TestPRBody_ChangeRequests (spec 23 section 9): the PR body has a Change
// Requests section after the gates, one row per ticket of the environment:
// the key linked to its URL, the provider, the state with the provider's
// own, whether the ticket is the promotion's (and why not), the approvers
// and when it was read. A body without change requests has no section.
//
// Covers CHANGE-REQUEST-11.
func TestPRBody_ChangeRequests(t *testing.T) {
	body, err := scm.RenderPRBody(changeRequestBody())
	require.NoError(t, err)
	assert.Contains(t, body, changeRequestSection)
	gates, crs := strings.Index(body, "### Policy Gate Compliance"), strings.Index(body, "### Change Requests")
	upstream := strings.Index(body, "### Upstream Verification")
	assert.True(t, gates >= 0 && gates < crs && crs < upstream, "between the gates and the upstream table")

	plain := changeRequestBody()
	plain.ChangeRequests = nil
	body, err = scm.RenderPRBody(plain)
	require.NoError(t, err)
	assert.NotContains(t, body, "Change Requests")
}

// TestPRBody_ChangeRequestLinks: the ticket's key and URL come from the
// ticketing system, so neither can break the table or link elsewhere: a
// URL that is not absolute http(s) (javascript:, relative, with userinfo)
// is not linked, the characters of a URL that could end the link or the
// cell are percent-encoded, and a key's brackets and pipes are escaped.
func TestPRBody_ChangeRequestLinks(t *testing.T) {
	for _, tc := range []struct {
		name, key, url, cell string
	}{
		{"https", "CHG1", "https://x.example.com/c/1", "[CHG1](https://x.example.com/c/1)"},
		{"http", "CHG1", "http://x.example.com/c/1", "[CHG1](http://x.example.com/c/1)"},
		{"javascript", "CHG1", "javascript:alert(1)", "CHG1"},
		{"relative", "CHG1", "/c/1", "CHG1"},
		{"userinfo", "CHG1", "https://u:p@x.example.com/c/1", "CHG1"},
		{"no URL", "CHG1", "", "CHG1"},
		{"closing paren and pipe", "CHG1", "https://x.example.com/c/1)|x", "[CHG1](https://x.example.com/c/1%29%7Cx)"},
		{"key brackets and pipe", "a]b[c|d", "https://x.example.com/", `[a\]b\[c\|d](https://x.example.com/)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := changeRequestBody()
			b.ChangeRequests = []scm.ChangeRequest{{Name: "c", Ticket: tc.key, URL: tc.url, State: "pending"}}
			body, err := scm.RenderPRBody(b)
			require.NoError(t, err)
			assert.Contains(t, body, "\n| c | "+tc.cell+" | — | pending | no | — | — |\n")
		})
	}
	t.Run("free text stays in its cell", func(t *testing.T) {
		b := changeRequestBody()
		b.ChangeRequests = []scm.ChangeRequest{{Name: "c|d", Ticket: "K", State: "pending", ProviderState: "a|b\nc",
			VerifiedReason: "x|y", Approvers: []string{"p|q", "r\ns"}}}
		body, err := scm.RenderPRBody(b)
		require.NoError(t, err)
		assert.Contains(t, body, "\n| c\\|d | K | — | pending (a\\|b c) | no: x\\|y | p\\|q, r s | — |\n")
	})
}

// TestPRTemplate_ChangeRequestsTable: a custom body gets the section as
// changeRequestsTable, without the blank lines around it, and empty without
// change requests.
func TestPRTemplate_ChangeRequestsTable(t *testing.T) {
	render := func(b scm.PRBody) string {
		t.Helper()
		out, err := scm.RenderPR(&v1alpha1.PRConfig{BodyTemplate: "before\n{{ changeRequestsTable }}\nafter"}, "title", b)
		require.NoError(t, err)
		return out.Body
	}
	const marker = "<!-- kardinal-promoter auto-generated PR -->\n"
	assert.Equal(t, marker+"before\n"+strings.Trim(changeRequestSection, "\n")+"\nafter", render(changeRequestBody()))
	plain := changeRequestBody()
	plain.ChangeRequests = nil
	assert.Equal(t, marker+"before\n\nafter", render(plain))
}

// TestPRBody_ChangeRequestTextInert (#2170 QA): approver names, states,
// reasons and keys come from the ticketing system, where anyone may set a
// display name. In the prod PR body each is inert text: no HTML, no image,
// no link (markdown or bare), no mention of a person or team, no code span,
// and an escape in the text cannot cancel kardinal's.
func TestPRBody_ChangeRequestTextInert(t *testing.T) {
	const zw = "\u200b"
	for _, tc := range []struct {
		name, in, out string
	}{
		{"html", `<a href="https://phish.example">click</a>`, `&lt;a href="https:` + zw + `//phish.example"&gt;click&lt;/a&gt;`},
		{"image", `![x](https://evil.example/t.png)`, `\!\[x\](https:` + zw + `//evil.example/t.png)`},
		{"mention", `@pnz1990/admins`, `@` + zw + `pnz1990/admins`},
		{"link", `[approve](https://phish.example)`, `\[approve\](https:` + zw + `//phish.example)`},
		{"bare URL", `https://phish.example/approve`, `https:` + zw + `//phish.example/approve`},
		{"entity", `&lt;img&gt;`, `&amp;lt;img&amp;gt;`},
		{"escape cancelling", `\[x\](https://e)`, `\\\[x\\\](https:` + zw + `//e)`},
		{"code and emphasis", "`x` *b* _i_ ~s~", "\\`x\\` \\*b\\* \\_i\\_ \\~s\\~"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := changeRequestBody()
			b.ChangeRequests = []scm.ChangeRequest{{Name: "c", Ticket: tc.in, State: "approved", ProviderState: tc.in,
				VerifiedReason: tc.in, Approvers: []string{tc.in}}}
			body, err := scm.RenderPRBody(b)
			require.NoError(t, err)
			section := body[strings.Index(body, "### Change Requests"):strings.Index(body, "### Upstream Verification")]
			assert.Contains(t, section, "\n| c | "+tc.out+" | — | approved ("+tc.out+") | no: "+tc.out+" | "+tc.out+" | — |\n")
			for _, raw := range []string{"<a", "<img", "](", "@pnz1990", "https://", "`x`", "*b*"} {
				assert.NotContains(t, strings.ReplaceAll(section, `\](`, ""), raw, "%s reaches the PR", raw)
			}
		})
	}
}
