// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// SCM kinds the scenario suite serves. Each is kardinal's real provider
// client (pkg/scm) talking to SCMServer's fake of that host's REST API.
const (
	SCMGitHub  = "github"
	SCMGitLab  = "gitlab"
	SCMForgejo = "forgejo"
)

// SCMHost is the web host of a kind's repositories in the scenario
// Pipelines' spec.git.url.
var SCMHost = map[string]string{
	SCMGitHub:  "github.com",
	SCMGitLab:  "gitlab.example.com",
	SCMForgejo: "forgejo.example.com",
}

// PR is a pull request (a GitLab merge request) on the fake SCM.
type PR struct {
	Repo     string
	Number   int
	Title    string
	Body     string
	Head     string
	Base     string
	HeadSHA  string
	State    string // open, closed, merged
	MergeSHA string
	// MergedBy is who merged it (MergeAs); the API reports it as GitHub's
	// merged_by, GitLab's merge_user and Forgejo's merged_by.
	MergedBy  string
	Labels    []string
	Comments  []string
	Approvals []string
	Reviewers []string
	Assignees []string
	AutoMerge bool
}

// SCMServer fakes the pull request API of GitHub, GitLab or Forgejo over
// HTTP for kardinal's provider clients. Merging a PR (Merge) merges its
// branch in the GitServer, as the host does.
type SCMServer struct {
	Kind string
	git  *GitServer
	srv  *httptest.Server

	mu       sync.Mutex
	prs      map[string][]*PR
	statuses map[string]map[string]string // sha -> context -> state
	labelIDs map[string]int
	// unknown are the requests no route served: the fake is missing an API
	// kardinal calls, and the scenario fails.
	unknown []string

	// OnChange, when set, is told of every request that changed something
	// (a side effect of the controller, World.CrashAfter); Dying, when it
	// reports true, drops requests that would change something.
	OnChange func(what string)
	Dying    func() bool
}

// statusRecorder keeps the status code a route answered.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// NewSCMServer starts the fake API of kind over git.
func NewSCMServer(kind string, git *GitServer) *SCMServer {
	s := &SCMServer{Kind: kind, git: git, prs: map[string][]*PR{}, statuses: map[string]map[string]string{}, labelIDs: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the API URL to configure the provider with.
func (s *SCMServer) URL() string { return s.srv.URL }

// Close stops the server.
func (s *SCMServer) Close() { s.srv.Close() }

// Unknown returns the requests no route served.
func (s *SCMServer) Unknown() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unknown...)
}

// repoURL is the git URL of repo (owner/name or a GitLab project path).
func (s *SCMServer) repoURL(repo string) string {
	return "https://" + SCMHost[s.Kind] + "/" + repo
}

// PRs returns a copy of every PR, by repository then number.
func (s *SCMServer) PRs() []PR {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PR
	repos := make([]string, 0, len(s.prs))
	for r := range s.prs {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		for _, p := range s.prs[r] {
			c := *p
			c.Labels = append([]string(nil), p.Labels...)
			c.Comments = append([]string(nil), p.Comments...)
			out = append(out, c)
		}
	}
	return out
}

// Approve records an approving review by user.
func (s *SCMServer) Approve(repo string, number int, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(repo, number)
	if p == nil {
		return fmt.Errorf("scm: no PR %s#%d", repo, number)
	}
	p.Approvals = append(p.Approvals, user)
	return nil
}

// MergeAs merges an open PR the way user clicking merge does: the API then
// names user as the merger.
func (s *SCMServer) MergeAs(repo string, number int, user string) error {
	s.mu.Lock()
	p := s.find(repo, number)
	if p == nil || p.State != "open" {
		s.mu.Unlock()
		return fmt.Errorf("scm: PR %s#%d is not open", repo, number)
	}
	head, base := p.Head, p.Base
	s.mu.Unlock()
	sha, err := s.git.Merge(s.repoURL(repo), head, base)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.State, p.MergeSHA, p.MergedBy = "merged", sha, user
	return nil
}

func (s *SCMServer) find(repo string, number int) *PR {
	for _, p := range s.prs[repo] {
		if p.Number == number {
			return p
		}
	}
	return nil
}

func (s *SCMServer) nextNumber(repo string) int {
	return len(s.prs[repo]) + 1
}

func (s *SCMServer) headSHA(repo, branch string) string {
	sha, _ := s.git.Head(s.repoURL(repo), branch)
	return sha
}

type route struct {
	method string
	re     *regexp.Regexp
	fn     func(s *SCMServer, w http.ResponseWriter, m []string, body map[string]interface{}, q url.Values)
}

func (s *SCMServer) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	path := r.URL.EscapedPath()
	var routes []route
	switch s.Kind {
	case SCMGitHub:
		routes = githubRoutes
		path = strings.TrimPrefix(path, "/api/v3")
	case SCMGitLab:
		routes = gitlabRoutes
	case SCMForgejo:
		routes = forgejoRoutes
	}
	for _, rt := range routes {
		if rt.method != r.Method {
			continue
		}
		if m := rt.re.FindStringSubmatch(path); m != nil {
			for i := range m {
				if u, err := url.PathUnescape(m[i]); err == nil {
					m[i] = u
				}
			}
			if r.Method != http.MethodGet && s.Dying != nil && s.Dying() {
				// The controller that sent it is dead: it never arrives.
				http.Error(w, `{"message":"connection reset"}`, http.StatusBadGateway)
				return
			}
			rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
			s.mu.Lock()
			rt.fn(s, rec, m, body, r.URL.Query())
			s.mu.Unlock()
			if r.Method != http.MethodGet && rec.code < 300 && s.OnChange != nil {
				s.OnChange(r.Method + " " + path)
			}
			return
		}
	}
	s.mu.Lock()
	s.unknown = append(s.unknown, r.Method+" "+path)
	s.mu.Unlock()
	http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func str(body map[string]interface{}, key string) string {
	v, _ := body[key].(string)
	return v
}

func strs(v interface{}) []string {
	var out []string
	switch x := v.(type) {
	case []interface{}:
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
	case string:
		if x != "" {
			out = strings.Split(x, ",")
		}
	}
	return out
}

// openPR creates a PR, or answers the host's duplicate error.
func (s *SCMServer) openPR(repo, title, body, head, base string) (*PR, bool) {
	for _, p := range s.prs[repo] {
		if p.State == "open" && p.Head == head && p.Base == base {
			return p, false
		}
	}
	p := &PR{Repo: repo, Number: s.nextNumber(repo), Title: title, Body: body, Head: head, Base: base, State: "open"}
	s.mu.Unlock()
	p.HeadSHA = s.headSHA(repo, head)
	s.mu.Lock()
	s.prs[repo] = append(s.prs[repo], p)
	return p, true
}

func (s *SCMServer) webURL(p *PR) string {
	if s.Kind == SCMGitLab {
		return fmt.Sprintf("https://%s/%s/-/merge_requests/%d", SCMHost[s.Kind], p.Repo, p.Number)
	}
	return fmt.Sprintf("https://%s/%s/pull/%d", SCMHost[s.Kind], p.Repo, p.Number)
}

func (s *SCMServer) prJSON(p *PR) map[string]interface{} {
	state := p.State
	if state == "merged" {
		state = "closed"
	}
	labels := make([]interface{}, 0, len(p.Labels))
	for _, l := range p.Labels {
		labels = append(labels, map[string]interface{}{"name": l, "id": s.labelID(l)})
	}
	var mergedBy interface{}
	if p.MergedBy != "" {
		mergedBy = map[string]interface{}{"login": p.MergedBy}
	}
	return map[string]interface{}{
		"merged_by": mergedBy,
		"number":    p.Number, "html_url": s.webURL(p), "state": state, "merged": p.State == "merged",
		"merge_commit_sha": p.MergeSHA, "node_id": fmt.Sprintf("PR_%d", p.Number), "title": p.Title, "body": p.Body,
		"head":   map[string]interface{}{"ref": p.Head, "label": p.Head, "sha": p.HeadSHA},
		"base":   map[string]interface{}{"ref": p.Base},
		"labels": labels, "mergeable": true,
	}
}

func (s *SCMServer) labelID(name string) int {
	if id, ok := s.labelIDs[name]; ok {
		return id
	}
	id := len(s.labelIDs) + 1
	s.labelIDs[name] = id
	return id
}

func (s *SCMServer) prOr404(w http.ResponseWriter, repo, num string) *PR {
	n, _ := strconv.Atoi(num)
	p := s.find(repo, n)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
	return p
}

func (s *SCMServer) setStatus(sha, ctx, state string) {
	if s.statuses[sha] == nil {
		s.statuses[sha] = map[string]string{}
	}
	s.statuses[sha][ctx] = state
}

// Statuses returns the commit statuses of sha by context.
func (s *SCMServer) Statuses(sha string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.statuses[sha] {
		out[k] = v
	}
	return out
}

const repoRe = `([^/]+/[^/]+)`

// githubMaxBody is the longest pull request body GitHub takes, in
// characters.
const githubMaxBody = 65536

// bodyTooLong answers GitHub's 422 for a pull request body over
// githubMaxBody characters.
func bodyTooLong(w http.ResponseWriter, b map[string]interface{}) bool {
	if utf8.RuneCountInString(str(b, "body")) <= githubMaxBody {
		return false
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"message": "Validation Failed",
		"errors": []map[string]string{{"resource": "PullRequest", "code": "custom", "field": "body",
			"message": "body is too long (maximum is 65536 characters)"}}})
	return true
}

var githubRoutes = []route{
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/pulls$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if bodyTooLong(w, b) {
			return
		}
		p, created := s.openPR(m[1], str(b, "title"), str(b, "body"), str(b, "head"), str(b, "base"))
		if !created {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed: A pull request already exists for " + p.Head})
			return
		}
		writeJSON(w, http.StatusCreated, s.prJSON(p))
	}},
	{"GET", regexp.MustCompile(`^/repos/` + repoRe + `/pulls$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, q url.Values) {
		head := q.Get("head")
		if i := strings.Index(head, ":"); i >= 0 {
			head = head[i+1:]
		}
		out := []interface{}{}
		for _, p := range s.prs[m[1]] {
			if (q.Get("state") == "" || q.Get("state") == "all" || (q.Get("state") == "open") == (p.State == "open")) &&
				(head == "" || p.Head == head) && (q.Get("base") == "" || p.Base == q.Get("base")) {
				out = append(out, s.prJSON(p))
			}
		}
		writeJSON(w, http.StatusOK, out)
	}},
	{"GET", regexp.MustCompile(`^/repos/` + repoRe + `/pulls/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			writeJSON(w, http.StatusOK, s.prJSON(p))
		}
	}},
	{"PATCH", regexp.MustCompile(`^/repos/` + repoRe + `/pulls/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if bodyTooLong(w, b) {
			return
		}
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			if str(b, "state") == "closed" && p.State == "open" {
				p.State = "closed"
			}
			if t := str(b, "title"); t != "" {
				p.Title = t
			}
			if v, ok := b["body"].(string); ok {
				p.Body = v
			}
			writeJSON(w, http.StatusOK, s.prJSON(p))
		}
	}},
	{"GET", regexp.MustCompile(`^/repos/` + repoRe + `/pulls/(\d+)/reviews$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, q url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			out := []interface{}{}
			if q.Get("page") == "" || q.Get("page") == "1" {
				for _, u := range p.Approvals {
					out = append(out, map[string]interface{}{"user": map[string]string{"login": u}, "state": "APPROVED"})
				}
			}
			writeJSON(w, http.StatusOK, out)
		}
	}},
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/issues/(\d+)/comments$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Comments = append(p.Comments, str(b, "body"))
			writeJSON(w, http.StatusCreated, map[string]interface{}{"id": len(p.Comments)})
		}
	}},
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/issues/(\d+)/labels$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Labels = appendUnique(p.Labels, strs(b["labels"])...)
			writeJSON(w, http.StatusOK, []interface{}{})
		}
	}},
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/issues/(\d+)/assignees$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Assignees = appendUnique(p.Assignees, strs(b["assignees"])...)
			writeJSON(w, http.StatusCreated, s.prJSON(p))
		}
	}},
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/pulls/(\d+)/requested_reviewers$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Reviewers = appendUnique(p.Reviewers, strs(b["reviewers"])...)
			writeJSON(w, http.StatusCreated, s.prJSON(p))
		}
	}},
	{"POST", regexp.MustCompile(`^/repos/` + repoRe + `/statuses/([0-9a-f]+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		s.setStatus(m[2], str(b, "context"), str(b, "state"))
		writeJSON(w, http.StatusCreated, map[string]string{"state": str(b, "state")})
	}},
	{"DELETE", regexp.MustCompile(`^/repos/` + repoRe + `/git/refs/heads/(.+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		s.deleteBranch(w, m[1], m[2])
	}},
}

var gitlabRoutes = []route{
	{"POST", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		p, created := s.openPR(m[1], str(b, "title"), str(b, "description"), str(b, "source_branch"), str(b, "target_branch"))
		if !created {
			writeJSON(w, http.StatusConflict, map[string]interface{}{"message": []string{"Another open merge request already exists for this source branch"}})
			return
		}
		writeJSON(w, http.StatusCreated, s.mrJSON(p))
	}},
	{"GET", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, q url.Values) {
		out := []interface{}{}
		for _, p := range s.prs[m[1]] {
			if (q.Get("state") == "" || (q.Get("state") == "opened") == (p.State == "open")) &&
				(q.Get("source_branch") == "" || q.Get("source_branch") == p.Head) &&
				(q.Get("target_branch") == "" || q.Get("target_branch") == p.Base) {
				out = append(out, s.mrJSON(p))
			}
		}
		writeJSON(w, http.StatusOK, out)
	}},
	{"GET", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			writeJSON(w, http.StatusOK, s.mrJSON(p))
		}
	}},
	{"PUT", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			if str(b, "state_event") == "close" && p.State == "open" {
				p.State = "closed"
			}
			p.Labels = appendUnique(p.Labels, strs(b["add_labels"])...)
			if v, ok := b["description"].(string); ok {
				p.Body = v
			}
			if ids, ok := b["reviewer_ids"]; ok {
				p.Reviewers = appendUnique(p.Reviewers, strs(ids)...)
			}
			if ids, ok := b["assignee_ids"]; ok {
				p.Assignees = appendUnique(p.Assignees, strs(ids)...)
			}
			writeJSON(w, http.StatusOK, s.mrJSON(p))
		}
	}},
	{"GET", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests/(\d+)/approvals$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			by := []interface{}{}
			for _, u := range p.Approvals {
				by = append(by, map[string]interface{}{"user": map[string]string{"username": u}})
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"approved_by": by})
		}
	}},
	{"POST", regexp.MustCompile(`^/api/v4/projects/([^/]+)/merge_requests/(\d+)/notes$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Comments = append(p.Comments, str(b, "body"))
			writeJSON(w, http.StatusCreated, map[string]interface{}{"id": len(p.Comments)})
		}
	}},
	{"POST", regexp.MustCompile(`^/api/v4/projects/([^/]+)/statuses/([0-9a-f]+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		s.setStatus(m[2], str(b, "name"), str(b, "state"))
		writeJSON(w, http.StatusCreated, map[string]string{"status": str(b, "state")})
	}},
	{"GET", regexp.MustCompile(`^/api/v4/users$`), func(s *SCMServer, w http.ResponseWriter, _ []string, _ map[string]interface{}, q url.Values) {
		writeJSON(w, http.StatusOK, []interface{}{map[string]interface{}{"id": s.labelID("user:" + q.Get("username")), "username": q.Get("username")}})
	}},
	{"DELETE", regexp.MustCompile(`^/api/v4/projects/([^/]+)/repository/branches/(.+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		s.deleteBranch(w, m[1], m[2])
	}},
}

func (s *SCMServer) mrJSON(p *PR) map[string]interface{} {
	state := map[string]string{"open": "opened", "closed": "closed", "merged": "merged"}[p.State]
	var mergeUser interface{}
	if p.MergedBy != "" {
		mergeUser = map[string]interface{}{"username": p.MergedBy}
	}
	return map[string]interface{}{
		"merge_user": mergeUser,
		"iid":        p.Number, "id": p.Number, "web_url": s.webURL(p), "state": state, "title": p.Title, "description": p.Body,
		"source_branch": p.Head, "target_branch": p.Base, "sha": p.HeadSHA, "merge_commit_sha": p.MergeSHA,
		"labels": p.Labels, "detailed_merge_status": "mergeable", "reviewers": []interface{}{}, "assignees": []interface{}{},
	}
}

var forgejoRoutes = []route{
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		p, created := s.openPR(m[1], str(b, "title"), str(b, "body"), str(b, "head"), str(b, "base"))
		if !created {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "pull request already exists for these targets"})
			return
		}
		writeJSON(w, http.StatusCreated, s.prJSON(p))
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, q url.Values) {
		out := []interface{}{}
		if q.Get("page") == "" || q.Get("page") == "1" {
			for _, p := range s.prs[m[1]] {
				if q.Get("state") == "" || q.Get("state") == "all" || (q.Get("state") == "open") == (p.State == "open") {
					out = append(out, s.prJSON(p))
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			writeJSON(w, http.StatusOK, s.prJSON(p))
		}
	}},
	{"PATCH", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			if str(b, "state") == "closed" && p.State == "open" {
				p.State = "closed"
			}
			if v, ok := b["body"].(string); ok {
				p.Body = v
			}
			writeJSON(w, http.StatusCreated, s.prJSON(p))
		}
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls/(\d+)/reviews$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			out := []interface{}{}
			for _, u := range p.Approvals {
				out = append(out, map[string]interface{}{"user": map[string]string{"login": u}, "state": "APPROVED"})
			}
			writeJSON(w, http.StatusOK, out)
		}
	}},
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/pulls/(\d+)/requested_reviewers$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Reviewers = appendUnique(p.Reviewers, strs(b["reviewers"])...)
			writeJSON(w, http.StatusCreated, []interface{}{})
		}
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/issues/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			as := []interface{}{}
			for _, a := range p.Assignees {
				as = append(as, map[string]string{"login": a})
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"number": p.Number, "assignees": as})
		}
	}},
	{"PATCH", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/issues/(\d+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Assignees = appendUnique(p.Assignees, strs(b["assignees"])...)
			writeJSON(w, http.StatusCreated, map[string]interface{}{"number": p.Number})
		}
	}},
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/issues/(\d+)/comments$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			p.Comments = append(p.Comments, str(b, "body"))
			writeJSON(w, http.StatusCreated, map[string]interface{}{"id": len(p.Comments)})
		}
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/labels$`), func(s *SCMServer, w http.ResponseWriter, _ []string, _ map[string]interface{}, q url.Values) {
		out := []interface{}{}
		if q.Get("page") == "" || q.Get("page") == "1" {
			names := make([]string, 0, len(s.labelIDs))
			for n := range s.labelIDs {
				if !strings.HasPrefix(n, "user:") {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			for _, n := range names {
				out = append(out, map[string]interface{}{"id": s.labelIDs[n], "name": n})
			}
		}
		writeJSON(w, http.StatusOK, out)
	}},
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/labels$`), func(s *SCMServer, w http.ResponseWriter, _ []string, b map[string]interface{}, _ url.Values) {
		writeJSON(w, http.StatusCreated, map[string]interface{}{"id": s.labelID(str(b, "name")), "name": str(b, "name")})
	}},
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/issues/(\d+)/labels$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		if p := s.prOr404(w, m[1], m[2]); p != nil {
			byID := map[string]string{}
			for n, id := range s.labelIDs {
				byID[strconv.Itoa(id)] = n
			}
			for _, id := range strs(b["labels"]) {
				if n, ok := byID[id]; ok {
					p.Labels = appendUnique(p.Labels, n)
				}
			}
			writeJSON(w, http.StatusOK, []interface{}{})
		}
	}},
	{"POST", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/statuses/([0-9a-f]+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, b map[string]interface{}, _ url.Values) {
		s.setStatus(m[2], str(b, "context"), str(b, "state"))
		writeJSON(w, http.StatusCreated, map[string]string{"status": str(b, "state")})
	}},
	{"GET", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/branches/(.+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		s.mu.Unlock()
		sha, _ := s.git.Head(s.repoURL(m[1]), m[2])
		s.mu.Lock()
		if sha == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "branch not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"name": m[2], "commit": map[string]string{"id": sha}})
	}},
	{"DELETE", regexp.MustCompile(`^/api/v1/repos/` + repoRe + `/branches/(.+)$`), func(s *SCMServer, w http.ResponseWriter, m []string, _ map[string]interface{}, _ url.Values) {
		s.deleteBranch(w, m[1], m[2])
	}},
}

// deleteBranch deletes a PR head branch, as the host's API does.
func (s *SCMServer) deleteBranch(w http.ResponseWriter, repo, branch string) {
	s.mu.Unlock()
	sha, _ := s.git.Head(s.repoURL(repo), branch)
	var err error
	if sha != "" {
		err = s.git.DeleteBranch(s.repoURL(repo), branch)
	}
	s.mu.Lock()
	switch {
	case sha == "":
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Reference does not exist"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func appendUnique(list []string, add ...string) []string {
	for _, a := range add {
		found := false
		for _, l := range list {
			if l == a {
				found = true
				break
			}
		}
		if !found && a != "" {
			list = append(list, a)
		}
	}
	return list
}
